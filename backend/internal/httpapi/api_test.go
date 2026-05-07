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
	expiresAt := vault.put(activeBlobKeyHandoff{
		AccountID: "account-1",
		RuntimeID: "runtime-1",
		HostID:    "host-1",
		Operation: "start",
		Key:       "key-1",
	})
	if !expiresAt.After(time.Now().UTC()) {
		t.Fatal("vault returned non-future expiry")
	}

	key, _, ok := vault.get("runtime-1", "host-1")
	if !ok || key != "key-1" {
		t.Fatalf("vault get = %q, %v; want key-1, true", key, ok)
	}
	if _, _, ok := vault.get("runtime-1", "host-2"); ok {
		t.Fatal("vault returned key for the wrong host")
	}

	vault.mu.Lock()
	vault.keys["runtime-1"] = activeBlobKeyEntry{
		accountID: "account-1",
		runtimeID: "runtime-1",
		hostID:    "host-1",
		operation: "start",
		key:       "key-1",
		expiresAt: time.Now().UTC().Add(-time.Second),
	}
	vault.mu.Unlock()

	if _, _, ok := vault.get("runtime-1", "host-1"); ok {
		t.Fatal("vault returned expired active blob key")
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
