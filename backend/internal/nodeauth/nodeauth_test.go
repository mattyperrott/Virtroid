package nodeauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
)

func TestSignAndVerifyNodeRequest(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	publicKey, err := PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("PublicKeyMaterial: %v", err)
	}

	body := []byte(`{"ok":true}`)
	bodyHash, signature, err := Sign(privateKey, "POST", "/api/v1/internal/hosts/heartbeat", "host-1", "1777777777", "nonce-1", body)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(publicKey, "POST", "/api/v1/internal/hosts/heartbeat", "host-1", "1777777777", "nonce-1", bodyHash, signature); err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if err := Verify(publicKey, "POST", "/api/v1/internal/hosts/host-2/assignments", "host-1", "1777777777", "nonce-1", bodyHash, signature); err == nil {
		t.Fatal("Verify accepted a signature for a different request URI")
	}
	if err := Verify(publicKey, "POST", "/api/v1/internal/hosts/heartbeat", "host-2", "1777777777", "nonce-1", bodyHash, signature); err == nil {
		t.Fatal("Verify accepted a signature for a different node id")
	}
}
