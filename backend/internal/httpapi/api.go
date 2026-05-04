package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"virtdroid/backend/internal/config"
	"virtdroid/backend/internal/store"
)

type API struct {
	cfg              config.ServerConfig
	store            *store.Store
	activeBlobKeys   *activeBlobKeyVault
	bootstrapLimiter *bootstrapRateLimiter
}

type activeBlobKeyVault struct {
	mu   sync.Mutex
	keys map[string]activeBlobKeyEntry
}

type activeBlobKeyEntry struct {
	key       string
	expiresAt time.Time
}

const activeBlobKeyHandoffTTL = 10 * time.Minute
const bootstrapRateLimitWindow = time.Minute
const defaultBootstrapMaxBodyBytes = 32 * 1024

type bootstrapRateLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	clients map[string]bootstrapRateLimitEntry
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
		max:     max,
		window:  window,
		clients: map[string]bootstrapRateLimitEntry{},
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

	entry := l.clients[client]
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

func newActiveBlobKeyVault() *activeBlobKeyVault {
	return &activeBlobKeyVault{keys: map[string]activeBlobKeyEntry{}}
}

func (v *activeBlobKeyVault) put(runtimeID string, key string) time.Time {
	expiresAt := time.Now().UTC().Add(activeBlobKeyHandoffTTL)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys[runtimeID] = activeBlobKeyEntry{key: key, expiresAt: expiresAt}
	return expiresAt
}

func (v *activeBlobKeyVault) get(runtimeID string) (string, time.Time, bool) {
	now := time.Now().UTC()
	v.mu.Lock()
	defer v.mu.Unlock()
	entry, ok := v.keys[runtimeID]
	if !ok {
		return "", time.Time{}, false
	}
	if !entry.expiresAt.After(now) {
		delete(v.keys, runtimeID)
		return "", time.Time{}, false
	}
	return entry.key, entry.expiresAt, true
}

func (v *activeBlobKeyVault) clear(runtimeID string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.keys, runtimeID)
}

func New(cfg config.ServerConfig, st *store.Store) http.Handler {
	bootstrapMaxBodyBytes := cfg.BootstrapMaxBodyBytes
	if bootstrapMaxBodyBytes <= 0 {
		bootstrapMaxBodyBytes = defaultBootstrapMaxBodyBytes
	}
	cfg.BootstrapMaxBodyBytes = bootstrapMaxBodyBytes

	api := &API{
		cfg:              cfg,
		store:            st,
		activeBlobKeys:   newActiveBlobKeyVault(),
		bootstrapLimiter: newBootstrapRateLimiter(cfg.BootstrapRateLimitPerMinute, bootstrapRateLimitWindow),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.healthz)
	mux.HandleFunc("GET /api/v1/meta", api.meta)
	mux.HandleFunc("GET /api/v1/hosts", api.hosts)
	mux.HandleFunc("POST /api/v1/bootstrap", api.bootstrap)

	mux.HandleFunc("GET /api/v1/me/runtimes", api.listMyRuntimes)
	mux.HandleFunc("GET /api/v1/me/storage", api.getMyStorage)
	mux.HandleFunc("PUT /api/v1/me/storage", api.updateMyStorage)
	mux.HandleFunc("POST /api/v1/me/identity/register", api.registerMyIdentity)
	mux.HandleFunc("POST /api/v1/me/runtimes", api.createMyRuntime)
	mux.HandleFunc("GET /api/v1/me/runtimes/{id}", api.getMyRuntime)
	mux.HandleFunc("PATCH /api/v1/me/runtimes/{id}", api.updateMyRuntime)
	mux.HandleFunc("DELETE /api/v1/me/runtimes/{id}", api.deleteMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/start", api.startMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/stop", api.stopMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/wipe", api.wipeMyRuntime)
	mux.HandleFunc("POST /api/v1/me/runtimes/{id}/session", api.createMyRuntimeSession)
	mux.HandleFunc("GET /api/v1/me/runtimes/{id}/logs", api.runtimeLogs)
	mux.HandleFunc("POST /api/v1/me/sessions/{id}/close", api.closeMySession)

	mux.HandleFunc("POST /api/v1/internal/hosts/heartbeat", api.hostHeartbeat)
	mux.HandleFunc("GET /api/v1/internal/hosts/{id}/assignments", api.hostAssignments)
	mux.HandleFunc("GET /api/v1/internal/sessions/{id}/relay", api.sessionRelayTarget)
	mux.HandleFunc("GET /api/v1/internal/runtimes/{id}/blob-key", api.runtimeBlobKey)
	mux.HandleFunc("POST /api/v1/internal/runtimes/{id}/status", api.runtimeStatusUpdate)
	mux.HandleFunc("POST /api/v1/internal/runtimes/{id}/logs", api.runtimeLogAppend)

	return withJSON(mux)
}

func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
	})
}

