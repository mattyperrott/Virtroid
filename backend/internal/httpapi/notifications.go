package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"virtroid/backend/internal/push"
	"virtroid/backend/internal/store"
)

const (
	maxNotificationSubscriptionBodyBytes = 16 << 10
	maxRuntimeNotificationBodyBytes      = 8 << 10
	maxNotificationPublicKeyChars        = 2048
	maxNotificationPackageChars          = 255
	maxNotificationLabelRunes            = 100
	maxNotificationTitleRunes            = 200
	notificationDeliveryTTL              = 7 * 24 * time.Hour
	notificationStreamHeartbeat          = 15 * time.Second
	notificationStreamBatchSize          = 20
)

var notificationPackagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

type runtimeNotificationEvent struct {
	EventID     string    `json:"event_id"`
	PackageName string    `json:"package_name"`
	AppLabel    string    `json:"app_label"`
	PostedAt    time.Time `json:"posted_at"`
	Title       string    `json:"title"`
}

type securityNoticeEvent struct {
	EventID    string
	Source     string
	Severity   string
	Summary    string
	ObservedAt time.Time
}

type notificationStreamHub struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func newNotificationStreamHub() *notificationStreamHub {
	return &notificationStreamHub{subscribers: map[string]map[chan struct{}]struct{}{}}
}

func (h *notificationStreamHub) subscribe(deviceID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subscribers[deviceID] == nil {
		h.subscribers[deviceID] = map[chan struct{}]struct{}{}
	}
	h.subscribers[deviceID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers[deviceID], ch)
		if len(h.subscribers[deviceID]) == 0 {
			delete(h.subscribers, deviceID)
		}
		h.mu.Unlock()
	}
}

