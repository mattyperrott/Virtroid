package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"virtroid/backend/internal/callbackauth"
	"virtroid/backend/internal/config"
	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/observability"
	"virtroid/backend/internal/store"
)

type API struct {
	cfg              config.ServerConfig
	store            *store.Store
	activeBlobKeys   *activeBlobKeyVault
	bootstrapLimiter *bootstrapRateLimiter
	observability    *observability.Service
	httpClient       *http.Client
}

type durableBlobKeyHandoffStore interface {
	ClearRuntimeBlobKeyHandoff(context.Context, string) error
	ClearAccountBlobKeyHandoffs(context.Context, string) error
}

type activeBlobKeyVault struct {
	mu    sync.Mutex
	keys  map[string]activeBlobKeyEntry
	store durableBlobKeyHandoffStore
}

type activeBlobKeyEntry struct {
	accountID       string
	runtimeID       string
	hostID          string
	operation       string
	leaseID         string
	envelope        blobKeyEnvelope
	blobKeyVerifier string
	hasEnvelope     bool
	expiresAt       time.Time
}

type activeBlobKeyLease struct {
	AccountID string
	RuntimeID string
	HostID    string
	Operation string
	LeaseID   string
}

type runtimeActor struct {
	accountID    string
	deviceID     string
	capabilityID string
	capability   bool
}

type activeBlobKeyHandoff struct {
	AccountID       string
	RuntimeID       string
	HostID          string
	Operation       string
	LeaseID         string
	Envelope        blobKeyEnvelope
	BlobKeyVerifier string
}

