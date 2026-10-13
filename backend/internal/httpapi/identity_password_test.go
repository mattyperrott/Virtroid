package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

func TestIdentityPasswordChangePostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	handler := New(config.ServerConfig{AppEnv: "production"}, st)

	device := newTestDevice(t)
	if _, err := st.BootstrapAccountWithIdentity(ctx, device.accountID, device.deviceID, "CI device", device.publicKey(t), store.CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer func() { _ = st.DeleteAccount(context.Background(), device.accountID) }()

	randomB64 := func(n int) string {
		raw := make([]byte, n)
		_, _ = rand.Read(raw)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	newRecoveryKey := func() (*ecdsa.PrivateKey, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return key, base64.StdEncoding.EncodeToString(der)
	}
	recoveryKey, recoveryPublic := newRecoveryKey()
	envelope := func(publicKey, verifier string) store.AccountRecoveryCredential {
		return store.AccountRecoveryCredential{
			AccountID: device.accountID, Version: 2, KDFAlgorithm: "PBKDF2_HMAC_SHA256_V2", KDFSalt: randomB64(32),
			KDFIterations: store.AccountRecoveryMinIterations, EnvelopeAlgorithm: "AES_GCM_256_V1", EnvelopeIV: randomB64(12),
			EnvelopeCiphertext: randomB64(160), RecoveryPublicKey: publicKey, BlobKeyVerifier: verifier,
		}
	}
	verifier := randomB64(32)
	original := envelope(recoveryPublic, verifier)
	body, _ := json.Marshal(original)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, device.request(t, http.MethodPost, "/api/v1/me/identity/recovery", body))
	if resp.Code != http.StatusCreated {
		t.Fatalf("register recovery = %d %s", resp.Code, resp.Body.String())
	}

	change := func(credential store.AccountRecoveryCredential, signer *ecdsa.PrivateKey) *httptest.ResponseRecorder {
		t.Helper()
		digest := sha256.Sum256([]byte(rewrapProofCanonical(device.deviceID, credential)))
		signature, err := ecdsa.SignASN1(rand.Reader, signer, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"credential": credential, "proof": base64.RawURLEncoding.EncodeToString(signature)})
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, device.request(t, http.MethodPost, "/api/v1/me/identity/change-password", body))
		return resp
	}

	// Signed by someone without the recovery key: refused.
	otherKey, _ := newRecoveryKey()
	if resp := change(envelope(recoveryPublic, verifier), otherKey); resp.Code != http.StatusForbidden {
		t.Fatalf("proof by another key = %d %s", resp.Code, resp.Body.String())
	}
	// A different account key or recovery key: refused even with a valid proof.
	if resp := change(envelope(recoveryPublic, randomB64(32)), recoveryKey); resp.Code != http.StatusForbidden {
		t.Fatalf("different account key = %d %s", resp.Code, resp.Body.String())
	}
	_, otherPublic := newRecoveryKey()
	if resp := change(envelope(otherPublic, verifier), recoveryKey); resp.Code != http.StatusForbidden {
		t.Fatalf("different recovery key = %d %s", resp.Code, resp.Body.String())
	}

	// The same keys under a new password: stored.
	rewrapped := envelope(recoveryPublic, verifier)
	if resp := change(rewrapped, recoveryKey); resp.Code != http.StatusOK {
		t.Fatalf("rewrap = %d %s", resp.Code, resp.Body.String())
	}
	stored, err := st.GetAccountRecoveryCredential(ctx, device.accountID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.KDFSalt != rewrapped.KDFSalt || stored.EnvelopeCiphertext != rewrapped.EnvelopeCiphertext ||
		stored.RecoveryPublicKey != recoveryPublic || stored.BlobKeyVerifier != verifier {
		t.Fatalf("stored credential = %+v", stored)
	}
	// The old envelope's signature does not carry over to another envelope.
	replay := envelope(recoveryPublic, verifier)
	digest := sha256.Sum256([]byte(rewrapProofCanonical(device.deviceID, rewrapped)))
	signature, _ := ecdsa.SignASN1(rand.Reader, recoveryKey, digest[:])
	replayBody, _ := json.Marshal(map[string]any{"credential": replay, "proof": base64.RawURLEncoding.EncodeToString(signature)})
	replayResp := httptest.NewRecorder()
	handler.ServeHTTP(replayResp, device.request(t, http.MethodPost, "/api/v1/me/identity/change-password", replayBody))
	if replayResp.Code != http.StatusForbidden {
		t.Fatalf("replayed proof = %d %s", replayResp.Code, replayResp.Body.String())
	}
}
