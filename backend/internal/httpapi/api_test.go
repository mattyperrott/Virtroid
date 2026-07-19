package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

func TestSensitiveAPIResponsesAreNonCacheableAndNosniff(t *testing.T) {
	handler := New(config.ServerConfig{AppEnv: "production"}, nil)
	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		wantStore string
	}{
		{name: "bootstrap", method: http.MethodPost, path: "/api/v1/bootstrap", body: `{`, wantStore: "no-store"},
		{name: "private", method: http.MethodGet, path: "/api/v1/me/runtimes", wantStore: "no-store"},
		{name: "internal", method: http.MethodGet, path: "/api/v1/internal/hosts/node-1/assignments", wantStore: "no-store"},
		{name: "public metadata", method: http.MethodGet, path: "/api/v1/meta"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)

			if got := resp.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if got := resp.Header().Get("Cache-Control"); got != tt.wantStore {
				t.Fatalf("Cache-Control = %q, want %q", got, tt.wantStore)
			}
		})
	}
}

func TestSafeLogValueEscapesControlsAndBoundsLength(t *testing.T) {
	got := safeLogValue("node\r\nforged-entry\x1b[31m" + strings.Repeat("x", 2000))
	if strings.ContainsAny(got, "\r\n\x1b") {
		t.Fatalf("safeLogValue retained a raw control character: %q", got)
	}
	for _, escaped := range []string{`\r`, `\n`, `\x1b`} {
		if !strings.Contains(got, escaped) {
			t.Fatalf("safeLogValue = %q, want visible escape %q", got, escaped)
		}
	}
	if len(got) > 1100 {
		t.Fatalf("safeLogValue length = %d, want bounded output", len(got))
	}
}

func TestAPIRequestBodyLimitRejectsDeclaredOversizeBeforeHandler(t *testing.T) {
	called := false
	handler := withJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/me/runtimes",
		strings.NewReader(strings.Repeat("x", int(maxAPIRequestBodyBytes)+1)),
	)
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if called {
		t.Fatal("oversized request reached route handler")
	}
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusRequestEntityTooLarge)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "request_body_too_large" {
		t.Fatalf("payload = %#v, want stable request-size code", payload)
	}
}

func TestIdentityPasswordRotationFailsClosedBeforeStoreMutation(t *testing.T) {
	// A nil store makes any accidental authentication, verifier lookup, or
	// mutation call panic. The route must reject before touching persistence.
	handler := New(config.ServerConfig{AppEnv: "production"}, nil)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/me/identity/change-password",
		strings.NewReader(`{"blob_key_verifier":"replacement","current_blob_key_verifier":"current"}`),
	)
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNotImplemented)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "identity_password_rotation_unavailable" {
		t.Fatalf("payload = %#v, want stable unavailable code", payload)
	}
	if !strings.Contains(payload["error"], "transactionally rewrapped") {
		t.Fatalf("error = %q, want snapshot rewrap explanation", payload["error"])
	}
	if resp.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", resp.Header().Get("Cache-Control"))
	}
}

func TestAccountStorageMutationFailsClosedBeforeStoreMutation(t *testing.T) {
	// A nil store makes any accidental authorization or persistence access
	// panic. User-managed funding remains unavailable until ownership is real.
	handler := New(config.ServerConfig{AppEnv: "production"}, nil)
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/me/storage",
		strings.NewReader(`{"provider":"sia-renterd","funding_model":"user-funded","wallet_address":"attacker"}`),
	)
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNotImplemented)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "account_storage_mutation_unavailable" {
		t.Fatalf("payload = %#v, want stable unavailable code", payload)
	}
	if !strings.Contains(payload["error"], "disabled") {
		t.Fatalf("error = %q, want disabled explanation", payload["error"])
	}
}

func TestClientVisibleAccountStorageRedactsPaymentAddresses(t *testing.T) {
	wallet := "legacy-user-wallet"
	funding := "untrusted-node-wallet"
	storage := clientVisibleAccountStorage(store.AccountStorage{
		Provider:       "sia-renterd",
		FundingModel:   "user-funded",
		WalletAddress:  &wallet,
		FundingAddress: &funding,
	})

	if storage.WalletAddress != nil || storage.FundingAddress != nil {
		t.Fatalf("client storage exposed payment address: %+v", storage)
	}
	if storage.FundingModel != "operator-pooled" {
		t.Fatalf("funding model = %q, want operator-pooled", storage.FundingModel)
	}
}