type blobKeyEnvelope struct {
	Version            int    `json:"version"`
	Algorithm          string `json:"algorithm"`
	LeaseID            string `json:"lease_id"`
	Operation          string `json:"operation"`
	RuntimeID          string `json:"runtime_id"`
	HostID             string `json:"host_id"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	IV                 string `json:"iv"`
	Ciphertext         string `json:"ciphertext"`
}

const activeBlobKeyHandoffTTL = 2 * time.Minute
const bootstrapRateLimitWindow = time.Minute
const defaultBootstrapRateLimitClients = 4096
const defaultBootstrapMaxBodyBytes = 32 * 1024
const viewerPrepareProxyTimeout = 90 * time.Second
const viewerReconnectBitRate = 4_000_000
const blobKeyEnvelopeAlgorithm = "P256_ECDH_HKDF_SHA256_AESGCM_V1"
const maxAPIRequestBodyBytes int64 = 2 << 20
const maxRuntimeFileImportBytes int64 = 32 << 20
const maxRuntimePhotoImportBytes int64 = 16 << 20
const runtimeMediaProxyTimeout = 2 * time.Minute

var errNodeAdvertiseAllowlistUnavailable = errors.New("node advertise address allowlist is required in production")

const (
	maxSecurityEventOutputRunes            = 4096
	maxSecurityEventJSONRunes              = 65536
	maxSecurityEventTags                   = 16
	maxSecurityEventTagRunes               = 64
	maxRuntimeLogSourceRunes               = 64
	maxRuntimeLogLevelRunes                = 32
	maxRuntimeLogMessageRunes              = 4096
	defaultSecurityEventRateLimitPerMinute = 120
	defaultSecurityEventRetention          = 7 * 24 * time.Hour
)

type bootstrapRateLimiter struct {
	mu         sync.Mutex
	max        int
	maxClients int
	window     time.Duration
	clients    map[string]bootstrapRateLimitEntry
}

type bootstrapRateLimitEntry struct {
	count int
	reset time.Time
}

func newBootstrapRateLimiter(max int, window time.Duration) *bootstrapRateLimiter {
	if window <= 0 {
		window = bootstrapRateLimitWindow
	}
	return &bootstrapRateLimiter{
		max:        max,
		maxClients: defaultBootstrapRateLimitClients,
		window:     window,
		clients:    map[string]bootstrapRateLimitEntry{},
	}
}

func (l *bootstrapRateLimiter) allow(client string) (bool, time.Duration) {
	if l == nil || l.max <= 0 {
		return true, 0
	}
	now := time.Now().UTC()
	client = strings.TrimSpace(client)
	if client == "" {
		client = "unknown"
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	for key, candidate := range l.clients {
		if !candidate.reset.After(now) {
			delete(l.clients, key)
		}
	}

	entry, found := l.clients[client]
	if !found && l.maxClients > 0 && len(l.clients) >= l.maxClients {
		var oldestReset time.Time
		for _, candidate := range l.clients {
			if oldestReset.IsZero() || candidate.reset.Before(oldestReset) {
				oldestReset = candidate.reset
			}
		}
		return false, time.Until(oldestReset).Round(time.Second)
	}
	if entry.reset.IsZero() || !entry.reset.After(now) {
		l.clients[client] = bootstrapRateLimitEntry{count: 1, reset: now.Add(l.window)}
		return true, 0
	}
	if entry.count >= l.max {
		return false, entry.reset.Sub(now)
	}
	entry.count++
	l.clients[client] = entry
	return true, 0
}

func newActiveBlobKeyVault(stores ...durableBlobKeyHandoffStore) *activeBlobKeyVault {
	var st durableBlobKeyHandoffStore
	if len(stores) > 0 {
		st = stores[0]
	}
	return &activeBlobKeyVault{keys: map[string]activeBlobKeyEntry{}, store: st}
}

func (v *activeBlobKeyVault) putLease(lease activeBlobKeyLease) time.Time {
	expiresAt := time.Now().UTC().Add(activeBlobKeyHandoffTTL)
	runtimeID := strings.TrimSpace(lease.RuntimeID)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys[runtimeID] = activeBlobKeyEntry{
		accountID: strings.TrimSpace(lease.AccountID),
		runtimeID: runtimeID,
		hostID:    strings.TrimSpace(lease.HostID),
		operation: strings.TrimSpace(lease.Operation),
		leaseID:   strings.TrimSpace(lease.LeaseID),
		expiresAt: expiresAt,
	}
	return expiresAt
}

func (v *activeBlobKeyVault) activate(handoff activeBlobKeyHandoff) (activeBlobKeyEntry, error) {
	now := time.Now().UTC()
	runtimeID := strings.TrimSpace(handoff.RuntimeID)
	hostID := strings.TrimSpace(handoff.HostID)
	operation := strings.TrimSpace(handoff.Operation)
	leaseID := strings.TrimSpace(handoff.LeaseID)

	v.mu.Lock()
	defer v.mu.Unlock()

	entry, ok := v.keys[runtimeID]
	if !ok {
		return activeBlobKeyEntry{}, errors.New("runtime blob-key lease is not available")
	}
	if !entry.expiresAt.After(now) {
		delete(v.keys, runtimeID)
		return activeBlobKeyEntry{}, errors.New("runtime blob-key lease has expired")
	}
	if entry.accountID != strings.TrimSpace(handoff.AccountID) ||
		entry.runtimeID != runtimeID ||
		entry.hostID != hostID ||
		entry.operation != operation ||
		entry.leaseID != leaseID {
		return activeBlobKeyEntry{}, errors.New("runtime blob-key lease does not match request")
	}
	if err := validateBlobKeyEnvelope(handoff.Envelope, entry); err != nil {
		return activeBlobKeyEntry{}, err
	}
	blobKeyVerifier := strings.TrimSpace(handoff.BlobKeyVerifier)
	if blobKeyVerifier == "" {
		return activeBlobKeyEntry{}, errors.New("runtime blob-key verifier is required")
	}

	entry.envelope = handoff.Envelope
	entry.blobKeyVerifier = blobKeyVerifier
	entry.hasEnvelope = true
	v.keys[runtimeID] = entry
	return entry, nil
}

func (v *activeBlobKeyVault) get(runtimeID string, hostID string) (blobKeyEnvelope, string, time.Time, bool) {
	now := time.Now().UTC()
	runtimeID = strings.TrimSpace(runtimeID)
	hostID = strings.TrimSpace(hostID)
	v.mu.Lock()
	defer v.mu.Unlock()
	entry, ok := v.keys[runtimeID]
	if !ok {
		return blobKeyEnvelope{}, "", time.Time{}, false
	}
	if !entry.expiresAt.After(now) {
		delete(v.keys, runtimeID)
		return blobKeyEnvelope{}, "", time.Time{}, false
	}
	if entry.runtimeID != runtimeID ||
		entry.hostID != hostID ||
		entry.accountID == "" ||
		entry.operation == "" ||
		strings.TrimSpace(entry.blobKeyVerifier) == "" ||
		!entry.hasEnvelope {
		return blobKeyEnvelope{}, "", time.Time{}, false
	}
	return entry.envelope, entry.blobKeyVerifier, entry.expiresAt, true
}

func (v *activeBlobKeyVault) cache(entry activeBlobKeyEntry) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys[entry.runtimeID] = entry
}

func (v *activeBlobKeyVault) clear(runtimeID string) {
	v.mu.Lock()
	delete(v.keys, runtimeID)
	v.mu.Unlock()
	if v.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := v.store.ClearRuntimeBlobKeyHandoff(ctx, runtimeID); err != nil {
			log.Printf("clear durable runtime blob-key handoff failed")
		}
	}
}

func (v *activeBlobKeyVault) clearAccount(accountID string) {
	accountID = strings.TrimSpace(accountID)
	v.mu.Lock()
	for runtimeID, entry := range v.keys {
		if entry.accountID == accountID {
			delete(v.keys, runtimeID)
		}
	}
	v.mu.Unlock()
	if v.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := v.store.ClearAccountBlobKeyHandoffs(ctx, accountID); err != nil {
			log.Printf("clear durable account blob-key handoffs failed")
		}
	}
}

func New(cfg config.ServerConfig, st *store.Store) http.Handler {
	if !strings.EqualFold(cfg.AppEnv, "development") {
		// Keep production invite-gated even when a caller constructs ServerConfig
		// directly instead of using config.LoadServer.
		cfg.BootstrapRequireInvite = true
	}
	bootstrapMaxBodyBytes := cfg.BootstrapMaxBodyBytes
	if bootstrapMaxBodyBytes <= 0 {
		bootstrapMaxBodyBytes = defaultBootstrapMaxBodyBytes
	}
	cfg.BootstrapMaxBodyBytes = bootstrapMaxBodyBytes

	activeBlobKeys := newActiveBlobKeyVault()
	if st != nil {
		activeBlobKeys = newActiveBlobKeyVault(st)
	}
	telemetry := observability.New("virtroidd")
	api := &API{
		cfg:              cfg,
		store:            st,
		activeBlobKeys:   activeBlobKeys,
		bootstrapLimiter: newBootstrapRateLimiter(cfg.BootstrapRateLimitPerMinute, bootstrapRateLimitWindow),
		observability:    telemetry,
		httpClient:       &http.Client{Transport: telemetry.Transport(http.DefaultTransport), Timeout: runtimeMediaProxyTimeout},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.healthz)
	mux.HandleFunc("GET /readyz", api.readyz)
	mux.HandleFunc("GET /metrics", api.metrics)
	mux.HandleFunc("GET /api/v1/meta", api.meta)
	mux.HandleFunc("GET /api/v1/hosts", api.hosts)
	mux.HandleFunc("GET /api/v1/apps/catalog", api.listAppCatalog)
	mux.HandleFunc("POST /api/v1/bootstrap", api.bootstrap)

	mux.HandleFunc("GET /api/v1/me/runtimes", api.listMyRuntimes)
	mux.HandleFunc("GET /api/v1/me/runtimes/state", api.listMyRuntimeStates)
	mux.HandleFunc("GET /api/v1/me/entitlement", api.getMyEntitlement)
	mux.HandleFunc("GET /api/v1/me/apps/catalog", api.listMyAppCatalog)
	mux.HandleFunc("PUT /api/v1/me/apps/selections", api.updateMyAppSelections)
	mux.HandleFunc("GET /api/v1/me/storage", api.getMyStorage)
	mux.HandleFunc("PUT /api/v1/me/storage", api.updateMyStorage)
	mux.HandleFunc("DELETE /api/v1/me", api.deleteMyAccount)
	mux.HandleFunc("GET /api/v1/me/devices", api.listMyDevices)
	mux.HandleFunc("DELETE /api/v1/me/devices/{device_id}", api.revokeMyDevice)
	mux.HandleFunc("POST /api/v1/me/identity/register", api.registerMyIdentity)
	mux.HandleFunc("POST /api/v1/me/identity/change-password", api.changeMyIdentityPassword)
	mux.HandleFunc("POST /api/v1/me/runtimes", api.createMyRuntime)
	mux.HandleFunc("GET /api/v1/me/runtimes/{id}", api.getMyRuntime)
	mux.HandleFunc("GET /api/v1/me/runtimes/{id}/state", api.getMyRuntimeState)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/capability", api.registerMyRuntimeCapability)
	mux.HandleFunc("PATCH /api/v1/me/runtimes/{id}", api.updateMyRuntime)
	mux.HandleFunc("DELETE /api/v1/me/runtimes/{id}", api.deleteMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/delete", api.deleteMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/blob-key-lease", api.createRuntimeBlobKeyLease)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/start", api.startMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/stop", api.stopMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/wipe", api.wipeMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/session", api.createMyRuntimeSession)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/files", api.importMyRuntimeFile)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/photos", api.importMyRuntimePhoto)
	mux.HandleFunc("GET /api/v1/me/runtimes/{id}/logs", api.runtimeLogs)
	mux.HandleFunc("GET /api/v1/me/sessions/active", api.listMyActiveSessions)
	mux.HandleFunc("GET /api/v1/me/sessions/{id}", api.getMySession)
	mux.HandleFunc("POST /api/v1/me/sessions/{id}/relay-token", api.issueMySessionRelayToken)
	mux.HandleFunc("POST /api/v1/me/sessions/{id}/heartbeat", api.heartbeatMySession)
	mux.HandleFunc("POST /api/v1/me/sessions/{id}/end", api.endMySession)
	mux.HandleFunc("POST /api/v1/me/sessions/{id}/close", api.closeMySession)

	mux.HandleFunc("POST /api/v1/internal/hosts/heartbeat", api.hostHeartbeat)
	mux.HandleFunc("GET /api/v1/internal/hosts/{id}/assignments", api.hostAssignments)
	mux.HandleFunc("GET /api/v1/internal/sessions/{id}/relay", api.sessionRelayTarget)
	mux.HandleFunc("GET /api/v1/internal/runtimes/{id}/blob-key", api.runtimeBlobKey)
	mux.HandleFunc("POST /api/v1/internal/runtimes/{id}/status", api.runtimeStatusUpdate)
	mux.HandleFunc("POST /api/v1/internal/runtimes/{id}/logs", api.runtimeLogAppend)
	mux.HandleFunc("POST /api/v1/internal/security/events", api.securityEventAppend)

	return telemetry.Middleware(withRecovery(withJSON(mux)))
}

func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	readiness, err := a.store.NodeReadiness(r.Context())
	if err == nil {
		a.observability.SetGauge("virtroid_control_plane_ready", boolFloat(readiness.Ready), nil)
		a.observability.SetGauge("virtroid_nodes_ready", float64(readiness.ReadyNodes), nil)
		a.observability.SetGauge("virtroid_nodes_observed", float64(readiness.ObservedNodes), nil)
		a.observability.SetGauge("virtroid_nodes_stale", float64(readiness.StaleNodes), nil)
	}
	a.observability.Handler().ServeHTTP(w, r)
}

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		writeServerAPIError(w, http.StatusServiceUnavailable, "health_check_failed", "service unavailable", err)
		return
	}

	readiness, err := a.store.NodeReadiness(r.Context())
	if err != nil {
		writeServerAPIError(w, http.StatusServiceUnavailable, "node_readiness_failed", "service unavailable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"role":     "virtroidd",
		"database": map[string]any{"ready": true},
		"nodes":    readiness,
	})
}

func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		writeServerAPIError(w, http.StatusServiceUnavailable, "database_not_ready", "service unavailable", err)
		return
	}
	readiness, err := a.store.NodeReadiness(r.Context())
	if err != nil {
		writeServerAPIError(w, http.StatusServiceUnavailable, "node_readiness_failed", "service unavailable", err)
		return
	}
	status := http.StatusOK
	if !readiness.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"ok":       readiness.Ready,
		"role":     "virtroidd",
		"database": map[string]any{"ready": true},
		"nodes":    readiness,
	})
}

func (a *API) meta(w http.ResponseWriter, r *http.Request) {
	relayURL := strings.TrimSpace(a.cfg.PublicRelayURL)
	if relayURL == "" {
		relayURL = a.cfg.PublicBaseURL
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service":              "virtroidd",
		"environment":          a.cfg.AppEnv,
		"public_base_url":      a.cfg.PublicBaseURL,
		"public_relay_url":     relayURL,
		"relay_websocket_base": websocketBase(relayURL) + "/api/v1/relay",
	})
}

func (a *API) hosts(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.requireNodeRequest(w, r, false); !ok {
		return
	}

	hosts, err := a.store.ListHosts(r.Context())
	if err != nil {
		writeInternalAPIError(w, "hosts_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": hosts})
}

func (a *API) bootstrap(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.BootstrapEnabled {
		writeAPIError(w, http.StatusServiceUnavailable, "bootstrap_disabled", "new account bootstrap is disabled")
		return
	}
	if ok, retryAfter := a.bootstrapLimiter.allow(bootstrapClientKey(r, a.cfg.TrustProxyHeaders)); !ok {
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "bootstrap rate limit exceeded"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.BootstrapMaxBodyBytes)

	var req struct {
		AccountID        string `json:"account_id"`
		DeviceID         string `json:"device_id"`
		DeviceName       string `json:"device_name"`
		PublicKey        string `json:"public_key"`
		InviteToken      string `json:"invite_token"`
		RuntimeName      string `json:"runtime_name"`
		AndroidImage     string `json:"android_image"`
		AndroidVersion   string `json:"android_version"`
		WidthPx          int    `json:"width_px"`
		HeightPx         int    `json:"height_px"`
		DensityDpi       int    `json:"density_dpi"`
		AudioEnabled     *bool  `json:"audio_enabled"`
		CameraMode       string `json:"camera_mode"`
		FileMode         string `json:"file_mode"`
		BlobAutoSnapshot bool   `json:"blob_auto_snapshot"`
		BlobRetainDays   int    `json:"blob_retain_days"`
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "bootstrap request body is too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read bootstrap request body"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}

	bootstrapAudioEnabled := true
	if req.AudioEnabled != nil {
		bootstrapAudioEnabled = *req.AudioEnabled
	}
	defaults := store.CreateRuntimeInput{
		Name:             req.RuntimeName,
		AndroidImage:     req.AndroidImage,
		AndroidVersion:   req.AndroidVersion,
		WidthPx:          req.WidthPx,
		HeightPx:         req.HeightPx,
		DensityDpi:       req.DensityDpi,
		AudioEnabled:     bootstrapAudioEnabled,
		AudioEnabledSet:  req.AudioEnabled != nil,
		CameraMode:       req.CameraMode,
		FileMode:         req.FileMode,
		BlobAutoSnapshot: req.BlobAutoSnapshot,
		BlobRetainDays:   req.BlobRetainDays,
	}
	if !verifyBootstrapProof(r, body, req.AccountID, req.DeviceID, req.PublicKey) {
		writeAPIError(w, http.StatusUnauthorized, "bootstrap_proof_invalid", "valid device signing proof is required")
		return
	}
	var result store.BootstrapResult
	if a.cfg.BootstrapRequireInvite {
		inviteToken := strings.TrimSpace(req.InviteToken)
		if inviteToken == "" {
			writeAPIError(w, http.StatusForbidden, "bootstrap_invite_required", "a valid bootstrap invitation is required")
			return
		}
		if len(inviteToken) > 256 {
			writeAPIError(w, http.StatusForbidden, "bootstrap_invite_invalid", "bootstrap invitation is invalid, expired, or already used")
			return
		}
		inviteTokenSHA256 := sha256.Sum256([]byte(inviteToken))
		result, err = a.store.BootstrapAccountWithInvitation(
			r.Context(),
			req.AccountID,
			req.DeviceID,
			req.DeviceName,
			req.PublicKey,
			inviteTokenSHA256[:],
			defaults,
		)
	} else {
		result, err = a.store.BootstrapAccountWithIdentity(
			r.Context(),
			req.AccountID,
			req.DeviceID,
			req.DeviceName,
			req.PublicKey,
			defaults,
		)
	}
	if err != nil {
		if errors.Is(err, store.ErrBootstrapInviteInvalid) {
			writeAPIError(w, http.StatusForbidden, "bootstrap_invite_invalid", "bootstrap invitation is invalid, expired, or already used")
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

func verifyBootstrapProof(r *http.Request, body []byte, accountID, deviceID, publicKeyMaterial string) bool {
	headerAccountID := strings.TrimSpace(r.Header.Get("X-Virtroid-Account-ID"))
	headerDeviceID := strings.TrimSpace(r.Header.Get("X-Virtroid-Device-ID"))
	timestampRaw := strings.TrimSpace(r.Header.Get("X-Virtroid-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Virtroid-Nonce"))
	bodyHash := strings.TrimSpace(r.Header.Get("X-Virtroid-Body-SHA256"))
	signatureRaw := strings.TrimSpace(r.Header.Get("X-Virtroid-Signature"))
	if headerAccountID == "" || headerDeviceID == "" || timestampRaw == "" || nonce == "" || bodyHash == "" || signatureRaw == "" {
		return false
	}
	if headerAccountID != strings.TrimSpace(accountID) || headerDeviceID != strings.TrimSpace(deviceID) {
		return false
	}
	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		return false
	}
	if skew := time.Since(time.Unix(timestamp, 0)); skew > 5*time.Minute || skew < -5*time.Minute {
		return false
	}
	if len(nonce) < 16 || len(nonce) > 128 {
		return false
	}
	actualBodyHashBytes := sha256.Sum256(body)
	actualBodyHash := base64.RawURLEncoding.EncodeToString(actualBodyHashBytes[:])
	if subtle.ConstantTimeCompare([]byte(bodyHash), []byte(actualBodyHash)) != 1 {
		return false
	}
	publicKey, err := parseECDSAPublicKeyMaterial(publicKeyMaterial)
	if err != nil || publicKey.Curve.Params().Name != "P-256" {
		return false
	}
	signatureDER, err := base64.RawURLEncoding.DecodeString(signatureRaw)
	if err != nil {
		return false
	}
	canonical := signedRequestCanonical(
		r.Method,
		r.URL.RequestURI(),
		headerAccountID,
		headerDeviceID,
		timestampRaw,
		nonce,
		bodyHash,
	)
	digest := sha256.Sum256([]byte(canonical))
	return ecdsa.VerifyASN1(publicKey, digest[:], signatureDER)
}

func bootstrapClientKey(r *http.Request, trustProxyHeaders bool) string {
	remoteHost := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remoteHost); err == nil {
		remoteHost = host
	}
	var forwardedFor string
	if trustProxyHeaders {
		forwardedFor = strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
		if forwardedFor != "" {
			if comma := strings.IndexByte(forwardedFor, ','); comma >= 0 {
				forwardedFor = forwardedFor[:comma]
			}
			forwardedFor = strings.TrimSpace(forwardedFor)
		}
	}
	if remoteHost == "" {
		remoteHost = "unknown"
	}
	if forwardedFor == "" {
		return remoteHost
	}
	return remoteHost + "|" + forwardedFor
}

func validateNodeAdvertiseAddr(raw string, allowed []string, appEnv string) (string, error) {
	address, err := normalizeNodeAdvertiseHost(raw)
	if err != nil {
		return "", err
	}
	if len(allowed) == 0 {
		if strings.EqualFold(strings.TrimSpace(appEnv), "production") {
			return "", errNodeAdvertiseAllowlistUnavailable
		}
		return address, nil
	}
	for _, candidate := range allowed {
		normalized, candidateErr := normalizeNodeAdvertiseHost(candidate)
		if candidateErr == nil && strings.EqualFold(normalized, address) {
			return address, nil
		}
	}
	return "", fmt.Errorf("node advertise address %q is not in the configured allowlist", address)
}

func normalizeNodeAdvertiseHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 253 || strings.ContainsAny(raw, "/\\@?#[] 	\r\n") {
		return "", errors.New("node advertise address must be a host without a scheme, port, path, or credentials")
	}
	if parsedIP := net.ParseIP(raw); parsedIP != nil {
		return parsedIP.String(), nil
	}
	labels := strings.Split(raw, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("node advertise address is not a valid DNS host")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
				(char < '0' || char > '9') && char != '-' {
				return "", errors.New("node advertise address is not a valid DNS host")
			}
		}
	}
	return strings.ToLower(raw), nil
}

func (a *API) listMyRuntimes(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	runtimes, err := a.store.ListRuntimes(r.Context(), accountID)
	if err != nil {
		writeInternalAPIError(w, "runtimes_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": runtimes})
}

func (a *API) listMyRuntimeStates(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	states, err := a.store.ListRuntimeStates(r.Context(), accountID, deviceID)
	if err != nil {
		writeInternalAPIError(w, "runtime_states_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": states})
}

func (a *API) listAppCatalog(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPublicAppCatalog(r.Context(), r.URL.Query().Get("search"))
	if err != nil {
		writeInternalAPIError(w, "public_catalog_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getMyEntitlement(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	entitlement, err := a.store.GetAccountEntitlementSummary(r.Context(), accountID)
	if err != nil {
		writeInternalAPIError(w, "entitlement_lookup_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, entitlement)
}

func (a *API) listMyAppCatalog(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	items, err := a.store.ListAppCatalog(r.Context(), accountID, r.URL.Query().Get("search"))
	if err != nil {
		writeInternalAPIError(w, "account_catalog_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) updateMyAppSelections(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID    string   `json:"account_id"`
		DeviceID     string   `json:"device_id"`
		PackageNames []string `json:"package_names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !a.validateSignedDeviceBody(w, accountID, deviceID, req.AccountID, req.DeviceID) {
		return
	}

	items, err := a.store.ReplaceAccountAppSelections(r.Context(), accountID, req.PackageNames)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getMyStorage(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	storage, err := a.store.GetAccountStorage(r.Context(), accountID)
	if err != nil {
		writeInternalAPIError(w, "account_storage_lookup_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, clientVisibleAccountStorage(storage))
}

func (a *API) updateMyStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"code":  "account_storage_mutation_unavailable",
		"error": "user-managed storage and funding configuration is disabled until payment ownership and storage isolation are authenticated end to end",
	})
}

