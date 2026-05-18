package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

func TestBootstrapProductionAllowsPublicSignupWhenInviteGateDisabled(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv: "production",
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(`{`))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("bootstrap status = %d, want %d from nil-store-safe invalid input", resp.Code, http.StatusBadRequest)
	}
}

func TestBootstrapRateLimitsBeforeStore(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv:                      "production",
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
	vault := newActiveBlobKeyVault()
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

	if _, _, _, ok := vault.get("runtime-1", "host-1"); ok {
		t.Fatal("vault returned an envelope for the erased account")
	}
	gotEnvelope, gotVerifier, _, ok := vault.get("runtime-2", "host-1")
	if !ok || gotEnvelope.Ciphertext != envelope.Ciphertext || gotVerifier != "verifier-2" {
		t.Fatalf("vault get for other account = %+v, %q, %v; want encrypted envelope and verifier", gotEnvelope, gotVerifier, ok)
	}
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