func (h *notificationStreamHub) notify(deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers[deviceID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (a *API) upsertMyNotificationSubscription(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		DeviceID            string `json:"device_id"`
		EncryptionPublicKey string `json:"encryption_public_key"`
	}
	if err := decodeStrictJSON(r.Body, maxNotificationSubscriptionBodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid notification subscription"})
		return
	}
	if strings.TrimSpace(req.DeviceID) != deviceID ||
		len(req.EncryptionPublicKey) == 0 || len(req.EncryptionPublicKey) > maxNotificationPublicKeyChars ||
		push.ValidateEncryptionPublicKey(req.EncryptionPublicKey) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid notification subscription"})
		return
	}
	if err := a.store.UpsertDeviceNotificationSubscription(r.Context(), store.DeviceNotificationSubscription{
		DeviceID:            deviceID,
		AccountID:           accountID,
		EncryptionPublicKey: strings.TrimSpace(req.EncryptionPublicKey),
	}); err != nil {
		if errors.Is(err, store.ErrDeviceNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "notification_subscription_update_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "transport": "virtroid_stream"})
}

func (a *API) deleteMyNotificationSubscription(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteDeviceNotificationSubscription(r.Context(), accountID, deviceID); err != nil &&
		!errors.Is(err, store.ErrNotificationSubscription) {
		writeInternalAPIError(w, "notification_subscription_delete_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) streamMyNotifications(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}
	if err := a.store.TouchDeviceNotificationSubscription(r.Context(), accountID, deviceID); err != nil {
		if errors.Is(err, store.ErrNotificationSubscription) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "notification subscription is required"})
			return
		}
		writeInternalAPIError(w, "notification_subscription_touch_failed", err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "notification_stream_unsupported", "notification stream is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	wake, unsubscribe := a.notificationHub.subscribe(deviceID)
	defer unsubscribe()
	ticker := time.NewTicker(notificationStreamHeartbeat)
	defer ticker.Stop()
	sent := make(map[string]struct{})

	for {
		deliveries, err := a.store.ListPendingNotificationDeliveries(
			r.Context(), accountID, deviceID, notificationStreamBatchSize,
		)
		if err != nil {
			return
		}
		for _, delivery := range deliveries {
			if _, alreadySent := sent[delivery.ID]; alreadySent {
				continue
			}
			payload, err := json.Marshal(map[string]string{
				"delivery_id": delivery.ID,
				"envelope":    delivery.EnvelopeCiphertext,
			})
			if err != nil {
				return
			}
			if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
				return
			}
			sent[delivery.ID] = struct{}{}
		}
		flusher.Flush()

		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (a *API) ackMyNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}
	deliveryID := strings.TrimSpace(r.PathValue("id"))
	if _, err := uuid.Parse(deliveryID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid notification delivery"})
		return
	}
	if err := a.store.MarkNotificationDeliveryDelivered(r.Context(), accountID, deviceID, deliveryID); err != nil {
		if errors.Is(err, store.ErrNotificationDelivery) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "notification delivery not found"})
			return
		}
		writeInternalAPIError(w, "notification_delivery_ack_failed", err)
		return
	}
	a.notificationHub.notify(deviceID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) registerRuntimeNotificationAgent(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}
	var req struct {
		TokenSHA256 string `json:"token_sha256"`
		VersionCode int64  `json:"version_code"`
	}
	if err := decodeStrictJSON(r.Body, maxRuntimeNotificationBodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid notification agent registration"})
		return
	}
	digest, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(req.TokenSHA256))
	if err != nil || len(digest) != sha256.Size || req.VersionCode <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid notification agent registration"})
		return
	}
	if err := a.store.RegisterRuntimeNotificationAgent(r.Context(), r.PathValue("id"), node.id, digest, req.VersionCode); err != nil {
		if errors.Is(err, store.ErrRuntimeNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "notification_agent_registration_failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (a *API) receiveRuntimeNotification(w http.ResponseWriter, r *http.Request) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "notification agent authorization is required"})
		return
	}
	token, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")))
	if err != nil || len(token) != 32 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "notification agent authorization is invalid"})
		return
	}
	tokenDigest := sha256.Sum256(token)
	target, err := a.store.AuthenticateRuntimeNotificationAgent(r.Context(), r.PathValue("id"), tokenDigest[:])
	if err != nil {
		if errors.Is(err, store.ErrNotificationAgent) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "notification_agent_auth_failed", err)
		return
	}
	if ok, retryAfter := a.notificationLimiter.allow(target.RuntimeID); !ok {
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		}
		writeAPIError(w, http.StatusTooManyRequests, "runtime_notification_rate_limited", "runtime notification rate limit exceeded")
		return
	}

	var event runtimeNotificationEvent
	if err := decodeStrictJSON(r.Body, maxRuntimeNotificationBodyBytes, &event); err != nil || !validRuntimeNotification(event) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid metadata-only notification event"})
		return
	}
	payload, err := encodeRuntimeNotificationPayload(event)
	if err != nil {
		writeInternalAPIError(w, "notification_payload_encode_failed", err)
		return
	}
	subscriptions, err := a.store.ListDeviceNotificationSubscriptions(r.Context(), target.AccountID)
	if err != nil {
		writeInternalAPIError(w, "notification_subscription_list_failed", err)
		return
	}
	queued := 0
	expiresAt := time.Now().UTC().Add(notificationDeliveryTTL)
	for _, subscription := range subscriptions {
		envelope, err := push.EncryptEnvelope(subscription.EncryptionPublicKey, payload)
		if err != nil {
			writeInternalAPIError(w, "notification_payload_encrypt_failed", err)
			return
		}
		inserted, err := a.store.EnqueueRuntimeNotificationDelivery(r.Context(), store.RuntimeNotificationDelivery{
			ID:                 uuid.NewString(),
			RuntimeID:          target.RuntimeID,
			EventID:            event.EventID,
			DeviceID:           subscription.DeviceID,
			EnvelopeCiphertext: envelope,
			ExpiresAt:          expiresAt,
		})
		if err != nil {
			writeInternalAPIError(w, "notification_delivery_enqueue_failed", err)
			return
		}
		if inserted {
			queued++
		}
		a.notificationHub.notify(subscription.DeviceID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":          true,
		"queued":      queued,
		"subscribers": len(subscriptions),
	})
}

func encodeRuntimeNotificationPayload(event runtimeNotificationEvent) ([]byte, error) {
	return json.Marshal(map[string]any{
		"version":      1,
		"event_id":     event.EventID,
		"package_name": event.PackageName,
		"app_label":    event.AppLabel,
		"posted_at":    event.PostedAt.UTC().Format(time.RFC3339Nano),
		"title":        event.Title,
	})
}