func clientVisibleAccountStorage(storage store.AccountStorage) store.AccountStorage {
	// Account-facing storage status is informational. Neither a node heartbeat
	// nor a legacy account row is an authenticated payment instruction, so never
	// return a deposit address to a client.
	storage.WalletAddress = nil
	storage.FundingAddress = nil
	if strings.TrimSpace(storage.Provider) == "sia-renterd" {
		storage.FundingModel = "operator-pooled"
	} else {
		storage.FundingModel = "operator"
	}
	return storage
}

func (a *API) registerMyIdentity(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID       string `json:"account_id"`
		DeviceID        string `json:"device_id"`
		BlobKeyVerifier string `json:"blob_key_verifier"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.AccountID) != "" && strings.TrimSpace(req.AccountID) != accountID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed account does not match request body"})
		return
	}
	if strings.TrimSpace(req.DeviceID) != "" && strings.TrimSpace(req.DeviceID) != deviceID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed device does not match request body"})
		return
	}

	if err := a.store.RegisterDeviceBlobKeyVerifier(
		r.Context(),
		accountID,
		deviceID,
		req.BlobKeyVerifier,
	); err != nil {
		switch err {
		case store.ErrDeviceNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrIdentityKeyRequired:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		case store.ErrIdentityAlreadySet:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		default:
			writeInternalAPIError(w, "identity_registration_failed", err)
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) changeMyIdentityPassword(w http.ResponseWriter, r *http.Request) {
	writeAPIError(
		w,
		http.StatusNotImplemented,
		"identity_password_rotation_unavailable",
		"password rotation is unavailable until snapshots can be transactionally rewrapped",
	)
}

func (a *API) deleteMyAccount(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	if err := a.store.DeleteAccount(r.Context(), accountID); err != nil {
		if err == store.ErrAccountNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "account_delete_failed", err)
		return
	}
	a.activeBlobKeys.clearAccount(accountID)

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) listMyDevices(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	devices, err := a.store.ListDevices(r.Context(), accountID)
	if err != nil {
		writeInternalAPIError(w, "devices_list_failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": devices})
}

func (a *API) revokeMyDevice(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	device, stoppedRuntimeIDs, err := a.store.RevokeDevice(r.Context(), accountID, r.PathValue("device_id"))
	if err != nil {
		switch err {
		case store.ErrDeviceNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		case store.ErrLastActiveDevice:
			writeAPIError(w, http.StatusConflict, "last_active_device", err.Error())
			return
		}
		writeInternalAPIError(w, "device_revoke_failed", err)
		return
	}
	a.activeBlobKeys.clearAccount(accountID)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"device":                device,
		"stopped_runtime_ids":   stoppedRuntimeIDs,
		"stopped_runtime_count": len(stoppedRuntimeIDs),
	})
}

func (a *API) getMyRuntime(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	runtime, err := a.store.GetRuntime(r.Context(), accountID, r.PathValue("id"))
	if err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "runtime_lookup_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, runtime)
}

func (a *API) getMyRuntimeState(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	state, err := a.store.GetRuntimeState(r.Context(), accountID, deviceID, r.PathValue("id"))
	if err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "runtime_state_lookup_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, state)
}

func (a *API) registerMyRuntimeCapability(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID    string `json:"account_id"`
		DeviceID     string `json:"device_id"`
		CapabilityID string `json:"capability_id"`
		PublicKey    string `json:"public_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !a.validateSignedDeviceBody(w, accountID, deviceID, req.AccountID, req.DeviceID) {
		return
	}
	publicKeyMaterial := strings.TrimSpace(req.PublicKey)
	if _, err := parseECDSAPublicKeyMaterial(publicKeyMaterial); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "runtime capability public key is invalid"})
		return
	}
	expectedID := runtimeCapabilityID(r.PathValue("id"), publicKeyMaterial)
	if strings.TrimSpace(req.CapabilityID) != expectedID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "runtime capability id does not match public key"})
		return
	}

	capability, err := a.store.UpsertRuntimeCapability(r.Context(), accountID, r.PathValue("id"), expectedID, publicKeyMaterial)
	if err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "runtime_capability_registration_failed", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"capability_id": capability.ID,
		"runtime_id":    capability.RuntimeID,
		"expires_at":    capability.ExpiresAt,
	})
}

func (a *API) createMyRuntime(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID        string `json:"account_id"`
		Name             string `json:"name"`
		AndroidImage     string `json:"android_image"`
		AndroidVersion   string `json:"android_version"`
		WidthPx          int    `json:"width_px"`
		HeightPx         int    `json:"height_px"`
		DensityDpi       int    `json:"density_dpi"`
		AudioEnabled     *bool  `json:"audio_enabled"`
		CameraMode       string `json:"camera_mode"`
		FileMode         string `json:"file_mode"`
		BlobAutoSnapshot bool   `json:"blob_auto_snapshot"`
		BlobRetainDays   int    `json:"blob_retain_days"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.AccountID) != "" && strings.TrimSpace(req.AccountID) != accountID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed account does not match request body"})
		return
	}

	audioEnabled := true
	if req.AudioEnabled != nil {
		audioEnabled = *req.AudioEnabled
	}
	runtime, err := a.store.CreateRuntime(r.Context(), accountID, store.CreateRuntimeInput{
		Name:             req.Name,
		AndroidImage:     req.AndroidImage,
		AndroidVersion:   req.AndroidVersion,
		WidthPx:          req.WidthPx,
		HeightPx:         req.HeightPx,
		DensityDpi:       req.DensityDpi,
		AudioEnabled:     audioEnabled,
		AudioEnabledSet:  req.AudioEnabled != nil,
		CameraMode:       req.CameraMode,
		FileMode:         req.FileMode,
		BlobAutoSnapshot: req.BlobAutoSnapshot,
		BlobRetainDays:   req.BlobRetainDays,
	})
	if err != nil {
		writeRuntimeMutationError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, runtime)
}

func (a *API) updateMyRuntime(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID        string  `json:"account_id"`
		Name             *string `json:"name"`
		AndroidImage     *string `json:"android_image"`
		AndroidVersion   *string `json:"android_version"`
		WidthPx          *int    `json:"width_px"`
		HeightPx         *int    `json:"height_px"`
		DensityDpi       *int    `json:"density_dpi"`
		AudioEnabled     *bool   `json:"audio_enabled"`
		CameraMode       *string `json:"camera_mode"`
		FileMode         *string `json:"file_mode"`
		BlobAutoSnapshot *bool   `json:"blob_auto_snapshot"`
		BlobRetainDays   *int    `json:"blob_retain_days"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.AccountID) != "" && strings.TrimSpace(req.AccountID) != accountID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed account does not match request body"})
		return
	}

	runtime, err := a.store.UpdateRuntimeSettings(r.Context(), accountID, r.PathValue("id"), store.UpdateRuntimeInput{
		Name:             req.Name,
		AndroidImage:     req.AndroidImage,
		AndroidVersion:   req.AndroidVersion,
		WidthPx:          req.WidthPx,
		HeightPx:         req.HeightPx,
		DensityDpi:       req.DensityDpi,
		AudioEnabled:     req.AudioEnabled,
		CameraMode:       req.CameraMode,
		FileMode:         req.FileMode,
		BlobAutoSnapshot: req.BlobAutoSnapshot,
		BlobRetainDays:   req.BlobRetainDays,
	})
	if err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeRuntimeMutationError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, runtime)
}

func (a *API) deleteMyRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := r.PathValue("id")
	actor, ok := a.requireRuntimeActor(w, r, runtimeID)
	if !ok {
		return
	}

	var req struct {
		AccountID       string          `json:"account_id"`
		DeviceID        string          `json:"device_id"`
		BlobKeyVerifier string          `json:"blob_key_verifier"`
		BlobKeyEnvelope blobKeyEnvelope `json:"blob_key_envelope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "runtime deletion requires a blob-key envelope"})
		return
	}
	if !actor.capability && !a.validateSignedDeviceBody(w, actor.accountID, actor.deviceID, req.AccountID, req.DeviceID) {
		return
	}

	lease, ok := a.activateRuntimeBlobKeyEnvelope(w, r, actor, runtimeID, "delete", req.BlobKeyVerifier, req.BlobKeyEnvelope)
	if !ok {
		return
	}
	if !a.verifyActiveBlobKeyEnvelope(w, r, lease) {
		return
	}

	runtime, err := a.store.DeleteRuntimeOnHost(r.Context(), actor.accountID, runtimeID, lease.hostID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		writeRuntimeMutationError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) createRuntimeBlobKeyLease(w http.ResponseWriter, r *http.Request) {
	runtimeID := r.PathValue("id")
	actor, ok := a.requireRuntimeActor(w, r, runtimeID)
	if !ok {
		return
	}

	var req struct {
		AccountID       string `json:"account_id"`
		DeviceID        string `json:"device_id"`
		Operation       string `json:"operation"`
		BlobKeyVerifier string `json:"blob_key_verifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !actor.capability && !a.validateSignedDeviceBody(w, actor.accountID, actor.deviceID, req.AccountID, req.DeviceID) {
		return
	}

	operation := normalizeBlobKeyOperation(req.Operation)
	if operation == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported blob-key lease operation"})
		return
	}
	if _, err := a.verifiedActorBlobKeyVerifier(r.Context(), actor, req.BlobKeyVerifier); err != nil {
		writeIdentityAuthError(w, err)
		return
	}

	runtime, host, err := a.store.RuntimeBlobKeyTarget(r.Context(), actor.accountID, runtimeID, operation)
	if err != nil {
		writeRuntimeMutationError(w, err)
		return
	}
	if strings.TrimSpace(host.PublicKey) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "runtime node key is not available"})
		return
	}

	leaseID, err := randomLeaseID()
	if err != nil {
		writeInternalAPIError(w, "blob_key_lease_failed", err)
		return
	}
	lease := activeBlobKeyLease{
		AccountID: actor.accountID,
		RuntimeID: runtime.ID,
		HostID:    host.ID,
		Operation: operation,
		LeaseID:   leaseID,
	}
	expiresAt := a.activeBlobKeys.putLease(lease)
	if a.store != nil {
		if err := a.store.PutRuntimeBlobKeyLease(r.Context(), store.RuntimeBlobKeyHandoff{
			AccountID: actor.accountID,
			RuntimeID: runtime.ID,
			HostID:    host.ID,
			Operation: operation,
			LeaseID:   leaseID,
			ExpiresAt: expiresAt,
		}); err != nil {
			a.activeBlobKeys.clear(runtime.ID)
			writeInternalAPIError(w, "blob_key_lease_persist_failed", err)
			return
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"lease_id":        leaseID,
		"runtime_id":      runtime.ID,
		"host_id":         host.ID,
		"operation":       operation,
		"algorithm":       blobKeyEnvelopeAlgorithm,
		"node_public_key": host.PublicKey,
		"expires_at":      expiresAt,
		"runtime":         runtime,
	})
}