func TestBootstrapProductionFailsClosedWhenDisabled(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv: "production",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(`{`))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("bootstrap status = %d, want %d", resp.Code, http.StatusServiceUnavailable)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "bootstrap_disabled" {
		t.Fatalf("payload = %#v, want stable bootstrap-disabled code", payload)
	}
}

func TestBootstrapRateLimitsBeforeStore(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv:                      "production",
		BootstrapEnabled:            true,
		BootstrapRateLimitPerMinute: 1,
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(`{`))
	req.RemoteAddr = "203.0.113.7:42312"
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("first bootstrap status = %d, want %d from nil-store-safe invalid input", resp.Code, http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(`{`))
	req.RemoteAddr = "203.0.113.7:42313"
	resp = httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("second bootstrap status = %d, want %d", resp.Code, http.StatusTooManyRequests)
	}
	if resp.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited bootstrap did not set Retry-After")
	}
}

func TestBootstrapClientKeyIgnoresForwardedHeadersByDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(`{}`))
	req.RemoteAddr = "10.0.0.5:42312"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	if got := bootstrapClientKey(req, false); got != "10.0.0.5" {
		t.Fatalf("bootstrapClientKey without trusted proxy = %q, want remote host only", got)
	}
	if got := bootstrapClientKey(req, true); got != "10.0.0.5|203.0.113.99" {
		t.Fatalf("bootstrapClientKey with trusted proxy = %q, want remote and forwarded host", got)
	}
}

func TestBootstrapRejectsOversizedBodyBeforeStore(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv:                      "production",
		BootstrapEnabled:            true,
		BootstrapRateLimitPerMinute: 10,
		BootstrapMaxBodyBytes:       8,
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(`{"device_name":"too-large"}`))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized bootstrap status = %d, want %d", resp.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestValidateNodeAdvertiseAddrRequiresProductionAllowlist(t *testing.T) {
	_, err := validateNodeAdvertiseAddr("virtnoded", nil, "production")
	if !errors.Is(err, errNodeAdvertiseAllowlistUnavailable) {
		t.Fatalf("validateNodeAdvertiseAddr error = %v, want %v", err, errNodeAdvertiseAllowlistUnavailable)
	}
}