func (a *API) enqueueNodeSecurityNotice(
	ctx context.Context,
	nodeID, source, rule, priority, fingerprint string,
	observedAt time.Time,
) (int, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	if source != "falco" && source != "suricata" {
		return 0, nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	if observedAt.Before(now.Add(-notificationDeliveryTTL)) || observedAt.After(now.Add(5*time.Minute)) {
		observedAt = now
	}
	event := securityNoticeEvent{
		EventID: uuid.NewHash(
			sha256.New(),
			uuid.NameSpaceOID,
			[]byte(strings.TrimSpace(nodeID)+"\x00"+fingerprint),
			5,
		).String(),
		Source:     source,
		Severity:   clientSecuritySeverity(priority),
		Summary:    clientSecuritySummary(source, rule),
		ObservedAt: observedAt.UTC(),
	}
	payload, err := encodeSecurityNoticePayload(event)
	if err != nil {
		return 0, err
	}
	subscriptions, err := a.store.ListDeviceNotificationSubscriptionsForNode(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	queued := 0
	expiresAt := time.Now().UTC().Add(notificationDeliveryTTL)
	for _, subscription := range subscriptions {
		envelope, err := push.EncryptEnvelope(subscription.EncryptionPublicKey, payload)
		if err != nil {
			return queued, err
		}
		inserted, err := a.store.EnqueueSecurityNoticeDelivery(ctx, store.SecurityNoticeDelivery{
			ID:                 uuid.NewString(),
			NodeID:             nodeID,
			EventID:            event.EventID,
			DeviceID:           subscription.DeviceID,
			EnvelopeCiphertext: envelope,
			ExpiresAt:          expiresAt,
		})
		if err != nil {
			return queued, err
		}
		if inserted {
			queued++
		}
		a.notificationHub.notify(subscription.DeviceID)
	}
	return queued, nil
}

func encodeSecurityNoticePayload(event securityNoticeEvent) ([]byte, error) {
	return json.Marshal(map[string]any{
		"version":     1,
		"kind":        "security_notice",
		"event_id":    event.EventID,
		"source":      event.Source,
		"severity":    event.Severity,
		"summary":     event.Summary,
		"observed_at": event.ObservedAt.UTC().Format(time.RFC3339Nano),
	})
}

func clientSecuritySeverity(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "emergency", "alert", "critical", "error":
		return "critical"
	case "warning", "warn":
		return "warning"
	default:
		return "notice"
	}
}

func clientSecuritySummary(source, rule string) string {
	rule = strings.ToLower(strings.TrimSpace(rule))
	if source == "suricata" {
		return "Network-security anomaly detected on the Virtroid host"
	}
	switch {
	case strings.Contains(rule, "shell spawned"):
		return "Unexpected shell activity detected on the Virtroid host"
	case strings.Contains(rule, "runtime root access"):
		return "Unexpected access to protected runtime storage detected"
	case strings.Contains(rule, "docker socket"):
		return "Unexpected container-management access detected"
	default:
		return "Host-security anomaly detected on the Virtroid host"
	}
}

func validRuntimeNotification(event runtimeNotificationEvent) bool {
	if _, err := uuid.Parse(strings.TrimSpace(event.EventID)); err != nil {
		return false
	}
	if len(event.PackageName) == 0 || len(event.PackageName) > maxNotificationPackageChars ||
		!notificationPackagePattern.MatchString(event.PackageName) {
		return false
	}
	if !utf8.ValidString(event.AppLabel) || utf8.RuneCountInString(event.AppLabel) == 0 ||
		utf8.RuneCountInString(event.AppLabel) > maxNotificationLabelRunes ||
		!utf8.ValidString(event.Title) || utf8.RuneCountInString(event.Title) > maxNotificationTitleRunes {
		return false
	}
	now := time.Now().UTC()
	return !event.PostedAt.IsZero() && event.PostedAt.After(now.Add(-7*24*time.Hour)) && event.PostedAt.Before(now.Add(5*time.Minute))
}

func decodeStrictJSON(reader io.Reader, maxBytes int64, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.InputOffset() > maxBytes {
		return errors.New("request body too large")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}