func (a *API) startMyRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := r.PathValue("id")
	actor, ok := a.requireRuntimeActor(w, r, runtimeID)
	if !ok {
		return
	}

	var req struct {
		AccountID       string          `json:"account_id"`
		DeviceID        string          `json:"device_id"`
		BlobKeyVerifier string          `json:"blob_key_verifier"`
		BlobKeyEnvelope blobKeyEnvelope `json:"blob_key_envelope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !actor.capability && !a.validateSignedDeviceBody(w, actor.accountID, actor.deviceID, req.AccountID, req.DeviceID) {
		return
	}

	lease, ok := a.activateRuntimeBlobKeyEnvelope(w, r, actor, runtimeID, "start", req.BlobKeyVerifier, req.BlobKeyEnvelope)
	if !ok {
		return
	}
	if !a.verifyActiveBlobKeyEnvelope(w, r, lease) {
		return
	}

	runtime, err := a.store.StartRuntimeOnHost(r.Context(), actor.accountID, runtimeID, lease.hostID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		writeRuntimeMutationError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) stopMyRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := r.PathValue("id")
	actor, ok := a.requireRuntimeActor(w, r, runtimeID)
	if !ok {
		return
	}

	var req struct {
		AccountID       string          `json:"account_id"`
		DeviceID        string          `json:"device_id"`
		BlobKeyVerifier string          `json:"blob_key_verifier"`
		BlobKeyEnvelope blobKeyEnvelope `json:"blob_key_envelope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !actor.capability && !a.validateSignedDeviceBody(w, actor.accountID, actor.deviceID, req.AccountID, req.DeviceID) {
		return
	}

	lease, ok := a.activateRuntimeBlobKeyEnvelope(w, r, actor, runtimeID, "stop", req.BlobKeyVerifier, req.BlobKeyEnvelope)
	if !ok {
		return
	}
	if !a.verifyActiveBlobKeyEnvelope(w, r, lease) {
		return
	}

	runtime, err := a.store.StopRuntime(r.Context(), actor.accountID, runtimeID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		switch err {
		case store.ErrRuntimeNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		default:
			writeInternalAPIError(w, "runtime_stop_failed", err)
		}
		return
	}
	if strings.EqualFold(strings.TrimSpace(runtime.Status), "stopped") &&
		strings.EqualFold(strings.TrimSpace(runtime.DesiredState), "stopped") {
		// A fully stopped runtime has no node work left that can consume the
		// duplicate request's short-lived envelope.
		a.activeBlobKeys.clear(runtimeID)
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) wipeMyRuntime(w http.ResponseWriter, r *http.Request) {
	runtimeID := r.PathValue("id")
	actor, ok := a.requireRuntimeActor(w, r, runtimeID)
	if !ok {
		return
	}

	var req struct {
		AccountID       string          `json:"account_id"`
		DeviceID        string          `json:"device_id"`
		BlobKeyVerifier string          `json:"blob_key_verifier"`
		BlobKeyEnvelope blobKeyEnvelope `json:"blob_key_envelope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !actor.capability && !a.validateSignedDeviceBody(w, actor.accountID, actor.deviceID, req.AccountID, req.DeviceID) {
		return
	}

	lease, ok := a.activateRuntimeBlobKeyEnvelope(w, r, actor, runtimeID, "wipe", req.BlobKeyVerifier, req.BlobKeyEnvelope)
	if !ok {
		return
	}
	if !a.verifyActiveBlobKeyEnvelope(w, r, lease) {
		return
	}

	runtime, err := a.store.WipeRuntimeOnHost(r.Context(), actor.accountID, runtimeID, lease.hostID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		writeRuntimeMutationError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) runtimeLogs(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	logs, err := a.store.ListRuntimeLogs(r.Context(), accountID, r.PathValue("id"), limit)
	if err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "runtime_logs_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": logs})
}

func (a *API) createMyRuntimeSession(w http.ResponseWriter, r *http.Request) {
	runtimeID := r.PathValue("id")
	actor, ok := a.requireRuntimeActor(w, r, runtimeID)
	if !ok {
		return
	}

	var req struct {
		AccountID       string          `json:"account_id"`
		DeviceID        string          `json:"device_id"`
		MaxSize         int             `json:"max_size"`
		BitRate         int             `json:"bit_rate"`
		BlobKeyVerifier string          `json:"blob_key_verifier"`
		BlobKeyEnvelope blobKeyEnvelope `json:"blob_key_envelope"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !actor.capability && !a.validateSignedDeviceBody(w, actor.accountID, actor.deviceID, req.AccountID, req.DeviceID) {
		return
	}

	relayEndpoint, err := a.publicRelayEndpoint()
	if err != nil {
		writeInternalAPIError(w, "public_relay_configuration_failed", err)
		return
	}

	// Reserve the one-live-session invariant before activating a key handoff or
	// rotating the guest viewer key. A competing actor must fail with zero node
	// side effects.
	var session store.Session
	if actor.capability {
		session, err = a.store.CreateSessionWithCapability(r.Context(), actor.accountID, actor.capabilityID, runtimeID)
	} else {
		session, err = a.store.CreateSession(r.Context(), actor.deviceID, runtimeID)
	}
	if err != nil {
		if errors.Is(err, store.ErrSessionAlreadyActive) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		switch err {
		case store.ErrRuntimeNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrRuntimeNotReady:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		case store.ErrRuntimeEntitlement:
			writeAPIError(w, http.StatusPaymentRequired, store.RuntimeEntitlementRequiredCode, err.Error())
		case store.ErrRuntimeTrialTimeQuota:
			writeAPIError(w, http.StatusPaymentRequired, store.RuntimeTrialTimeExceededCode, err.Error())
		default:
			writeInternalAPIError(w, "session_create_failed", err)
		}
		return
	}
	abortPendingSession := func(reason string) {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if _, abortErr := a.store.AbortPendingSession(abortCtx, session.ID, session.RelayToken, reason); abortErr != nil {
			log.Printf("abort pending session after setup failure failed")
		}
	}

	lease, ok := a.activateRuntimeBlobKeyEnvelope(w, r, actor, runtimeID, "session", req.BlobKeyVerifier, req.BlobKeyEnvelope)
	if !ok {
		abortPendingSession("blob-key handoff activation failed")
		return
	}
	if !a.verifyActiveBlobKeyEnvelope(w, r, lease) {
		abortPendingSession("blob-key handoff verification failed")
		return
	}

	runtime, err := a.store.GetRuntime(r.Context(), actor.accountID, runtimeID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		abortPendingSession("runtime lookup failed")
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "session_runtime_lookup_failed", err)
		return
	}
	if runtime.Status != "running" ||
		runtime.DesiredState != "running" ||
		runtime.ConnectionStatus != "online" ||
		runtime.HostID == nil ||
		runtime.ViewerPort == nil {
		a.activeBlobKeys.clear(runtimeID)
		abortPendingSession("runtime became unavailable during session setup")
		writeJSON(w, http.StatusConflict, map[string]any{"error": store.ErrRuntimeNotReady.Error()})
		return
	}
	if runtime.HostID == nil || strings.TrimSpace(*runtime.HostID) != lease.hostID {
		a.activeBlobKeys.clear(runtimeID)
		abortPendingSession("runtime assignment changed during session setup")
		writeJSON(w, http.StatusConflict, map[string]any{"error": "runtime blob-key lease host does not match assignment"})
		return
	}

	host, err := a.store.GetHost(r.Context(), *runtime.HostID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		abortPendingSession("runtime host lookup failed")
		writeInternalAPIError(w, "session_host_lookup_failed", err)
		return
	}

	maxSize := req.MaxSize
	if maxSize <= 0 {
		maxSize = max(runtime.WidthPx, runtime.HeightPx)
	}
	bitRate := req.BitRate
	if bitRate <= 0 {
		bitRate = 8_000_000
	}

	viewerPublicKey, err := a.prepareViewer(r.Context(), host.AdvertiseAddr, host.RelayPort, runtime.ID, maxSize, bitRate)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		abortPendingSession("viewer preparation failed")
		writeServerAPIError(w, http.StatusBadGateway, "viewer_prepare_failed", "upstream service error", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"session":           session,
		"viewer_host":       relayEndpoint.Host,
		"viewer_port":       relayEndpoint.Port,
		"viewer_address":    relayEndpoint.Address,
		"relay_host":        relayEndpoint.Host,
		"relay_port":        relayEndpoint.Port,
		"relay_scheme":      relayEndpoint.Scheme,
		"relay_tls":         relayEndpoint.TLS,
		"relay_path":        "/api/v1/relay/" + session.ID,
		"viewer_public_key": viewerPublicKey,
		"audio_enabled":     runtime.AudioEnabled,
		"camera_mode":       runtime.CameraMode,
		"file_mode":         runtime.FileMode,
	})
}

func (a *API) importMyRuntimeFile(w http.ResponseWriter, r *http.Request) {
	runtimeID := strings.TrimSpace(r.PathValue("id"))
	actor, ok := a.requireRuntimeCapabilityRequest(w, r, runtimeID)
	if !ok {
		return
	}
	filename, err := decodeRuntimeMediaName(r.URL.Query().Get("name"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_file_name", err.Error())
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "file_too_large", "imported file exceeds 32 MiB")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "file_read_failed", "could not read imported file")
		return
	}
	if len(body) == 0 {
		writeAPIError(w, http.StatusBadRequest, "empty_file", "imported file is empty")
		return
	}
	if int64(len(body)) > maxRuntimeFileImportBytes {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "file_too_large", "imported file exceeds 32 MiB")
		return
	}

	_, host, ok := a.runtimeMediaTarget(w, r, actor, runtimeID, "file")
	if !ok {
		return
	}
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	callbackPath := "/api/v1/internal/runtimes/" + url.PathEscape(runtimeID) + "/files?name=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString([]byte(filename)))
	a.proxyRuntimeMedia(w, r, host, callbackPath, body, map[string]string{"Content-Type": contentType})
}

func (a *API) importMyRuntimePhoto(w http.ResponseWriter, r *http.Request) {
	runtimeID := strings.TrimSpace(r.PathValue("id"))
	actor, ok := a.requireRuntimeCapabilityRequest(w, r, runtimeID)
	if !ok {
		return
	}
	filename, err := decodeRuntimeMediaName(r.URL.Query().Get("name"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_photo_name", err.Error())
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "photo_too_large", "photo must be a JPEG no larger than 16 MiB")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "photo_read_failed", "could not read captured photo")
		return
	}
	if err := validateRuntimePhoto(filename, body); err != nil {
		writeAPIError(w, http.StatusUnsupportedMediaType, "photo_invalid", err.Error())
		return
	}

	_, host, ok := a.runtimeMediaTarget(w, r, actor, runtimeID, "photo")
	if !ok {
		return
	}
	callbackPath := "/api/v1/internal/runtimes/" + url.PathEscape(runtimeID) + "/photos?name=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString([]byte(filename)))
	a.proxyRuntimeMedia(w, r, host, callbackPath, body, map[string]string{"Content-Type": "image/jpeg"})
}

func validateRuntimePhoto(filename string, body []byte) error {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if extension != ".jpg" && extension != ".jpeg" {
		return errors.New("captured photo name must end in .jpg or .jpeg")
	}
	if len(body) < 4 || int64(len(body)) > maxRuntimePhotoImportBytes || body[0] != 0xff || body[1] != 0xd8 {
		return errors.New("photo must be a JPEG no larger than 16 MiB")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 64_000_000 {
		return errors.New("photo contains invalid or excessive JPEG dimensions")
	}
	return nil
}

func (a *API) runtimeMediaTarget(w http.ResponseWriter, r *http.Request, actor runtimeActor, runtimeID, operation string) (store.Runtime, store.Host, bool) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" || !actor.capability {
		writeAPIError(w, http.StatusConflict, "active_session_required", "an active viewer session is required for runtime media")
		return store.Runtime{}, store.Host{}, false
	}
	if err := a.store.RequireSessionRuntimeWithCapability(r.Context(), actor.accountID, actor.capabilityID, sessionID, runtimeID); err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			writeAPIError(w, http.StatusConflict, "active_session_required", "an active viewer session is required for runtime media")
		} else {
			writeInternalAPIError(w, "runtime_media_session_lookup_failed", err)
		}
		return store.Runtime{}, store.Host{}, false
	}
	runtime, err := a.store.GetRuntime(r.Context(), actor.accountID, runtimeID)
	if err != nil {
		if errors.Is(err, store.ErrRuntimeNotFound) {
			writeAPIError(w, http.StatusNotFound, "runtime_not_found", err.Error())
		} else {
			writeInternalAPIError(w, "runtime_media_lookup_failed", err)
		}
		return store.Runtime{}, store.Host{}, false
	}
	if runtime.Status != "running" || runtime.DesiredState != "running" || runtime.ConnectionStatus != "online" || runtime.HostID == nil {
		writeAPIError(w, http.StatusConflict, "runtime_not_ready", "runtime must be online for active media operations")
		return store.Runtime{}, store.Host{}, false
	}
	switch operation {
	case "file":
		if !strings.EqualFold(runtime.FileMode, "upload-only") && !strings.EqualFold(runtime.FileMode, "bidirectional") {
			writeAPIError(w, http.StatusConflict, "file_import_disabled", "file import is disabled for this runtime")
			return store.Runtime{}, store.Host{}, false
		}
	case "photo":
		if !strings.EqualFold(runtime.CameraMode, "photo-import") {
			writeAPIError(w, http.StatusConflict, "camera_photo_import_disabled", "camera photo import is disabled for this runtime")
			return store.Runtime{}, store.Host{}, false
		}
	}
	host, err := a.store.GetHost(r.Context(), strings.TrimSpace(*runtime.HostID))
	if err != nil {
		writeInternalAPIError(w, "runtime_media_host_lookup_failed", err)
		return store.Runtime{}, store.Host{}, false
	}
	if (operation == "file" || operation == "photo") && !host.FileImport {
		writeAPIError(w, http.StatusServiceUnavailable, "node_file_import_unavailable", "assigned node does not support file import")
		return store.Runtime{}, store.Host{}, false
	}
	return runtime, host, true
}

