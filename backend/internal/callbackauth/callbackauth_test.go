package callbackauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"testing"

	"virtroid/backend/internal/nodeauth"
)

func TestSignedCallbackRoundTripAndContextBinding(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	publicKey, err := nodeauth.PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("PublicKeyMaterial: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://virtnoded:8090/api/v1/internal/viewer/prepare", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	body := []byte(`{"runtime_id":"runtime-1"}`)
	if err := ApplySignedHeaders(req, privateKey, body, "1700000000", "nonce-1"); err != nil {
		t.Fatalf("ApplySignedHeaders: %v", err)
	}
	if err := Verify(
		publicKey,
		req.Method,
		req.URL.RequestURI(),
		req.Header.Get(HeaderTimestamp),
		req.Header.Get(HeaderNonce),
		req.Header.Get(HeaderBodySHA256),
		req.Header.Get(HeaderSignature),
	); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := Verify(
		publicKey,
		req.Method,
		"/api/v1/internal/blob-key/verify",
		req.Header.Get(HeaderTimestamp),
		req.Header.Get(HeaderNonce),
		req.Header.Get(HeaderBodySHA256),
		req.Header.Get(HeaderSignature),
	); err == nil {
		t.Fatal("Verify accepted the signature for a different callback path")
	}
}
