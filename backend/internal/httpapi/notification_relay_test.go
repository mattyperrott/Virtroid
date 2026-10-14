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
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

// relayRequest is signed the way the app's notification relay signs while
// the phone is locked: with its relay key, under the relay context.
func relayRequest(t *testing.T, d testDevice, relayKey *ecdsa.PrivateKey, signatureContext, method, target string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	bodyDigest := sha256.Sum256(body)
	bodyHash := base64.RawURLEncoding.EncodeToString(bodyDigest[:])
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.NewString()
	digest := sha256.Sum256([]byte(signedRequestCanonicalFor(signatureContext, method, req.URL.RequestURI(), d.accountID, d.deviceID, timestamp, nonce, bodyHash)))
	signature, err := ecdsa.SignASN1(rand.Reader, relayKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Virtroid-Account-ID", d.accountID)
	req.Header.Set("X-Virtroid-Device-ID", d.deviceID)
	req.Header.Set("X-Virtroid-Timestamp", timestamp)
	req.Header.Set("X-Virtroid-Nonce", nonce)
	req.Header.Set("X-Virtroid-Body-SHA256", bodyHash)
	req.Header.Set("X-Virtroid-Signature", base64.RawURLEncoding.EncodeToString(signature))
	req.Header.Set("X-Virtroid-Signing-Key", notificationRelaySigningKey)
	return req
}

func TestNotificationRelayKeyPostgresIntegration(t *testing.T) {
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

	relayKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relayDER, err := x509.MarshalPKIXPublicKey(&relayKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pushKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pushDER, err := x509.MarshalPKIXPublicKey(&pushKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	serve := func(req *http.Request) (int, string) {
		t.Helper()
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(resp.Body.Bytes(), &body)
		return resp.Code, body.Error
	}
	// An unknown delivery gets past authentication to "not found".
	ackPath := "/api/v1/me/notification-deliveries/" + uuid.NewString() + "/ack"
	const authenticated = "notification delivery not found"

	if code, msg := serve(relayRequest(t, device, relayKey, relaySignatureContext, http.MethodPost, ackPath, []byte("{}"))); msg == authenticated {
		t.Fatalf("relay key accepted before it was registered: %d %s", code, msg)
	}

	subscription, _ := json.Marshal(map[string]string{
		"device_id":             device.deviceID,
		"encryption_public_key": base64.RawURLEncoding.EncodeToString(pushDER),
		"relay_public_key":      base64.StdEncoding.EncodeToString(relayDER),
	})
	if code, msg := serve(device.request(t, http.MethodPut, "/api/v1/me/notification-subscription", subscription)); code != http.StatusOK {
		t.Fatalf("register subscription with relay key: %d %s", code, msg)
	}

	if code, msg := serve(relayRequest(t, device, relayKey, relaySignatureContext, http.MethodPost, ackPath, []byte("{}"))); msg != authenticated {
		t.Fatalf("relay-signed ack: %d %q, want %q", code, msg, authenticated)
	}
	// The device key still works there.
	if code, msg := serve(device.request(t, http.MethodPost, ackPath, []byte("{}"))); msg != authenticated {
		t.Fatalf("device-signed ack: %d %q, want %q", code, msg, authenticated)
	}
	// The relay key signs nothing else, and never under the device context.
	if code, _ := serve(relayRequest(t, device, relayKey, relaySignatureContext, http.MethodGet, "/api/v1/me/wallet", nil)); code != http.StatusUnauthorized {
		t.Fatalf("relay-signed wallet request: %d, want 401", code)
	}
	if code, _ := serve(relayRequest(t, device, relayKey, deviceSignatureContext, http.MethodPost, ackPath, []byte("{}"))); code != http.StatusUnauthorized {
		t.Fatalf("relay key under the device context: %d, want 401", code)
	}

	// A subscription without a relay key removes it.
	subscription, _ = json.Marshal(map[string]string{
		"device_id":             device.deviceID,
		"encryption_public_key": base64.RawURLEncoding.EncodeToString(pushDER),
	})
	if code, msg := serve(device.request(t, http.MethodPut, "/api/v1/me/notification-subscription", subscription)); code != http.StatusOK {
		t.Fatalf("register subscription without relay key: %d %s", code, msg)
	}
	if code, msg := serve(relayRequest(t, device, relayKey, relaySignatureContext, http.MethodPost, ackPath, []byte("{}"))); msg == authenticated {
		t.Fatalf("relay key accepted after it was replaced: %d %s", code, msg)
	}
}