func (a *API) proxyRuntimeMedia(w http.ResponseWriter, r *http.Request, host store.Host, callbackPath string, body []byte, headers map[string]string) {
	advertiseAddr, err := validateNodeAdvertiseAddr(host.AdvertiseAddr, a.cfg.NodeAllowedAdvertiseAddrs, a.cfg.AppEnv)
	if err != nil {
		writeServerAPIError(w, http.StatusBadGateway, "node_address_rejected", "assigned node address is unavailable", err)
		return
	}
	port := host.RelayPort
	if port <= 0 {
		port = 8090
	}
	proxyCtx, cancel := context.WithTimeout(r.Context(), runtimeMediaProxyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(proxyCtx, http.MethodPost, "http://"+net.JoinHostPort(advertiseAddr, strconv.Itoa(port))+callbackPath, bytes.NewReader(body))
	if err != nil {
		writeInternalAPIError(w, "runtime_media_proxy_request_failed", err)
		return
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	if err := a.signNodeCallback(req, body); err != nil {
		writeInternalAPIError(w, "runtime_media_proxy_sign_failed", err)
		return
	}
	resp, err := a.outboundClient().Do(req)
	if err != nil {
		writeServerAPIError(w, http.StatusBadGateway, "runtime_media_proxy_failed", "assigned node did not accept media", err)
		return
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxAPIRequestBodyBytes))
	if readErr != nil {
		writeServerAPIError(w, http.StatusBadGateway, "runtime_media_proxy_response_failed", "assigned node returned an invalid response", readErr)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

func decodeRuntimeMediaName(encoded string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(decoded) == 0 || len(decoded) > 255 || !utf8.Valid(decoded) {
		return "", errors.New("file name must be base64url encoded UTF-8 and no more than 255 bytes")
	}
	name := strings.TrimSpace(string(decoded))
	if name == "" || strings.ContainsRune(name, 0) || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return "", errors.New("file name is invalid")
	}
	return name, nil
}

func (a *API) endMySession(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.requireSessionActor(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID       string          `json:"account_id"`
		DeviceID        string          `json:"device_id"`
		BlobKeyVerifier string          `json:"blob_key_verifier"`
		BlobKeyEnvelope blobKeyEnvelope `json:"blob_key_envelope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if !actor.capability && !a.validateSignedDeviceBody(w, actor.accountID, actor.deviceID, req.AccountID, req.DeviceID) {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	runtimeID := strings.TrimSpace(req.BlobKeyEnvelope.RuntimeID)
	if sessionID == "" || runtimeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session id and blob-key envelope runtime id are required"})
		return
	}
	if actor.capability && strings.TrimSpace(r.URL.Query().Get("runtime_id")) != runtimeID {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "capability runtime does not match blob-key envelope runtime"})
		return
	}

	var bindingErr error
	if actor.capability {
		bindingErr = a.store.RequireSessionRuntimeWithCapability(r.Context(), actor.accountID, actor.capabilityID, sessionID, runtimeID)
	} else {
		bindingErr = a.store.RequireSessionRuntime(r.Context(), actor.accountID, actor.deviceID, sessionID, runtimeID)
	}
	if bindingErr != nil {
		if errors.Is(bindingErr, store.ErrSessionNotFound) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "session does not match blob-key envelope runtime"})
			return
		}
		writeInternalAPIError(w, "session_binding_failed", bindingErr)
		return
	}

	lease, ok := a.activateRuntimeBlobKeyEnvelope(w, r, actor, runtimeID, "stop", req.BlobKeyVerifier, req.BlobKeyEnvelope)
	if !ok {
		return
	}
	if !a.verifyActiveBlobKeyEnvelope(w, r, lease) {
		return
	}

	var runtime store.Runtime
	var err error
	if actor.capability {
		runtime, err = a.store.EndSessionAndStopRuntimeWithCapability(r.Context(), actor.accountID, actor.capabilityID, sessionID, runtimeID)
	} else {
		runtime, err = a.store.EndSessionAndStopRuntime(r.Context(), actor.accountID, actor.deviceID, sessionID, runtimeID)
	}
	if err != nil {
		a.activeBlobKeys.clear(lease.runtimeID)
		switch err {
		case store.ErrSessionNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrRuntimeNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		default:
			writeInternalAPIError(w, "session_end_failed", err)
		}
		return
	}
	if runtime.ID != lease.runtimeID {
		a.activeBlobKeys.clear(lease.runtimeID)
		writeJSON(w, http.StatusConflict, map[string]any{"error": "runtime blob-key lease does not match session runtime"})
		return
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) closeMySession(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.requireSessionActor(w, r)
	if !ok {
		return
	}

	var err error
	if actor.capability {
		err = a.store.CloseSessionWithCapability(r.Context(), actor.accountID, actor.capabilityID, r.PathValue("id"))
	} else {
		err = a.store.CloseSession(r.Context(), actor.accountID, actor.deviceID, r.PathValue("id"))
	}
	if err != nil {
		if err == store.ErrSessionNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "session_close_failed", err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) listMyActiveSessions(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	sessions, capabilities, err := a.store.ListActiveSessionCapabilities(r.Context(), accountID)
	if err != nil {
		writeInternalAPIError(w, "active_sessions_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":     sessions,
		"capabilities": capabilities,
	})
}

func (a *API) getMySession(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.requireSessionActor(w, r)
	if !ok {
		return
	}

	var state store.SessionState
	var err error
	if actor.capability {
		state, err = a.store.GetSessionStateWithCapability(r.Context(), actor.accountID, actor.capabilityID, r.PathValue("id"))
	} else {
		state, err = a.store.GetSessionState(r.Context(), actor.accountID, actor.deviceID, r.PathValue("id"))
	}
	if err != nil {
		if err == store.ErrSessionNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "session_state_lookup_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, state)
}

func (a *API) issueMySessionRelayToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.requireSessionActor(w, r)
	if !ok {
		return
	}

	var state store.SessionState
	var err error
	if actor.capability {
		state, err = a.store.GetSessionStateWithCapability(r.Context(), actor.accountID, actor.capabilityID, r.PathValue("id"))
	} else {
		state, err = a.store.GetSessionState(r.Context(), actor.accountID, actor.deviceID, r.PathValue("id"))
	}
	if err != nil {
		if err == store.ErrSessionNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "session_state_lookup_failed", err)
		return
	}
	if !state.CanResume || state.Runtime.HostID == nil || state.Runtime.ViewerPort == nil {
		writeAPIError(w, http.StatusConflict, "session_not_resumable", "session is not resumable")
		return
	}

	host, err := a.store.GetHost(r.Context(), *state.Runtime.HostID)
	if err != nil {
		writeInternalAPIError(w, "session_host_lookup_failed", err)
		return
	}
	viewerPublicKey, err := a.prepareViewer(
		r.Context(),
		host.AdvertiseAddr,
		host.RelayPort,
		state.Runtime.ID,
		max(state.Runtime.WidthPx, state.Runtime.HeightPx),
		viewerReconnectBitRate,
	)
	if err != nil {
		writeServerAPIError(w, http.StatusBadGateway, "viewer_reprepare_failed", "upstream service error", err)
		return
	}

	var session store.Session
	if actor.capability {
		session, err = a.store.IssueSessionRelayTokenWithCapability(r.Context(), actor.accountID, actor.capabilityID, r.PathValue("id"))
	} else {
		session, err = a.store.IssueSessionRelayToken(r.Context(), actor.accountID, actor.deviceID, r.PathValue("id"))
	}
	if err != nil {
		if err == store.ErrSessionNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "session_relay_token_issue_failed", err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"session":           session,
		"viewer_public_key": viewerPublicKey,
	})
}

func (a *API) heartbeatMySession(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.requireSessionActor(w, r)
	if !ok {
		return
	}

	var session store.Session
	var err error
	if actor.capability {
		session, err = a.store.HeartbeatSessionWithCapability(r.Context(), actor.accountID, actor.capabilityID, r.PathValue("id"))
	} else {
		session, err = a.store.HeartbeatSession(r.Context(), actor.accountID, actor.deviceID, r.PathValue("id"))
	}
	if err != nil {
		if err == store.ErrSessionNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "session_heartbeat_failed", err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"session": session,
	})
}

type publicRelayEndpoint struct {
	Host    string
	Port    int
	Scheme  string
	TLS     bool
	Address string
}

func (a *API) publicRelayEndpoint() (publicRelayEndpoint, error) {
	raw := strings.TrimSpace(a.cfg.PublicRelayURL)
	if raw == "" {
		raw = strings.TrimSpace(a.cfg.PublicBaseURL)
	}
	if raw == "" {
		return publicRelayEndpoint{}, errors.New("PUBLIC_RELAY_URL or PUBLIC_BASE_URL is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return publicRelayEndpoint{}, fmt.Errorf("invalid PUBLIC_RELAY_URL: %w", err)
	}

	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return publicRelayEndpoint{}, errors.New("PUBLIC_RELAY_URL must include a host")
	}

	port := 0
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port <= 0 || port > 65535 {
			return publicRelayEndpoint{}, errors.New("PUBLIC_RELAY_URL must include a valid port")
		}
	} else {
		switch parsed.Scheme {
		case "https", "wss":
			port = 443
		case "http", "ws":
			port = 80
		default:
			return publicRelayEndpoint{}, fmt.Errorf("unsupported PUBLIC_RELAY_URL scheme %q", parsed.Scheme)
		}
	}

	tlsEnabled := parsed.Scheme == "https" || parsed.Scheme == "wss"
	relayScheme := "tcp"
	if tlsEnabled {
		relayScheme = "tls"
	}

	return publicRelayEndpoint{
		Host:    host,
		Port:    port,
		Scheme:  relayScheme,
		TLS:     tlsEnabled,
		Address: fmt.Sprintf("%s:%d", host, port),
	}, nil
}

func (a *API) hostHeartbeat(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, true)
	if !ok {
		return
	}

	var req struct {
		ID                     string `json:"id"`
		Name                   string `json:"name"`
		AdvertiseAddr          string `json:"advertise_addr"`
		RelayPort              int    `json:"relay_port"`
		DockerSocket           bool   `json:"docker_socket"`
		Binder                 bool   `json:"binder"`
		AudioStreaming         bool   `json:"audio_streaming"`
		FileImport             bool   `json:"file_import"`
		CameraPassthrough      bool   `json:"camera_passthrough"`
		CameraSlots            int    `json:"camera_slots"`
		BlobStoreKind          string `json:"blob_store_kind"`
		StoragePreflightKind   string `json:"storage_preflight_kind"`
		StoragePreflightStatus string `json:"storage_preflight_status"`
		StoragePreflightJSON   string `json:"storage_preflight_json"`
		StoragePreflightAt     string `json:"storage_preflight_at"`
		StorageWalletAddress   string `json:"storage_wallet_address"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.ID) != "" && strings.TrimSpace(req.ID) != node.id {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed node does not match heartbeat body"})
		return
	}

	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.AdvertiseAddr) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id, name, and advertise_addr are required"})
		return
	}
	advertiseAddr, err := validateNodeAdvertiseAddr(req.AdvertiseAddr, a.cfg.NodeAllowedAdvertiseAddrs, a.cfg.AppEnv)
	if err != nil {
		if errors.Is(err, errNodeAdvertiseAllowlistUnavailable) {
			writeServerAPIError(w, http.StatusServiceUnavailable, "node_advertise_allowlist_unavailable", "node registration unavailable", err)
		} else {
			writeAPIError(w, http.StatusForbidden, "node_advertise_address_rejected", "node advertise address is not allowed")
		}
		return
	}
	req.AdvertiseAddr = advertiseAddr
	if len(req.StoragePreflightJSON) > maxSecurityEventJSONRunes {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "storage_preflight_json is too large"})
		return
	}

	var storagePreflightAt *time.Time
	if strings.TrimSpace(req.StoragePreflightAt) != "" {
		parsedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(req.StoragePreflightAt))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid storage_preflight_at"})
			return
		}
		storagePreflightAt = &parsedAt
	}

	host, err := a.store.UpsertHostHeartbeat(r.Context(), store.HostHeartbeat{
		ID:                     node.id,
		Name:                   req.Name,
		AdvertiseAddr:          req.AdvertiseAddr,
		RelayPort:              req.RelayPort,
		DockerSocket:           req.DockerSocket,
		Binder:                 req.Binder,
		AudioStreaming:         req.AudioStreaming,
		FileImport:             req.FileImport,
		CameraPassthrough:      req.CameraPassthrough,
		CameraSlots:            req.CameraSlots,
		PublicKey:              node.publicKey,
		BlobStoreKind:          req.BlobStoreKind,
		StoragePreflightKind:   req.StoragePreflightKind,
		StoragePreflightStatus: req.StoragePreflightStatus,
		StoragePreflightJSON:   req.StoragePreflightJSON,
		StoragePreflightAt:     storagePreflightAt,
		StorageWalletAddress:   req.StorageWalletAddress,
	})
	if err != nil {
		writeInternalAPIError(w, "host_heartbeat_upsert_failed", err)
		return
	}
	if _, err := a.store.AssignPendingRemoteRuntimeCleanup(r.Context(), host.ID, host.BlobStoreKind); err != nil {
		writeInternalAPIError(w, "remote_cleanup_assignment_failed", err)
		return
	}

	writeJSON(w, http.StatusAccepted, host)
}

