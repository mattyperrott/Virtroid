package httpapi

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/callbackauth"
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
		{name: "account recovery", method: http.MethodPost, path: "/api/v1/recovery/challenge", body: `{`, wantStore: "no-store"},
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

func TestRecoverySignatureBindsReplacementDeviceChallenge(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate recovery key: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal recovery key: %v", err)
	}
	challenge := store.AccountRecoveryChallenge{
		ID:              "11111111-1111-1111-1111-111111111111",
		AccountID:       "22222222-2222-2222-2222-222222222222",
		DeviceID:        "33333333-3333-3333-3333-333333333333",
		DeviceName:      "Replacement phone",
		DevicePublicKey: "replacement-device-public-key",
		Nonce:           "challenge-nonce",
	}
	digest := sha256.Sum256([]byte(recoverySignatureCanonical(challenge)))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		t.Fatalf("sign recovery challenge: %v", err)
	}
	publicKey := base64.StdEncoding.EncodeToString(publicKeyDER)
	signatureEncoded := base64.RawURLEncoding.EncodeToString(signature)

	if !verifyRecoverySignature(publicKey, challenge, signatureEncoded) {
		t.Fatal("valid recovery signature was rejected")
	}
	challenge.DeviceID = "44444444-4444-4444-4444-444444444444"
	if verifyRecoverySignature(publicKey, challenge, signatureEncoded) {
		t.Fatal("recovery signature remained valid after replacement device was changed")
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

func TestAPIRequestBodyLimitAllowsOnlyBoundedMediaRoutes(t *testing.T) {
	if got := apiRequestBodyLimit("/api/v1/me/runtimes/id/files"); got != maxRuntimeFileImportBytes {
		t.Fatalf("file limit = %d", got)
	}
	if got := apiRequestBodyLimit("/api/v1/me/runtimes/id/photos"); got != maxRuntimePhotoImportBytes {
		t.Fatalf("photo limit = %d", got)
	}
	if got := apiRequestBodyLimit("/api/v1/me/runtimes/id/videos"); got != maxRuntimeVideoImportBytes {
		t.Fatalf("video limit = %d", got)
	}
	if got := apiRequestBodyLimit("/api/v1/me/runtimes/id/session"); got != maxAPIRequestBodyBytes {
		t.Fatalf("default limit = %d", got)
	}
}

func TestValidateRuntimePhotoRequiresBoundedJPEG(t *testing.T) {
	var photo bytes.Buffer
	if err := jpeg.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimePhoto("capture.jpg", photo.Bytes()); err != nil {
		t.Fatalf("valid JPEG rejected: %v", err)
	}
	if err := validateRuntimePhoto("capture.png", photo.Bytes()); err == nil {
		t.Fatal("JPEG with a non-JPEG name was accepted")
	}
	if err := validateRuntimePhoto("capture.jpg", []byte("not a jpeg")); err == nil {
		t.Fatal("invalid JPEG was accepted")
	}
}

func TestValidateRuntimeVideoRequiresBoundedMP4(t *testing.T) {
	video := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	if err := validateRuntimeVideo("capture.mp4", video); err != nil {
		t.Fatalf("valid MP4 rejected: %v", err)
	}
	if err := validateRuntimeVideo("capture.mov", video); err == nil {
		t.Fatal("MP4 with a non-MP4 name was accepted")
	}
	if err := validateRuntimeVideo("capture.mp4", []byte("not an mp4")); err == nil {
		t.Fatal("invalid MP4 was accepted")
	}
}

func TestDecodeRuntimeMediaNameRejectsPathTraversal(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("../payload.txt"))
	if _, err := decodeRuntimeMediaName(encoded); err == nil {
		t.Fatal("accepted a path-like media name")
	}
	encoded = base64.RawURLEncoding.EncodeToString([]byte("report.pdf"))
	got, err := decodeRuntimeMediaName(encoded)
	if err != nil || got != "report.pdf" {
		t.Fatalf("decodeRuntimeMediaName = %q, %v", got, err)
	}
}

func TestRecoveryMiddlewareReturnsStableInternalError(t *testing.T) {
	handler := withRecovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("sensitive panic detail")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/runtimes", nil)
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusInternalServerError)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "internal_server_error" || strings.Contains(resp.Body.String(), "sensitive") {
		t.Fatalf("payload = %#v, want stable redacted internal error", payload)
	}
}