func (a *API) meta(w http.ResponseWriter, r *http.Request) {
	relayURL := strings.TrimSpace(a.cfg.PublicRelayURL)
	if relayURL == "" {
		relayURL = a.cfg.PublicBaseURL
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"service":              "virtdroidd",
		"environment":          a.cfg.AppEnv,
		"public_base_url":      a.cfg.PublicBaseURL,
		"public_relay_url":     relayURL,
		"relay_websocket_base": websocketBase(relayURL) + "/api/v1/relay",
	})
}

func (a *API) hosts(w http.ResponseWriter, r *http.Request) {
	if !a.internalAuthorized(w, r) {
		return
	}

	hosts, err := a.store.ListHosts(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": hosts})
}

func (a *API) bootstrap(w http.ResponseWriter, r *http.Request) {
	productionBootstrap := a.cfg.AppEnv != "development"
	if productionBootstrap && strings.TrimSpace(a.cfg.BootstrapToken) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bootstrap is not configured"})
		return
	}
	if ok, retryAfter := a.bootstrapLimiter.allow(bootstrapClientKey(r)); !ok {
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "bootstrap rate limit exceeded"})
		return
	}
	if productionBootstrap && subtle.ConstantTimeCompare(
		[]byte(r.Header.Get("X-Virtdroid-Bootstrap-Token")),
		[]byte(a.cfg.BootstrapToken),
	) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid bootstrap token"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.BootstrapMaxBodyBytes)

	var req struct {
		AccountID        string `json:"account_id"`
		DeviceID         string `json:"device_id"`
		DeviceName       string `json:"device_name"`
		PublicKey        string `json:"public_key"`
		RuntimeName      string `json:"runtime_name"`
		AndroidImage     string `json:"android_image"`
		AndroidVersion   string `json:"android_version"`
		WidthPx          int    `json:"width_px"`
		HeightPx         int    `json:"height_px"`
		DensityDpi       int    `json:"density_dpi"`
		AudioEnabled     bool   `json:"audio_enabled"`
		CameraMode       string `json:"camera_mode"`
		FileMode         string `json:"file_mode"`
		BlobAutoSnapshot bool   `json:"blob_auto_snapshot"`
		BlobRetainDays   int    `json:"blob_retain_days"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "bootstrap request body is too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}

	result, err := a.store.BootstrapAccountWithIdentity(
		r.Context(),
		req.AccountID,
		req.DeviceID,
		req.DeviceName,
		req.PublicKey,
		store.CreateRuntimeInput{
			Name:             req.RuntimeName,
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
		},
	)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

func bootstrapClientKey(r *http.Request) string {
	remoteHost := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remoteHost); err == nil {
		remoteHost = host
	}
	forwardedFor := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwardedFor != "" {
		if comma := strings.IndexByte(forwardedFor, ','); comma >= 0 {
			forwardedFor = forwardedFor[:comma]
		}
		forwardedFor = strings.TrimSpace(forwardedFor)
	}
	if remoteHost == "" {
		remoteHost = "unknown"
	}
	if forwardedFor == "" {
		return remoteHost
	}
	return remoteHost + "|" + forwardedFor
}

func (a *API) listMyRuntimes(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	runtimes, err := a.store.ListRuntimes(r.Context(), accountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": runtimes})
}

func (a *API) getMyStorage(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	storage, err := a.store.GetAccountStorage(r.Context(), accountID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, storage)
}

func (a *API) updateMyStorage(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID          string  `json:"account_id"`
		Provider           string  `json:"provider"`
		FundingModel       string  `json:"funding_model"`
		WalletAddress      *string `json:"wallet_address"`
		EncryptedSeedBlob  *string `json:"encrypted_seed_blob"`
		SeedEncryptionHint *string `json:"seed_encryption_hint"`
		Status             string  `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(req.AccountID) != "" && strings.TrimSpace(req.AccountID) != accountID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "signed account does not match request body"})
		return
	}

	storage, err := a.store.UpdateAccountStorage(r.Context(), accountID, store.UpdateAccountStorageInput{
		Provider:           req.Provider,
		FundingModel:       req.FundingModel,
		WalletAddress:      req.WalletAddress,
		EncryptedSeedBlob:  req.EncryptedSeedBlob,
		SeedEncryptionHint: req.SeedEncryptionHint,
		Status:             req.Status,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, storage)
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
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
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
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, runtime)
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
		AudioEnabled     bool   `json:"audio_enabled"`
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

	runtime, err := a.store.CreateRuntime(r.Context(), accountID, store.CreateRuntimeInput{
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, runtime)
}