func (a *API) hostAssignments(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}
	if pathHostID := strings.TrimSpace(r.PathValue("id")); pathHostID != "" && pathHostID != node.id {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed node does not match host assignment request"})
		return
	}

	items, err := a.store.ListAssignedRuntimes(r.Context(), node.id)
	if err != nil {
		writeInternalAPIError(w, "host_assignments_list_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) runtimeBlobKey(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}

	runtimeID := r.PathValue("id")
	hostID := strings.TrimSpace(r.URL.Query().Get("host_id"))
	if hostID != "" && hostID != node.id {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed node does not match blob-key host"})
		return
	}
	hostID = node.id
	if err := a.store.RequireRuntimeAssignedToHost(r.Context(), runtimeID, hostID); err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "runtime_assignment_check_failed", err)
		return
	}

	envelope, blobKeyVerifier, expiresAt, err := a.getActiveRuntimeBlobKeyHandoff(r.Context(), runtimeID, hostID)
	if errors.Is(err, store.ErrRuntimeBlobKeyHandoff) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "runtime active blob key envelope is not available"})
		return
	}
	if err != nil {
		writeInternalAPIError(w, "runtime_blob_key_handoff_lookup_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"blob_key_envelope":   envelope,
		"blob_key_verifier":   blobKeyVerifier,
		"blob_key_expires_at": expiresAt,
	})
}

func (a *API) activateRuntimeBlobKeyEnvelope(
	w http.ResponseWriter,
	r *http.Request,
	actor runtimeActor,
	runtimeID string,
	operation string,
	blobKeyVerifier string,
	envelope blobKeyEnvelope,
) (activeBlobKeyEntry, bool) {
	verifiedBlobKeyVerifier, err := a.verifiedActorBlobKeyVerifier(r.Context(), actor, blobKeyVerifier)
	if err != nil {
		writeIdentityAuthError(w, err)
		return activeBlobKeyEntry{}, false
	}
	handoff := activeBlobKeyHandoff{
		AccountID:       actor.accountID,
		RuntimeID:       runtimeID,
		HostID:          envelope.HostID,
		Operation:       operation,
		LeaseID:         envelope.LeaseID,
		Envelope:        envelope,
		BlobKeyVerifier: verifiedBlobKeyVerifier,
	}
	entry, err := a.activateRuntimeBlobKeyHandoff(r.Context(), handoff)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return activeBlobKeyEntry{}, false
	}
	return entry, true
}

func (a *API) activateRuntimeBlobKeyHandoff(ctx context.Context, handoff activeBlobKeyHandoff) (activeBlobKeyEntry, error) {
	if a.store == nil {
		return a.activeBlobKeys.activate(handoff)
	}
	entry := activeBlobKeyEntry{
		accountID:       strings.TrimSpace(handoff.AccountID),
		runtimeID:       strings.TrimSpace(handoff.RuntimeID),
		hostID:          strings.TrimSpace(handoff.HostID),
		operation:       strings.TrimSpace(handoff.Operation),
		leaseID:         strings.TrimSpace(handoff.LeaseID),
		envelope:        handoff.Envelope,
		blobKeyVerifier: strings.TrimSpace(handoff.BlobKeyVerifier),
		hasEnvelope:     true,
	}
	if err := validateBlobKeyEnvelope(handoff.Envelope, entry); err != nil {
		return activeBlobKeyEntry{}, err
	}
	if entry.blobKeyVerifier == "" {
		return activeBlobKeyEntry{}, errors.New("runtime blob-key verifier is required")
	}
	envelopeJSON, err := json.Marshal(handoff.Envelope)
	if err != nil {
		return activeBlobKeyEntry{}, err
	}
	persisted, err := a.store.ActivateRuntimeBlobKeyHandoff(ctx, store.RuntimeBlobKeyHandoff{
		AccountID:       entry.accountID,
		RuntimeID:       entry.runtimeID,
		HostID:          entry.hostID,
		Operation:       entry.operation,
		LeaseID:         entry.leaseID,
		EnvelopeJSON:    string(envelopeJSON),
		BlobKeyVerifier: entry.blobKeyVerifier,
	})
	if err != nil {
		return activeBlobKeyEntry{}, err
	}
	entry.expiresAt = persisted.ExpiresAt
	a.activeBlobKeys.cache(entry)
	return entry, nil
}

func (a *API) getActiveRuntimeBlobKeyHandoff(ctx context.Context, runtimeID, hostID string) (blobKeyEnvelope, string, time.Time, error) {
	if a.store != nil {
		handoff, err := a.store.GetRuntimeBlobKeyHandoff(ctx, runtimeID, hostID)
		if err != nil {
			return blobKeyEnvelope{}, "", time.Time{}, err
		}
		var envelope blobKeyEnvelope
		if err := json.Unmarshal([]byte(handoff.EnvelopeJSON), &envelope); err != nil {
			return blobKeyEnvelope{}, "", time.Time{}, fmt.Errorf("decode durable blob-key envelope: %w", err)
		}
		entry := activeBlobKeyEntry{
			accountID:       handoff.AccountID,
			runtimeID:       handoff.RuntimeID,
			hostID:          handoff.HostID,
			operation:       handoff.Operation,
			leaseID:         handoff.LeaseID,
			envelope:        envelope,
			blobKeyVerifier: handoff.BlobKeyVerifier,
			hasEnvelope:     true,
			expiresAt:       handoff.ExpiresAt,
		}
		if err := validateBlobKeyEnvelope(envelope, entry); err != nil {
			return blobKeyEnvelope{}, "", time.Time{}, fmt.Errorf("validate durable blob-key envelope: %w", err)
		}
		a.activeBlobKeys.cache(entry)
		return envelope, handoff.BlobKeyVerifier, handoff.ExpiresAt, nil
	}
	envelope, verifier, expiresAt, ok := a.activeBlobKeys.get(runtimeID, hostID)
	if !ok {
		return blobKeyEnvelope{}, "", time.Time{}, store.ErrRuntimeBlobKeyHandoff
	}
	return envelope, verifier, expiresAt, nil
}

func (a *API) verifyActiveBlobKeyEnvelope(w http.ResponseWriter, r *http.Request, entry activeBlobKeyEntry) bool {
	host, err := a.store.GetHost(r.Context(), entry.hostID)
	if err != nil {
		writeInternalAPIError(w, "blob_key_host_lookup_failed", err)
		return false
	}
	if err := a.verifyBlobKeyEnvelopeWithNode(r.Context(), host, entry); err != nil {
		a.activeBlobKeys.clear(entry.runtimeID)
		writeAPIError(w, http.StatusConflict, "blob_key_envelope_rejected", "blob-key envelope verification failed")
		return false
	}
	return true
}

func (a *API) verifyBlobKeyEnvelopeWithNode(ctx context.Context, host store.Host, entry activeBlobKeyEntry) error {
	advertiseAddr, err := validateNodeAdvertiseAddr(host.AdvertiseAddr, a.cfg.NodeAllowedAdvertiseAddrs, a.cfg.AppEnv)
	if err != nil {
		return err
	}
	relayPort := host.RelayPort
	if relayPort <= 0 {
		relayPort = 8090
	}
	body, err := json.Marshal(map[string]any{
		"blob_key_envelope":   entry.envelope,
		"blob_key_verifier":   entry.blobKeyVerifier,
		"blob_key_expires_at": entry.expiresAt,
	})
	if err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		verifyCtx,
		http.MethodPost,
		"http://"+net.JoinHostPort(advertiseAddr, strconv.Itoa(relayPort))+"/api/v1/internal/blob-key/verify",
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := a.signNodeCallback(req, body); err != nil {
		return err
	}
	resp, err := a.outboundClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("verify blob key envelope: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return nil
}

func (a *API) signNodeCallback(req *http.Request, body []byte) error {
	privateKey, _, err := nodeauth.LoadPrivateKey(a.cfg.ControlPlaneCallbackPrivateKey)
	if err != nil {
		return fmt.Errorf("load control-plane callback signing key: %w", err)
	}
	if privateKey == nil {
		return callbackauth.ErrMissingPrivateKey
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	return callbackauth.ApplySignedHeaders(
		req,
		privateKey,
		body,
		strconv.FormatInt(time.Now().Unix(), 10),
		base64.RawURLEncoding.EncodeToString(nonceBytes),
	)
}

func (a *API) verifiedActorBlobKeyVerifier(ctx context.Context, actor runtimeActor, blobKeyVerifier string) (string, error) {
	if actor.capability {
		return a.store.VerifiedAccountBlobKeyVerifier(ctx, actor.accountID, blobKeyVerifier)
	}
	return a.store.VerifiedDeviceBlobKeyVerifier(ctx, actor.accountID, actor.deviceID, blobKeyVerifier)
}

func (a *API) requireRuntimeActor(w http.ResponseWriter, r *http.Request, runtimeID string) (runtimeActor, bool) {
	if strings.TrimSpace(r.Header.Get("X-Virtroid-Capability-ID")) != "" {
		return a.requireRuntimeCapabilityRequest(w, r, runtimeID)
	}
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return runtimeActor{}, false
	}
	return runtimeActor{accountID: accountID, deviceID: deviceID}, true
}

func (a *API) requireSessionActor(w http.ResponseWriter, r *http.Request) (runtimeActor, bool) {
	if strings.TrimSpace(r.Header.Get("X-Virtroid-Capability-ID")) == "" {
		accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
		if !ok {
			return runtimeActor{}, false
		}
		return runtimeActor{accountID: accountID, deviceID: deviceID}, true
	}
	runtimeID := strings.TrimSpace(r.URL.Query().Get("runtime_id"))
	if runtimeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "runtime_id is required for capability-authenticated session requests"})
		return runtimeActor{}, false
	}
	return a.requireRuntimeCapabilityRequest(w, r, runtimeID)
}