func TestBootstrapRateLimiterPrunesExpiredEntriesAndCapsClients(t *testing.T) {
	limiter := newBootstrapRateLimiter(5, time.Hour)
	limiter.maxClients = 2
	limiter.clients["expired"] = bootstrapRateLimitEntry{
		count: 1,
		reset: time.Now().Add(-time.Second),
	}

	if allowed, _ := limiter.allow("client-a"); !allowed {
		t.Fatal("first client was unexpectedly rejected")
	}
	if _, found := limiter.clients["expired"]; found {
		t.Fatal("expired client was not pruned")
	}
	if allowed, _ := limiter.allow("client-b"); !allowed {
		t.Fatal("second client was unexpectedly rejected")
	}
	if allowed, _ := limiter.allow("client-c"); allowed {
		t.Fatal("new client was accepted after the bounded limiter reached capacity")
	}
	if len(limiter.clients) != limiter.maxClients {
		t.Fatalf("client map size = %d, want bounded size %d", len(limiter.clients), limiter.maxClients)
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

func TestPublicBootstrapRequiresSigningKeyProof(t *testing.T) {
	handler := New(config.ServerConfig{
		AppEnv:                      "production",
		BootstrapEnabled:            true,
		BootstrapRateLimitPerMinute: 5,
	}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(`{
		"account_id":"11111111-1111-1111-1111-111111111111",
		"device_id":"22222222-2222-2222-2222-222222222222",
		"device_name":"Pixel",
		"public_key":"not-a-key"
	}`))
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("bootstrap status = %d, want %d", resp.Code, http.StatusUnauthorized)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "bootstrap_proof_invalid" {
		t.Fatalf("payload = %#v, want stable proof failure", payload)
	}
}

func TestProductionBootstrapRequiresInviteAfterValidDeviceProof(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	publicKey := base64.StdEncoding.EncodeToString(publicKeyDER)
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	body := []byte(fmt.Sprintf(
		`{"account_id":%q,"device_id":%q,"device_name":"Pixel","public_key":%q}`,
		accountID,
		deviceID,
		publicKey,
	))
	req := newSignedBootstrapRequest(t, body, accountID, deviceID, privateKey)
	resp := httptest.NewRecorder()
	New(config.ServerConfig{
		AppEnv:                      "production",
		BootstrapEnabled:            true,
		BootstrapAutoIssueInvite:    false,
		BootstrapRateLimitPerMinute: 5,
	}, nil).ServeHTTP(resp, req)

	if resp.Code != http.StatusForbidden {
		t.Fatalf("bootstrap status = %d body=%s, want %d", resp.Code, resp.Body.String(), http.StatusForbidden)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != "bootstrap_invite_required" {
		t.Fatalf("payload = %#v, want stable invite-required code", payload)
	}
}

func TestVerifyBootstrapProofAcceptsExactSignedBodyAndRejectsTampering(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	publicKey := base64.StdEncoding.EncodeToString(publicKeyDER)
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	body := []byte(`{"account_id":"11111111-1111-1111-1111-111111111111"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(string(body)))
	timestamp := time.Now().Unix()
	timestampRaw := strconv.FormatInt(timestamp, 10)
	nonce := "33333333-3333-4333-8333-333333333333"
	bodyDigest := sha256.Sum256(body)
	bodyHash := base64.RawURLEncoding.EncodeToString(bodyDigest[:])
	canonical := signedRequestCanonical(http.MethodPost, "/api/v1/bootstrap", accountID, deviceID, timestampRaw, nonce, bodyHash)
	canonicalDigest := sha256.Sum256([]byte(canonical))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, canonicalDigest[:])
	if err != nil {
		t.Fatalf("SignASN1: %v", err)
	}
	req.Header.Set("X-Virtroid-Account-ID", accountID)
	req.Header.Set("X-Virtroid-Device-ID", deviceID)
	req.Header.Set("X-Virtroid-Timestamp", timestampRaw)
	req.Header.Set("X-Virtroid-Nonce", nonce)
	req.Header.Set("X-Virtroid-Body-SHA256", bodyHash)
	req.Header.Set("X-Virtroid-Signature", base64.RawURLEncoding.EncodeToString(signature))

	if !verifyBootstrapProof(req, body, accountID, deviceID, publicKey) {
		t.Fatal("verifyBootstrapProof rejected valid proof")
	}
	tampered := append([]byte(nil), body...)
	tampered[0] ^= 1
	if verifyBootstrapProof(req, tampered, accountID, deviceID, publicKey) {
		t.Fatal("verifyBootstrapProof accepted a tampered request body")
	}
}

func TestInviteGatedSignedBootstrapPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	publicKey := base64.StdEncoding.EncodeToString(publicKeyDER)
	accountID := uuid.NewString()
	deviceID := uuid.NewString()
	inviteToken := "integration-" + uuid.NewString()
	inviteTokenSHA256 := sha256.Sum256([]byte(inviteToken))
	if _, err := st.CreateBootstrapInvitation(
		ctx,
		inviteTokenSHA256[:],
		"HTTP integration test",
		"test-suite",
		time.Now().UTC().Add(5*time.Minute),
	); err != nil {
		t.Fatalf("create bootstrap invitation: %v", err)
	}
	defer func() {
		if err := st.DeleteAccount(context.Background(), accountID); err != nil && !errors.Is(err, store.ErrAccountNotFound) {
			t.Errorf("cleanup account: %v", err)
		}
	}()
	body := []byte(fmt.Sprintf(
		`{"account_id":%q,"device_id":%q,"device_name":"Invited client","public_key":%q,"invite_token":%q}`,
		accountID,
		deviceID,
		publicKey,
		inviteToken,
	))
	req := newSignedBootstrapRequest(t, body, accountID, deviceID, privateKey)
	resp := httptest.NewRecorder()
	New(config.ServerConfig{
		AppEnv:                      "production",
		BootstrapEnabled:            true,
		BootstrapRequireInvite:      true,
		BootstrapRateLimitPerMinute: 5,
	}, st).ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("invite-gated bootstrap status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload store.BootstrapResult
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode bootstrap response: %v", err)
	}
	if payload.Account.ID != accountID || payload.Device.ID != deviceID || payload.Runtime != nil {
		t.Fatalf("unexpected invite-gated bootstrap result: %+v", payload)
	}
	if _, err := st.BootstrapAccountWithInvitation(
		ctx,
		uuid.NewString(),
		uuid.NewString(),
		"Replay attempt",
		publicKey,
		inviteTokenSHA256[:],
		store.CreateRuntimeInput{},
	); !errors.Is(err, store.ErrBootstrapInviteInvalid) {
		t.Fatalf("reusing consumed invitation returned %v, want %v", err, store.ErrBootstrapInviteInvalid)
	}
}

func newSignedBootstrapRequest(
	t *testing.T,
	body []byte,
	accountID string,
	deviceID string,
	privateKey *ecdsa.PrivateKey,
) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(string(body)))
	timestampRaw := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.NewString()
	bodyDigest := sha256.Sum256(body)
	bodyHash := base64.RawURLEncoding.EncodeToString(bodyDigest[:])
	canonical := signedRequestCanonical(http.MethodPost, "/api/v1/bootstrap", accountID, deviceID, timestampRaw, nonce, bodyHash)
	canonicalDigest := sha256.Sum256([]byte(canonical))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, canonicalDigest[:])
	if err != nil {
		t.Fatalf("SignASN1: %v", err)
	}
	req.Header.Set("X-Virtroid-Account-ID", accountID)
	req.Header.Set("X-Virtroid-Device-ID", deviceID)
	req.Header.Set("X-Virtroid-Timestamp", timestampRaw)
	req.Header.Set("X-Virtroid-Nonce", nonce)
	req.Header.Set("X-Virtroid-Body-SHA256", bodyHash)
	req.Header.Set("X-Virtroid-Signature", base64.RawURLEncoding.EncodeToString(signature))
	return req
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

func TestPrepareViewerSendsSignedReconnectParametersAndReturnsFreshKey(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	publicKeyMaterial := base64.StdEncoding.EncodeToString(publicKeyDER)

	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/viewer/prepare" {
			t.Fatalf("request = %s %s, want viewer prepare POST", r.Method, r.URL.Path)
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Fatalf("read body: %v", readErr)
		}
		if verifyErr := callbackauth.Verify(
			publicKeyMaterial,
			r.Method,
			r.URL.RequestURI(),
			r.Header.Get(callbackauth.HeaderTimestamp),
			r.Header.Get(callbackauth.HeaderNonce),
			r.Header.Get(callbackauth.HeaderBodySHA256),
			r.Header.Get(callbackauth.HeaderSignature),
		); verifyErr != nil {
			t.Fatalf("verify callback signature: %v", verifyErr)
		}
		var request struct {
			RuntimeID string `json:"runtime_id"`
			MaxSize   int    `json:"max_size"`
			BitRate   int    `json:"bit_rate"`
		}
		if decodeErr := json.Unmarshal(body, &request); decodeErr != nil {
			t.Fatalf("decode body: %v", decodeErr)
		}
		if request.RuntimeID != "runtime-reconnect" || request.MaxSize != 1600 || request.BitRate != viewerReconnectBitRate {
			t.Fatalf("prepare request = %#v", request)
		}
		writeJSON(w, http.StatusOK, map[string]any{"viewer_public_key": "fresh-viewer-key"})
	}))
	defer node.Close()

	host, portRaw, err := net.SplitHostPort(node.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatalf("parse listener port: %v", err)
	}
	api := &API{cfg: config.ServerConfig{
		AppEnv:                         "production",
		NodeAllowedAdvertiseAddrs:      []string{host},
		ControlPlaneCallbackPrivateKey: base64.StdEncoding.EncodeToString(privateKeyDER),
	}}

	got, err := api.prepareViewer(context.Background(), host, port, "runtime-reconnect", 1600, viewerReconnectBitRate)
	if err != nil {
		t.Fatalf("prepareViewer: %v", err)
	}
	if got != "fresh-viewer-key" {
		t.Fatalf("viewer public key = %q, want fresh-viewer-key", got)
	}
}

func TestInternalRoutesRejectLegacySharedSecretWithoutNodeSignature(t *testing.T) {
	handler := New(config.ServerConfig{AppEnv: "production"}, nil)

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

func TestRuntimeNotReadyIsAControlledConflict(t *testing.T) {
	resp := httptest.NewRecorder()

	writeRuntimeMutationError(resp, store.ErrRuntimeNotReady)

	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusConflict)
	}
	var payload map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["code"] != store.RuntimeNotReadyCode {
		t.Fatalf("code = %q, want %q", payload["code"], store.RuntimeNotReadyCode)
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