func TestValidateNodeAdvertiseAddrAcceptsOnlyConfiguredHosts(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		allowed []string
		want    string
		wantErr bool
	}{
		{name: "service name", raw: "VIRTNODED", allowed: []string{"virtnoded"}, want: "virtnoded"},
		{name: "normalized ip", raw: "2001:0db8::1", allowed: []string{"2001:db8::1"}, want: "2001:db8::1"},
		{name: "loopback not listed", raw: "127.0.0.1", allowed: []string{"virtnoded"}, wantErr: true},
		{name: "url rejected", raw: "http://virtnoded", allowed: []string{"virtnoded"}, wantErr: true},
		{name: "credentials rejected", raw: "root@virtnoded", allowed: []string{"virtnoded"}, wantErr: true},
		{name: "port rejected", raw: "virtnoded:8090", allowed: []string{"virtnoded"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateNodeAdvertiseAddr(tt.raw, tt.allowed, "production")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("validateNodeAdvertiseAddr(%q) = %q, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateNodeAdvertiseAddr(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("validateNodeAdvertiseAddr(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestValidateNodeAdvertiseAddrDevelopmentAllowsSyntacticHost(t *testing.T) {
	got, err := validateNodeAdvertiseAddr("node.dev.internal", nil, "development")
	if err != nil {
		t.Fatalf("validateNodeAdvertiseAddr: %v", err)
	}
	if got != "node.dev.internal" {
		t.Fatalf("validateNodeAdvertiseAddr = %q, want node.dev.internal", got)
	}
}

func TestInternalRoutesRejectLegacySharedSecretWithoutNodeSignature(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv:           "production",
		NodeSharedSecret: "legacy-shared-secret",
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/hosts/victim-host/assignments", nil)
	req.Header.Set("X-Virtroid-Node-Secret", "legacy-shared-secret")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("internal route status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
}

func TestActiveBlobKeyVaultExpires(t *testing.T) {
	vault := newActiveBlobKeyVault()
	expiresAt := vault.putLease(activeBlobKeyLease{
		AccountID: "account-1",
		RuntimeID: "runtime-1",
		HostID:    "host-1",
		Operation: "start",
		LeaseID:   "lease-1",
	})
	if !expiresAt.After(time.Now().UTC()) {
		t.Fatal("vault returned non-future expiry")
	}
	if expiresAt.After(time.Now().UTC().Add(2*time.Minute + 5*time.Second)) {
		t.Fatalf("vault expiry = %s, want roughly two minutes", expiresAt)
	}

	envelope := testBlobKeyEnvelope("runtime-1", "host-1", "start", "lease-1")
	if _, err := vault.activate(activeBlobKeyHandoff{
		AccountID:       "account-1",
		RuntimeID:       "runtime-1",
		HostID:          "host-1",
		Operation:       "start",
		LeaseID:         "lease-1",
		Envelope:        envelope,
		BlobKeyVerifier: "verifier-1",
	}); err != nil {
		t.Fatalf("activate envelope: %v", err)
	}

	gotEnvelope, gotVerifier, _, ok := vault.get("runtime-1", "host-1")
	if !ok || gotEnvelope.Ciphertext != envelope.Ciphertext || gotVerifier != "verifier-1" {
		t.Fatalf("vault get = %+v, %q, %v; want encrypted envelope and verifier", gotEnvelope, gotVerifier, ok)
	}
	if _, _, _, ok := vault.get("runtime-1", "host-2"); ok {
		t.Fatal("vault returned envelope for the wrong host")
	}

	vault.mu.Lock()
	vault.keys["runtime-1"] = activeBlobKeyEntry{
		accountID:       "account-1",
		runtimeID:       "runtime-1",
		hostID:          "host-1",
		operation:       "start",
		leaseID:         "lease-1",
		envelope:        envelope,
		blobKeyVerifier: "verifier-1",
		hasEnvelope:     true,
		expiresAt:       time.Now().UTC().Add(-time.Second),
	}
	vault.mu.Unlock()

	if _, _, _, ok := vault.get("runtime-1", "host-1"); ok {
		t.Fatal("vault returned expired active blob key envelope")
	}
}

func TestActiveBlobKeyVaultClearAccount(t *testing.T) {
	durableStore := &fakeDurableBlobKeyHandoffStore{}
	vault := newActiveBlobKeyVault(durableStore)
	vault.putLease(activeBlobKeyLease{
		AccountID: "account-1",
		RuntimeID: "runtime-1",
		HostID:    "host-1",
		Operation: "session",
		LeaseID:   "lease-1",
	})
	vault.putLease(activeBlobKeyLease{
		AccountID: "account-2",
		RuntimeID: "runtime-2",
		HostID:    "host-1",
		Operation: "session",
		LeaseID:   "lease-2",
	})
	envelope := testBlobKeyEnvelope("runtime-2", "host-1", "session", "lease-2")
	if _, err := vault.activate(activeBlobKeyHandoff{
		AccountID:       "account-2",
		RuntimeID:       "runtime-2",
		HostID:          "host-1",
		Operation:       "session",
		LeaseID:         "lease-2",
		Envelope:        envelope,
		BlobKeyVerifier: "verifier-2",
	}); err != nil {
		t.Fatalf("activate envelope: %v", err)
	}

	vault.clearAccount("account-1")
	if len(durableStore.clearedAccounts) != 1 || durableStore.clearedAccounts[0] != "account-1" {
		t.Fatalf("durable account clears = %v, want account-1", durableStore.clearedAccounts)
	}

	if _, _, _, ok := vault.get("runtime-1", "host-1"); ok {
		t.Fatal("vault returned an envelope for the erased account")
	}
	gotEnvelope, gotVerifier, _, ok := vault.get("runtime-2", "host-1")
	if !ok || gotEnvelope.Ciphertext != envelope.Ciphertext || gotVerifier != "verifier-2" {
		t.Fatalf("vault get for other account = %+v, %q, %v; want encrypted envelope and verifier", gotEnvelope, gotVerifier, ok)
	}
}

func TestActiveBlobKeyVaultClearRuntimeRemovesDurableHandoff(t *testing.T) {
	durableStore := &fakeDurableBlobKeyHandoffStore{}
	vault := newActiveBlobKeyVault(durableStore)
	vault.putLease(activeBlobKeyLease{
		AccountID: "account-1",
		RuntimeID: "runtime-1",
		HostID:    "host-1",
		Operation: "cleanup",
		LeaseID:   "lease-1",
	})

	vault.clear("runtime-1")

	if len(durableStore.clearedRuntimes) != 1 || durableStore.clearedRuntimes[0] != "runtime-1" {
		t.Fatalf("durable runtime clears = %v, want runtime-1", durableStore.clearedRuntimes)
	}
	if _, _, _, ok := vault.get("runtime-1", "host-1"); ok {
		t.Fatal("vault returned an envelope after runtime cleanup")
	}
}

type fakeDurableBlobKeyHandoffStore struct {
	clearedRuntimes []string
	clearedAccounts []string
}

func (s *fakeDurableBlobKeyHandoffStore) ClearRuntimeBlobKeyHandoff(_ context.Context, runtimeID string) error {
	s.clearedRuntimes = append(s.clearedRuntimes, runtimeID)
	return nil
}

func (s *fakeDurableBlobKeyHandoffStore) ClearAccountBlobKeyHandoffs(_ context.Context, accountID string) error {
	s.clearedAccounts = append(s.clearedAccounts, accountID)
	return nil
}

func TestActiveBlobKeyVaultRejectsEnvelopeWithoutVerifier(t *testing.T) {
	vault := newActiveBlobKeyVault()
	vault.putLease(activeBlobKeyLease{
		AccountID: "account-1",
		RuntimeID: "runtime-1",
		HostID:    "host-1",
		Operation: "start",
		LeaseID:   "lease-1",
	})

	_, err := vault.activate(activeBlobKeyHandoff{
		AccountID: "account-1",
		RuntimeID: "runtime-1",
		HostID:    "host-1",
		Operation: "start",
		LeaseID:   "lease-1",
		Envelope:  testBlobKeyEnvelope("runtime-1", "host-1", "start", "lease-1"),
	})
	if err == nil {
		t.Fatal("activate accepted a blob-key envelope without the expected verifier")
	}
}

func testBlobKeyEnvelope(runtimeID, hostID, operation, leaseID string) blobKeyEnvelope {
	return blobKeyEnvelope{
		Version:            1,
		Algorithm:          blobKeyEnvelopeAlgorithm,
		LeaseID:            leaseID,
		Operation:          operation,
		RuntimeID:          runtimeID,
		HostID:             hostID,
		EphemeralPublicKey: "ephemeral-public-key",
		IV:                 "iv",
		Ciphertext:         "ciphertext",
	}
}

func TestRuntimeMutationErrorsIncludeStableCodes(t *testing.T) {
	resp := httptest.NewRecorder()

	writeRuntimeMutationError(resp, store.ErrRuntimeStartQuota)

	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusTooManyRequests)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != store.RuntimeStartQuotaExceededCode {
		t.Fatalf("code = %q, want %q", payload["code"], store.RuntimeStartQuotaExceededCode)
	}
}

func TestRuntimeMutationUnexpectedErrorIsNotExposed(t *testing.T) {
	resp := httptest.NewRecorder()

	writeRuntimeMutationError(resp, errors.New("postgres://secret@database/internal table detail"))

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusInternalServerError)
	}
	if strings.Contains(resp.Body.String(), "secret") || strings.Contains(resp.Body.String(), "table detail") {
		t.Fatalf("response exposed internal error detail: %s", resp.Body.String())
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "runtime_mutation_failed" || payload["error"] != "internal server error" {
		t.Fatalf("payload = %#v, want stable generic internal error", payload)
	}
}