func (a *API) requireRuntimeCapabilityRequest(w http.ResponseWriter, r *http.Request, runtimeID string) (runtimeActor, bool) {
	capabilityID := strings.TrimSpace(r.Header.Get("X-Virtroid-Capability-ID"))
	timestampRaw := strings.TrimSpace(r.Header.Get("X-Virtroid-Capability-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Virtroid-Capability-Nonce"))
	bodyHash := strings.TrimSpace(r.Header.Get("X-Virtroid-Capability-Body-SHA256"))
	signatureRaw := strings.TrimSpace(r.Header.Get("X-Virtroid-Capability-Signature"))
	if capabilityID == "" || timestampRaw == "" || nonce == "" || bodyHash == "" || signatureRaw == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "runtime capability signature headers are required"})
		return runtimeActor{}, false
	}

	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid runtime capability signature timestamp"})
		return runtimeActor{}, false
	}
	if skew := time.Since(time.Unix(timestamp, 0)); skew > 5*time.Minute || skew < -5*time.Minute {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "runtime capability signature timestamp is outside the allowed window"})
		return runtimeActor{}, false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read request body"})
		return runtimeActor{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	actualBodyHashBytes := sha256.Sum256(body)
	actualBodyHash := base64.RawURLEncoding.EncodeToString(actualBodyHashBytes[:])
	if bodyHash != actualBodyHash {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "runtime capability signature body hash mismatch"})
		return runtimeActor{}, false
	}

	accountID, publicKeyMaterial, err := a.store.RuntimeCapabilityPublicKey(r.Context(), runtimeID, capabilityID)
	if err != nil {
		if errors.Is(err, store.ErrRuntimeCapability) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return runtimeActor{}, false
		}
		writeInternalAPIError(w, "runtime_capability_lookup_failed", err)
		return runtimeActor{}, false
	}
	publicKey, err := parseECDSAPublicKeyMaterial(publicKeyMaterial)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "stored runtime capability public key is invalid"})
		return runtimeActor{}, false
	}
	signatureDER, err := base64.RawURLEncoding.DecodeString(signatureRaw)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "runtime capability signature is invalid"})
		return runtimeActor{}, false
	}
	canonical := capabilityRequestCanonical(r.Method, r.URL.RequestURI(), capabilityID, timestampRaw, nonce, bodyHash)
	digest := sha256.Sum256([]byte(canonical))
	if !ecdsa.VerifyASN1(publicKey, digest[:], signatureDER) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "runtime capability signature verification failed"})
		return runtimeActor{}, false
	}

	if err := a.store.RememberRuntimeCapabilityNonce(r.Context(), capabilityID, nonce, time.Unix(timestamp, 0).Add(5*time.Minute)); err != nil {
		if err == store.ErrRuntimeCapabilityReplay {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return runtimeActor{}, false
		}
		writeInternalAPIError(w, "runtime_capability_nonce_failed", err)
		return runtimeActor{}, false
	}
	if err := a.store.TouchRuntimeCapability(r.Context(), capabilityID); err != nil {
		writeInternalAPIError(w, "runtime_capability_touch_failed", err)
		return runtimeActor{}, false
	}

	return runtimeActor{accountID: accountID, capabilityID: capabilityID, capability: true}, true
}

func (a *API) validateSignedDeviceBody(w http.ResponseWriter, signedAccountID, signedDeviceID, bodyAccountID, bodyDeviceID string) bool {
	if strings.TrimSpace(bodyAccountID) != "" && strings.TrimSpace(bodyAccountID) != signedAccountID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed account does not match request body"})
		return false
	}
	if strings.TrimSpace(bodyDeviceID) != "" && strings.TrimSpace(bodyDeviceID) != signedDeviceID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed device does not match request body"})
		return false
	}
	return true
}

func writeIdentityAuthError(w http.ResponseWriter, err error) {
	switch err {
	case store.ErrDeviceNotFound:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
	case store.ErrIdentityNotFound, store.ErrIdentityAuthFailed, store.ErrIdentityKeyRequired:
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	default:
		writeInternalAPIError(w, "identity_verification_failed", err)
	}
}

func normalizeBlobKeyOperation(operation string) string {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case "start", "stop", "wipe", "delete", "session":
		return strings.ToLower(strings.TrimSpace(operation))
	default:
		return ""
	}
}

func validateBlobKeyEnvelope(envelope blobKeyEnvelope, entry activeBlobKeyEntry) error {
	if envelope.Version != 1 {
		return errors.New("unsupported blob-key envelope version")
	}
	if strings.TrimSpace(envelope.Algorithm) != blobKeyEnvelopeAlgorithm {
		return errors.New("unsupported blob-key envelope algorithm")
	}
	if strings.TrimSpace(envelope.LeaseID) != entry.leaseID ||
		strings.TrimSpace(envelope.RuntimeID) != entry.runtimeID ||
		strings.TrimSpace(envelope.HostID) != entry.hostID ||
		strings.TrimSpace(envelope.Operation) != entry.operation {
		return errors.New("blob-key envelope does not match lease")
	}
	if strings.TrimSpace(envelope.EphemeralPublicKey) == "" ||
		strings.TrimSpace(envelope.IV) == "" ||
		strings.TrimSpace(envelope.Ciphertext) == "" {
		return errors.New("blob-key envelope is incomplete")
	}
	if len(envelope.EphemeralPublicKey) > 2048 || len(envelope.IV) > 128 || len(envelope.Ciphertext) > 1024 {
		return errors.New("blob-key envelope is too large")
	}
	return nil
}

func randomLeaseID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (a *API) sessionRelayTarget(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}

	relayToken := strings.TrimSpace(r.Header.Get("X-Virtroid-Relay-Token"))
	if relayToken == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "relay token is required"})
		return
	}

	target, err := a.store.ResolveSessionRelayTarget(r.Context(), r.PathValue("id"), relayToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "relay target not found"})
			return
		}
		writeInternalAPIError(w, "session_relay_target_failed", err)
		return
	}
	if target.HostID != node.id {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed node does not match relay target host"})
		return
	}

	writeJSON(w, http.StatusOK, target)
}