func (a *API) deleteMyRuntime(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	runtime, err := a.store.DeleteRuntime(r.Context(), accountID, r.PathValue("id"))
	if err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) startMyRuntime(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID     string `json:"account_id"`
		DeviceID      string `json:"device_id"`
		BlobAccessKey string `json:"blob_access_key"`
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

	blobAccessKey, err := a.store.VerifyDeviceBlobAccessKey(r.Context(), accountID, deviceID, req.BlobAccessKey)
	if err != nil {
		switch err {
		case store.ErrDeviceNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrIdentityNotFound, store.ErrIdentityAuthFailed, store.ErrIdentityKeyRequired:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}

	runtimeID := r.PathValue("id")
	a.activeBlobKeys.put(runtimeID, blobAccessKey)
	runtime, err := a.store.StartRuntime(r.Context(), accountID, runtimeID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		switch err {
		case store.ErrRuntimeNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrNoReadyHost:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) stopMyRuntime(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID     string `json:"account_id"`
		DeviceID      string `json:"device_id"`
		BlobAccessKey string `json:"blob_access_key"`
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

	blobAccessKey, err := a.store.VerifyDeviceBlobAccessKey(r.Context(), accountID, deviceID, req.BlobAccessKey)
	if err != nil {
		switch err {
		case store.ErrDeviceNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrIdentityNotFound, store.ErrIdentityAuthFailed, store.ErrIdentityKeyRequired:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}

	runtimeID := r.PathValue("id")
	a.activeBlobKeys.put(runtimeID, blobAccessKey)
	runtime, err := a.store.StopRuntime(r.Context(), accountID, runtimeID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		switch err {
		case store.ErrRuntimeNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}

	writeJSON(w, http.StatusAccepted, runtime)
}

func (a *API) wipeMyRuntime(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID     string `json:"account_id"`
		DeviceID      string `json:"device_id"`
		BlobAccessKey string `json:"blob_access_key"`
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

	blobAccessKey, err := a.store.VerifyDeviceBlobAccessKey(r.Context(), accountID, deviceID, req.BlobAccessKey)
	if err != nil {
		switch err {
		case store.ErrDeviceNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrIdentityNotFound, store.ErrIdentityAuthFailed, store.ErrIdentityKeyRequired:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}

	runtimeID := r.PathValue("id")
	a.activeBlobKeys.put(runtimeID, blobAccessKey)
	runtime, err := a.store.WipeRuntime(r.Context(), accountID, runtimeID)
	if err != nil {
		a.activeBlobKeys.clear(runtimeID)
		switch err {
		case store.ErrRuntimeNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
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
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": logs})
}

func (a *API) createMyRuntimeSession(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		AccountID     string `json:"account_id"`
		DeviceID      string `json:"device_id"`
		MaxSize       int    `json:"max_size"`
		BitRate       int    `json:"bit_rate"`
		BlobAccessKey string `json:"blob_access_key"`
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

	blobAccessKey, err := a.store.VerifyDeviceBlobAccessKey(r.Context(), accountID, deviceID, req.BlobAccessKey)
	if err != nil {
		switch err {
		case store.ErrDeviceNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrIdentityNotFound, store.ErrIdentityAuthFailed, store.ErrIdentityKeyRequired:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}

	runtime, err := a.store.GetRuntime(r.Context(), accountID, r.PathValue("id"))
	if err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if runtime.Status != "running" || runtime.DesiredState != "running" || runtime.HostID == nil || runtime.ViewerPort == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": store.ErrRuntimeNotReady.Error()})
		return
	}
	a.activeBlobKeys.put(runtime.ID, blobAccessKey)

	host, err := a.store.GetHost(r.Context(), *runtime.HostID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	session, err := a.store.CreateSession(r.Context(), deviceID, runtime.ID)
	if err != nil {
		switch err {
		case store.ErrRuntimeNotFound:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		case store.ErrRuntimeNotReady:
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
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

	if err := a.prepareViewer(r.Context(), host.AdvertiseAddr, host.RelayPort, runtime.ID, maxSize, bitRate); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	relayEndpoint, err := a.publicRelayEndpoint()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"session":        session,
		"viewer_host":    relayEndpoint.Host,
		"viewer_port":    relayEndpoint.Port,
		"viewer_address": relayEndpoint.Address,
		"relay_host":     relayEndpoint.Host,
		"relay_port":     relayEndpoint.Port,
		"relay_scheme":   relayEndpoint.Scheme,
		"relay_tls":      relayEndpoint.TLS,
		"relay_path":     "/api/v1/relay/" + session.ID,
	})
}

func (a *API) closeMySession(w http.ResponseWriter, r *http.Request) {
	accountID, deviceID, ok := a.requireSignedDeviceRequest(w, r)
	if !ok {
		return
	}

	if err := a.store.CloseSession(r.Context(), accountID, deviceID, r.PathValue("id")); err != nil {
		if err == store.ErrSessionNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
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
	if !a.internalAuthorized(w, r) {
		return
	}

	var req struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		AdvertiseAddr string `json:"advertise_addr"`
		RelayPort     int    `json:"relay_port"`
		DockerSocket  bool   `json:"docker_socket"`
		Binder        bool   `json:"binder"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}

	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.AdvertiseAddr) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id, name, and advertise_addr are required"})
		return
	}

	host, err := a.store.UpsertHostHeartbeat(r.Context(), store.HostHeartbeat{
		ID:            req.ID,
		Name:          req.Name,
		AdvertiseAddr: req.AdvertiseAddr,
		RelayPort:     req.RelayPort,
		DockerSocket:  req.DockerSocket,
		Binder:        req.Binder,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, host)
}

func (a *API) hostAssignments(w http.ResponseWriter, r *http.Request) {
	if !a.internalAuthorized(w, r) {
		return
	}

	items, err := a.store.ListAssignedRuntimes(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) runtimeBlobKey(w http.ResponseWriter, r *http.Request) {
	if !a.internalAuthorized(w, r) {
		return
	}

	runtimeID := r.PathValue("id")
	hostID := strings.TrimSpace(r.URL.Query().Get("host_id"))
	if hostID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "host_id is required"})
		return
	}
	if err := a.store.RequireRuntimeAssignedToHost(r.Context(), runtimeID, hostID); err != nil {
		if err == store.ErrRuntimeNotFound {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	key, expiresAt, ok := a.activeBlobKeys.get(runtimeID)
	if !ok {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "runtime active blob key is not available"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"blob_key_b64":        key,
		"blob_key_expires_at": expiresAt,
	})
}

func (a *API) sessionRelayTarget(w http.ResponseWriter, r *http.Request) {
	if !a.internalAuthorized(w, r) {
		return
	}

	relayToken := strings.TrimSpace(r.Header.Get("X-Virtdroid-Relay-Token"))
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
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, target)
}

func (a *API) runtimeStatusUpdate(w http.ResponseWriter, r *http.Request) {
	if !a.internalAuthorized(w, r) {
		return
	}

	var req struct {
		HostID             string     `json:"host_id"`
		Status             string     `json:"status"`
		ConnectionStatus   string     `json:"connection_status"`
		ContainerName      *string    `json:"container_name"`
		ADBPort            *int       `json:"adb_port"`
		LastError          *string    `json:"last_error"`
		BlobLastSnapshotAt *time.Time `json:"blob_last_snapshot_at"`
		ClearWipeRequested bool       `json:"clear_wipe_requested"`
		ActivePersonaJSON  *string    `json:"active_persona_json"`
		ClearActivePersona bool       `json:"clear_active_persona"`
		BlobStoreKind      *string    `json:"blob_store_kind"`
		BlobManifestJSON   *string    `json:"blob_manifest_json"`
		ClearBlobManifest  bool       `json:"clear_blob_manifest"`
		Deleted            bool       `json:"deleted"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}

	if err := a.store.UpdateRuntimeObservation(r.Context(), r.PathValue("id"), store.RuntimeObservation{
		HostID:             strings.TrimSpace(req.HostID),
		Status:             strings.TrimSpace(req.Status),
		ConnectionStatus:   strings.TrimSpace(req.ConnectionStatus),
		ContainerName:      req.ContainerName,
		ADBPort:            req.ADBPort,
		LastError:          req.LastError,
		BlobLastSnapshotAt: req.BlobLastSnapshotAt,
		ClearWipeRequested: req.ClearWipeRequested,
		ActivePersonaJSON:  req.ActivePersonaJSON,
		ClearActivePersona: req.ClearActivePersona,
		BlobStoreKind:      req.BlobStoreKind,
		BlobManifestJSON:   req.BlobManifestJSON,
		ClearBlobManifest:  req.ClearBlobManifest,
		Deleted:            req.Deleted,
	}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) runtimeLogAppend(w http.ResponseWriter, r *http.Request) {
	if !a.internalAuthorized(w, r) {
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

	if err := a.store.AppendRuntimeLog(r.Context(), r.PathValue("id"), req.Source, req.Level, req.Message); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) requireSignedDeviceRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	accountID := strings.TrimSpace(r.Header.Get("X-Virtdroid-Account-ID"))
	deviceID := strings.TrimSpace(r.Header.Get("X-Virtdroid-Device-ID"))
	timestampRaw := strings.TrimSpace(r.Header.Get("X-Virtdroid-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Virtdroid-Nonce"))
	bodyHash := strings.TrimSpace(r.Header.Get("X-Virtdroid-Body-SHA256"))
	signatureRaw := strings.TrimSpace(r.Header.Get("X-Virtdroid-Signature"))
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
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
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return "", "", false
	}

	return accountID, deviceID, true
}

func signedRequestCanonical(method, requestURI, accountID, deviceID, timestamp, nonce, bodyHash string) string {
	return strings.Join([]string{
		"VIRTDROID-DEVICE-SIGNATURE-V1",
		strings.ToUpper(method),
		requestURI,
		accountID,
		deviceID,
		timestamp,
		nonce,
		bodyHash,
	}, "\n")
}

func (a *API) internalAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if a.cfg.NodeSharedSecret == "" && a.cfg.AppEnv != "development" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "node shared secret is not configured"})
		return false
	}
	if a.cfg.NodeSharedSecret != "" && r.Header.Get("X-Virtdroid-Node-Secret") != a.cfg.NodeSharedSecret {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid node secret"})
		return false
	}
	return true
}

func (a *API) prepareViewer(ctx context.Context, advertiseAddr string, relayPort int, runtimeID string, maxSize, bitRate int) error {
	if relayPort <= 0 {
		relayPort = 8090
	}
	body, err := json.Marshal(map[string]any{
		"runtime_id": runtimeID,
		"max_size":   maxSize,
		"bit_rate":   bitRate,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("http://%s:%d/api/v1/internal/viewer/prepare", advertiseAddr, relayPort),
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.NodeSharedSecret != "" {
		req.Header.Set("X-Virtdroid-Node-Secret", a.cfg.NodeSharedSecret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("viewer prepare failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
