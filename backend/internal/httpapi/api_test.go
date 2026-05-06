package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"virtdroid/backend/internal/config"
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

func TestActiveBlobKeyVaultExpires(t *testing.T) {
	vault := newActiveBlobKeyVault()
	expiresAt := vault.put("runtime-1", "key-1")
	if !expiresAt.After(time.Now().UTC()) {
		t.Fatal("vault returned non-future expiry")
	}

	key, _, ok := vault.get("runtime-1")
	if !ok || key != "key-1" {
		t.Fatalf("vault get = %q, %v; want key-1, true", key, ok)
	}

	vault.mu.Lock()
	vault.keys["runtime-1"] = activeBlobKeyEntry{key: "key-1", expiresAt: time.Now().UTC().Add(-time.Second)}
	vault.mu.Unlock()

	if _, _, ok := vault.get("runtime-1"); ok {
		t.Fatal("vault returned expired active blob key")
	}
}