func (a *API) runtimeStatusUpdate(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}

	var req struct {
		HostID              string     `json:"host_id"`
		Status              string     `json:"status"`
		ConnectionStatus    string     `json:"connection_status"`
		ContainerName       *string    `json:"container_name"`
		ADBPort             *int       `json:"adb_port"`
		LastError           *string    `json:"last_error"`
		BlobLastSnapshotAt  *time.Time `json:"blob_last_snapshot_at"`
		LoadAverage         *float64   `json:"load_average"`
		ClearWipeRequested  bool       `json:"clear_wipe_requested"`
		ActivePersonaJSON   *string    `json:"active_persona_json"`
		ClearActivePersona  bool       `json:"clear_active_persona"`
		BlobStoreKind       *string    `json:"blob_store_kind"`
		BlobManifestJSON    *string    `json:"blob_manifest_json"`
		ClearBlobManifest   bool       `json:"clear_blob_manifest"`
		CleanupComplete     bool       `json:"cleanup_complete"`
		OperationGeneration int64      `json:"operation_generation"`
		Deleted             bool       `json:"deleted"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.HostID) != "" && strings.TrimSpace(req.HostID) != node.id {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed node does not match runtime status host"})
		return
	}
	if req.OperationGeneration <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "operation_generation is required"})
		return
	}

	if err := a.store.UpdateRuntimeObservation(r.Context(), r.PathValue("id"), store.RuntimeObservation{
		HostID:              node.id,
		Status:              strings.TrimSpace(req.Status),
		ConnectionStatus:    strings.TrimSpace(req.ConnectionStatus),
		ContainerName:       req.ContainerName,
		ADBPort:             req.ADBPort,
		LastError:           req.LastError,
		BlobLastSnapshotAt:  req.BlobLastSnapshotAt,
		LoadAverage:         req.LoadAverage,
		ClearWipeRequested:  req.ClearWipeRequested,
		ActivePersonaJSON:   req.ActivePersonaJSON,
		ClearActivePersona:  req.ClearActivePersona,
		BlobStoreKind:       req.BlobStoreKind,
		BlobManifestJSON:    req.BlobManifestJSON,
		ClearBlobManifest:   req.ClearBlobManifest,
		CleanupComplete:     req.CleanupComplete,
		OperationGeneration: req.OperationGeneration,
		Deleted:             req.Deleted,
	}); err != nil {
		if errors.Is(err, store.ErrRuntimeObservationStale) {
			writeAPIError(w, http.StatusConflict, "runtime_observation_stale", err.Error())
			return
		}
		if errors.Is(err, store.ErrRuntimeStorageQuota) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, store.RuntimeStorageQuotaExceededCode, err.Error())
			return
		}
		if errors.Is(err, store.ErrRuntimeSnapshotRollback) {
			writeAPIError(w, http.StatusConflict, store.RuntimeSnapshotRollbackCode, err.Error())
			return
		}
		writeInternalAPIError(w, "runtime_status_update_failed", err)
		return
	}
	if req.CleanupComplete || req.Deleted {
		a.activeBlobKeys.clear(r.PathValue("id"))
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) runtimeLogAppend(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}

	var req struct {
		Source  string `json:"source"`
		Level   string `json:"level"`
		Message string `json:"message"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
		return
	}
	if err := a.store.RequireRuntimeAssignedToHost(r.Context(), r.PathValue("id"), node.id); err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeInternalAPIError(w, "runtime_assignment_check_failed", err)
		return
	}

	if err := a.store.AppendRuntimeLog(
		r.Context(),
		r.PathValue("id"),
		trimForStorage(req.Source, maxRuntimeLogSourceRunes),
		trimForStorage(req.Level, maxRuntimeLogLevelRunes),
		trimForStorage(req.Message, maxRuntimeLogMessageRunes),
	); err != nil {
		writeInternalAPIError(w, "runtime_log_append_failed", err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) securityEventAppend(w http.ResponseWriter, r *http.Request) {
	node, ok := a.requireNodeRequest(w, r, false)
	if !ok {
		return
	}

	var req struct {
		Source   string          `json:"source"`
		Rule     string          `json:"rule"`
		Priority string          `json:"priority"`
		Output   string          `json:"output"`
		Tags     []string        `json:"tags"`
		Time     *time.Time      `json:"time"`
		Event    json.RawMessage `json:"event"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}

	rule := trimForStorage(req.Rule, 256)
	output := trimForStorage(req.Output, maxSecurityEventOutputRunes)
	if rule == "" && output == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rule or output is required"})
		return
	}

	eventJSON := strings.TrimSpace(string(req.Event))
	if eventJSON == "" || !json.Valid(req.Event) {
		fallback, _ := json.Marshal(map[string]any{
			"source":   req.Source,
			"rule":     req.Rule,
			"priority": req.Priority,
			"output":   req.Output,
			"tags":     req.Tags,
			"time":     req.Time,
		})
		eventJSON = string(fallback)
	}
	eventJSON = trimForStorage(eventJSON, maxSecurityEventJSONRunes)

	rateLimit := a.cfg.SecurityEventRateLimitPerMinute
	if rateLimit <= 0 {
		rateLimit = defaultSecurityEventRateLimitPerMinute
	}
	retention := a.cfg.SecurityEventRetention
	if retention <= 0 {
		retention = defaultSecurityEventRetention
	}

	if err := a.store.AppendSecurityEventLimited(r.Context(), store.SecurityEventInput{
		NodeID:    node.id,
		Source:    trimForStorage(req.Source, 64),
		Rule:      rule,
		Priority:  trimForStorage(req.Priority, 32),
		Output:    output,
		Tags:      normalizeSecurityEventTags(req.Tags),
		EventJSON: eventJSON,
		EventTime: req.Time,
	}, rateLimit, retention); err != nil {
		if errors.Is(err, store.ErrSecurityEventRateLimit) {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "rate_limited": true})
			return
		}
		writeInternalAPIError(w, "security_event_append_failed", err)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) requireSignedDeviceRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	accountID := strings.TrimSpace(r.Header.Get("X-Virtroid-Account-ID"))
	deviceID := strings.TrimSpace(r.Header.Get("X-Virtroid-Device-ID"))
	timestampRaw := strings.TrimSpace(r.Header.Get("X-Virtroid-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Virtroid-Nonce"))
	bodyHash := strings.TrimSpace(r.Header.Get("X-Virtroid-Body-SHA256"))
	signatureRaw := strings.TrimSpace(r.Header.Get("X-Virtroid-Signature"))
	if accountID == "" || deviceID == "" || timestampRaw == "" || nonce == "" || bodyHash == "" || signatureRaw == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "signed device request headers are required"})
		return "", "", false
	}

	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid device signature timestamp"})
		return "", "", false
	}
	if skew := time.Since(time.Unix(timestamp, 0)); skew > 5*time.Minute || skew < -5*time.Minute {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "device signature timestamp is outside the allowed window"})
		return "", "", false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read request body"})
		return "", "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	actualBodyHashBytes := sha256.Sum256(body)
	actualBodyHash := base64.RawURLEncoding.EncodeToString(actualBodyHashBytes[:])
	if bodyHash != actualBodyHash {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "device signature body hash mismatch"})
		return "", "", false
	}

	publicKeyMaterial, err := a.store.DevicePublicKey(r.Context(), accountID, deviceID)
	if err != nil {
		if err == store.ErrDeviceNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return "", "", false
		}
		writeInternalAPIError(w, "device_key_lookup_failed", err)
		return "", "", false
	}
	publicKeyDER, err := base64.StdEncoding.DecodeString(publicKeyMaterial)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "stored device public key is invalid"})
		return "", "", false
	}
	parsedPublicKey, err := x509.ParsePKIXPublicKey(publicKeyDER)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "stored device public key is invalid"})
		return "", "", false
	}
	publicKey, ok := parsedPublicKey.(*ecdsa.PublicKey)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "stored device public key type is unsupported"})
		return "", "", false
	}
	signatureDER, err := base64.RawURLEncoding.DecodeString(signatureRaw)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "device signature is invalid"})
		return "", "", false
	}

	canonical := signedRequestCanonical(r.Method, r.URL.RequestURI(), accountID, deviceID, timestampRaw, nonce, bodyHash)
	digest := sha256.Sum256([]byte(canonical))
	if !ecdsa.VerifyASN1(publicKey, digest[:], signatureDER) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "device signature verification failed"})
		return "", "", false
	}

	if err := a.store.RememberDeviceRequestNonce(r.Context(), accountID, deviceID, nonce, time.Unix(timestamp, 0).Add(5*time.Minute)); err != nil {
		if err == store.ErrDeviceRequestReplay {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return "", "", false
		}
		writeInternalAPIError(w, "device_nonce_persist_failed", err)
		return "", "", false
	}

	return accountID, deviceID, true
}

func signedRequestCanonical(method, requestURI, accountID, deviceID, timestamp, nonce, bodyHash string) string {
	return strings.Join([]string{
		"VIRTROID-DEVICE-SIGNATURE-V1",
		strings.ToUpper(method),
		requestURI,
		accountID,
		deviceID,
		timestamp,
		nonce,
		bodyHash,
	}, "\n")
}

func capabilityRequestCanonical(method, requestURI, capabilityID, timestamp, nonce, bodyHash string) string {
	return strings.Join([]string{
		"VIRTROID-CAPABILITY-SIGNATURE-V1",
		strings.ToUpper(method),
		requestURI,
		capabilityID,
		timestamp,
		nonce,
		bodyHash,
	}, "\n")
}

func runtimeCapabilityID(runtimeID, publicKeyMaterial string) string {
	material := strings.Join([]string{
		"VIRTROID-RUNTIME-CAPABILITY-ID-V1",
		strings.TrimSpace(runtimeID),
		strings.TrimSpace(publicKeyMaterial),
	}, "\n")
	digest := sha256.Sum256([]byte(material))
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}

func parseECDSAPublicKeyMaterial(publicKeyMaterial string) (*ecdsa.PublicKey, error) {
	publicKeyDER, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyMaterial))
	if err != nil {
		return nil, err
	}
	parsedPublicKey, err := x509.ParsePKIXPublicKey(publicKeyDER)
	if err != nil {
		return nil, err
	}
	publicKey, ok := parsedPublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("unsupported ECDSA public key type")
	}
	return publicKey, nil
}

type nodeRequestIdentity struct {
	id        string
	publicKey string
}

func (a *API) requireNodeRequest(w http.ResponseWriter, r *http.Request, allowDevelopmentEnrollment bool) (nodeRequestIdentity, bool) {
	nodeID := strings.TrimSpace(r.Header.Get(nodeauth.HeaderNodeID))
	timestampRaw := strings.TrimSpace(r.Header.Get(nodeauth.HeaderTimestamp))
	nonce := strings.TrimSpace(r.Header.Get(nodeauth.HeaderNonce))
	bodyHash := strings.TrimSpace(r.Header.Get(nodeauth.HeaderBodySHA256))
	signatureRaw := strings.TrimSpace(r.Header.Get(nodeauth.HeaderSignature))
	if nodeID == "" || timestampRaw == "" || nonce == "" || bodyHash == "" || signatureRaw == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "signed node request headers are required"})
		return nodeRequestIdentity{}, false
	}

	timestamp, err := strconv.ParseInt(timestampRaw, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid node signature timestamp"})
		return nodeRequestIdentity{}, false
	}
	if skew := time.Since(time.Unix(timestamp, 0)); skew > 5*time.Minute || skew < -5*time.Minute {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "node signature timestamp is outside the allowed window"})
		return nodeRequestIdentity{}, false
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read request body"})
		return nodeRequestIdentity{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	if actualBodyHash := nodeauth.BodyHash(body); bodyHash != actualBodyHash {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "node signature body hash mismatch"})
		return nodeRequestIdentity{}, false
	}
	if a.store == nil {
		writeInternalAPIError(w, "store_unavailable", errors.New("store is not configured"))
		return nodeRequestIdentity{}, false
	}

	approvedKeys, err := a.store.AuthorizedNodeKeys(r.Context(), nodeID)
	developmentEnrollment := false
	if err != nil && !errors.Is(err, store.ErrApprovedNodeNotFound) {
		writeInternalAPIError(w, "node_key_lookup_failed", err)
		return nodeRequestIdentity{}, false
	}
	if len(approvedKeys) == 0 {
		if !allowDevelopmentEnrollment ||
			!strings.EqualFold(strings.TrimSpace(a.cfg.AppEnv), "development") ||
			!a.cfg.NodeDevelopmentEnrollmentEnabled {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "node is not approved"})
			return nodeRequestIdentity{}, false
		}
		if a.cfg.NodeRegistrationSecret != "" && r.Header.Get(nodeauth.HeaderRegistrationSecret) != a.cfg.NodeRegistrationSecret {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid node registration secret"})
			return nodeRequestIdentity{}, false
		}
		publicKey, fingerprint, normalizeErr := nodeauth.NormalizePublicKey(r.Header.Get(nodeauth.HeaderPublicKey))
		if normalizeErr != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "node public key is required for registration"})
			return nodeRequestIdentity{}, false
		}
		approvedKeys = []store.ApprovedNodeKey{{
			NodeID:            nodeID,
			KeyVersion:        1,
			PublicKey:         publicKey,
			FingerprintSHA256: fingerprint,
			State:             "active",
		}}
		developmentEnrollment = true
	}

	verifiedPublicKey := ""
	for _, approvedKey := range approvedKeys {
		if err := nodeauth.Verify(
			approvedKey.PublicKey,
			r.Method,
			r.URL.RequestURI(),
			nodeID,
			timestampRaw,
			nonce,
			bodyHash,
			signatureRaw,
		); err == nil {
			verifiedPublicKey = approvedKey.PublicKey
			break
		}
	}
	if verifiedPublicKey == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "node signature verification failed"})
		return nodeRequestIdentity{}, false
	}
	if developmentEnrollment {
		if _, err := a.store.EnrollDevelopmentNode(r.Context(), nodeID, verifiedPublicKey); err != nil {
			writeInternalAPIError(w, "development_node_enrollment_failed", err)
			return nodeRequestIdentity{}, false
		}
	}

	if err := a.store.RememberNodeRequestNonce(r.Context(), nodeID, nonce, time.Unix(timestamp, 0).Add(5*time.Minute)); err != nil {
		if err == store.ErrNodeRequestReplay {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
			return nodeRequestIdentity{}, false
		}
		writeInternalAPIError(w, "node_nonce_persist_failed", err)
		return nodeRequestIdentity{}, false
	}

	return nodeRequestIdentity{id: nodeID, publicKey: verifiedPublicKey}, true
}

func (a *API) prepareViewer(ctx context.Context, advertiseAddr string, relayPort int, runtimeID string, maxSize, bitRate int) (string, error) {
	var err error
	advertiseAddr, err = validateNodeAdvertiseAddr(advertiseAddr, a.cfg.NodeAllowedAdvertiseAddrs, a.cfg.AppEnv)
	if err != nil {
		return "", err
	}
	if relayPort <= 0 {
		relayPort = 8090
	}
	body, err := json.Marshal(map[string]any{
		"runtime_id": runtimeID,
		"max_size":   maxSize,
		"bit_rate":   bitRate,
	})
	if err != nil {
		return "", err
	}

	prepareCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), viewerPrepareProxyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		prepareCtx,
		http.MethodPost,
		"http://"+net.JoinHostPort(advertiseAddr, strconv.Itoa(relayPort))+"/api/v1/internal/viewer/prepare",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := a.signNodeCallback(req, body); err != nil {
		return "", err
	}

	resp, err := a.outboundClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("viewer prepare failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	var payload struct {
		ViewerPublicKey string `json:"viewer_public_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	viewerPublicKey := strings.TrimSpace(payload.ViewerPublicKey)
	if viewerPublicKey == "" {
		return "", errors.New("viewer prepare did not return a public key")
	}
	return viewerPublicKey, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (a *API) outboundClient() *http.Client {
	if a.httpClient != nil {
		return a.httpClient
	}
	return http.DefaultClient
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	payload := map[string]any{"error": message}
	if strings.TrimSpace(code) != "" {
		payload["code"] = code
	}
	writeJSON(w, status, payload)
}

func writeServerAPIError(w http.ResponseWriter, status int, code, message string, err error) {
	if err != nil {
		log.Printf("httpapi server error status=%d", status)
	}
	writeAPIError(w, status, code, message)
}

func writeInternalAPIError(w http.ResponseWriter, code string, err error) {
	writeServerAPIError(w, http.StatusInternalServerError, code, "internal server error", err)
}

func writeRuntimeMutationError(w http.ResponseWriter, err error) {
	switch err {
	case store.ErrRuntimeNotFound:
		writeAPIError(w, http.StatusNotFound, "runtime_not_found", err.Error())
	case store.ErrNoReadyHost:
		writeAPIError(w, http.StatusConflict, store.NoReadyHostCode, err.Error())
	case store.ErrRuntimeBlobOwner:
		writeAPIError(w, http.StatusConflict, store.RuntimeBlobOwnerCode, err.Error())
	case store.ErrRuntimeCleanupPending:
		writeAPIError(w, http.StatusConflict, store.RuntimeCleanupPendingCode, err.Error())
	case store.ErrRuntimeNotReady:
		writeAPIError(w, http.StatusConflict, store.RuntimeNotReadyCode, err.Error())
	case store.ErrRuntimeMediaSettingsActive:
		writeAPIError(w, http.StatusConflict, store.RuntimeMediaSettingsActiveCode, err.Error())
	case store.ErrRuntimeEntitlement:
		writeAPIError(w, http.StatusPaymentRequired, store.RuntimeEntitlementRequiredCode, err.Error())
	case store.ErrRuntimeQuota:
		writeAPIError(w, http.StatusConflict, store.RuntimeQuotaExceededCode, err.Error())
	case store.ErrRuntimeActiveQuota:
		writeAPIError(w, http.StatusConflict, store.ActiveRuntimeQuotaExceededCode, err.Error())
	case store.ErrRuntimeStartQuota:
		writeAPIError(w, http.StatusTooManyRequests, store.RuntimeStartQuotaExceededCode, err.Error())
	case store.ErrRuntimeTrialTimeQuota:
		writeAPIError(w, http.StatusPaymentRequired, store.RuntimeTrialTimeExceededCode, err.Error())
	case store.ErrRuntimeProfile:
		writeAPIError(w, http.StatusBadRequest, store.RuntimeProfileNotAllowedCode, err.Error())
	default:
		writeInternalAPIError(w, "runtime_mutation_failed", err)
	}
}

func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if responseMustNotBeStored(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
		}
		if r.Body != nil {
			limit := apiRequestBodyLimit(r.URL.Path)
			if r.ContentLength > limit {
				writeAPIError(w, http.StatusRequestEntityTooLarge, "request_body_too_large", "request body is too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func apiRequestBodyLimit(path string) int64 {
	if strings.HasSuffix(path, "/files") {
		return maxRuntimeFileImportBytes
	}
	if strings.HasSuffix(path, "/photos") {
		return maxRuntimePhotoImportBytes
	}
	return maxAPIRequestBodyBytes
}

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("http handler panic")
				writeAPIError(w, http.StatusInternalServerError, "internal_server_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func responseMustNotBeStored(path string) bool {
	path = strings.TrimSpace(path)
	return path == "/api/v1/bootstrap" ||
		path == "/api/v1/hosts" ||
		path == "/api/v1/me" ||
		strings.HasPrefix(path, "/api/v1/me/") ||
		path == "/api/v1/internal" ||
		strings.HasPrefix(path, "/api/v1/internal/")
}

func websocketBase(base string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return strings.TrimRight(base, "/")
	}

	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	}

	return strings.TrimRight(parsed.String(), "/")
}

func normalizeSecurityEventTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, min(len(tags), maxSecurityEventTags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = trimForStorage(tag, maxSecurityEventTagRunes)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
		if len(normalized) >= maxSecurityEventTags {
			break
		}
	}
	return normalized
}

func trimForStorage(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
