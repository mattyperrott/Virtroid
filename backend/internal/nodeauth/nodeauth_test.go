package nodeauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
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

func TestNormalizePublicKeyCanonicalizesEncodingAndFingerprintsDER(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	base64Material, err := PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("PublicKeyMaterial: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	pemMaterial := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	canonicalBase64, base64Fingerprint, err := NormalizePublicKey(base64Material)
	if err != nil {
		t.Fatalf("NormalizePublicKey(base64): %v", err)
	}
	canonicalPEM, pemFingerprint, err := NormalizePublicKey(pemMaterial)
	if err != nil {
		t.Fatalf("NormalizePublicKey(PEM): %v", err)
	}
	if canonicalBase64 != canonicalPEM || canonicalBase64 != base64Material {
		t.Fatalf("canonical forms differ: base64=%q pem=%q", canonicalBase64, canonicalPEM)
	}
	if base64Fingerprint != pemFingerprint || len(base64Fingerprint) != 64 {
		t.Fatalf("fingerprints differ or are malformed: base64=%q pem=%q", base64Fingerprint, pemFingerprint)
	}
}

func TestNormalizePublicKeyRejectsNonP256Key(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	material, err := PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("PublicKeyMaterial: %v", err)
	}
	if _, _, err := NormalizePublicKey(material); err == nil {
		t.Fatal("NormalizePublicKey accepted a non-P-256 key")
	}
}

func TestLoadPrivateKeyRejectsNonP256PKCS8AndSEC1Keys(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	sec1DER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}

	for name, material := range map[string]string{
		"PKCS8": base64.StdEncoding.EncodeToString(pkcs8DER),
		"SEC1":  base64.StdEncoding.EncodeToString(sec1DER),
	} {
		t.Run(name, func(t *testing.T) {
			loaded, publicKey, err := LoadPrivateKey(material)
			if !errors.Is(err, ErrInvalidPrivateKey) {
				t.Fatalf("LoadPrivateKey error = %v, want %v", err, ErrInvalidPrivateKey)
			}
			if loaded != nil || publicKey != "" {
				t.Fatalf("LoadPrivateKey returned key material for non-P256 %s key", name)
			}
		})
	}
}

func TestLoadPrivateKeyAcceptsP256PKCS8AndSEC1Keys(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pkcs8DER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	sec1DER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}

	for name, material := range map[string]string{
		"PKCS8": base64.StdEncoding.EncodeToString(pkcs8DER),
		"SEC1":  base64.StdEncoding.EncodeToString(sec1DER),
	} {
		t.Run(name, func(t *testing.T) {
			loaded, publicKey, err := LoadPrivateKey(material)
			if err != nil {
				t.Fatalf("LoadPrivateKey: %v", err)
			}
			if loaded == nil || publicKey == "" {
				t.Fatalf("LoadPrivateKey did not return P-256 %s key material", name)
			}
		})
	}
}
