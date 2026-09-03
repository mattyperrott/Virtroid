package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestEncryptEnvelopeDecryptsWithRecipientKey(t *testing.T) {
	recipient, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	materialDER, err := x509.MarshalPKIXPublicKey(&recipient.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	material := base64.RawURLEncoding.EncodeToString(materialDER)
	payload := []byte(`{"event_id":"metadata-only"}`)

	envelopeJSON, err := EncryptEnvelope(material, payload)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Version            int    `json:"v"`
		EphemeralPublicKey string `json:"ephemeral_public_key"`
		Nonce              string `json:"nonce"`
		Ciphertext         string `json:"ciphertext"`
	}
	if err := json.Unmarshal([]byte(envelopeJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != 1 {
		t.Fatalf("envelope version = %d", envelope.Version)
	}

	ephemeralDER, err := base64.RawURLEncoding.DecodeString(envelope.EphemeralPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKIXPublicKey(ephemeralDER)
	if err != nil {
		t.Fatal(err)
	}
	ephemeralECDSA, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		t.Fatal("ephemeral key is not ECDSA")
	}
	ephemeralECDH, err := ephemeralECDSA.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	recipientECDH, err := recipient.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	shared, err := recipientECDH.ECDH(ephemeralECDH)
	if err != nil {
		t.Fatal(err)
	}
	key, err := hkdf.Key(sha256.New, shared, nil, pushEnvelopeContext, 32)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(pushEnvelopeContext))
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != string(payload) {
		t.Fatalf("plaintext = %q", plaintext)
	}
}

func TestValidateEncryptionPublicKeyRejectsWrongCurve(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateEncryptionPublicKey(base64.RawURLEncoding.EncodeToString(der)); err == nil {
		t.Fatal("accepted a non-P-256 encryption key")
	}
}
