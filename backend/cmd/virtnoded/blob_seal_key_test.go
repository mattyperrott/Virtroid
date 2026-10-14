package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"
)

// sealedTestEnvelope seals blobKey to a seal key the way the phone does.
func sealedTestEnvelope(t *testing.T, sealPublicKey string, blobKey []byte) blobKeyEnvelopePayload {
	t.Helper()
	der, err := base64.StdEncoding.DecodeString(sealPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := parsed.(*ecdsa.PublicKey).ECDH()
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ephemeral.ECDH(seal)
	if err != nil {
		t.Fatal(err)
	}
	envelope := blobKeyEnvelopePayload{
		Version:   1,
		Algorithm: blobKeyEnvelopeAlgorithmSealed,
		LeaseID:   "lease-1",
		Operation: "start",
		RuntimeID: "runtime-1",
		HostID:    "host-1",
	}
	aad := blobKeyEnvelopeAAD(envelope)
	salt := sha256.Sum256(aad)
	block, err := aes.NewCipher(hkdfSHA256(secret, salt[:], []byte(blobSealKeyHKDFInfo), 32))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := bytes.Repeat([]byte{3}, gcm.NonceSize())
	ephemeralDER, err := x509.MarshalPKIXPublicKey(ephemeral.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	envelope.EphemeralPublicKey = base64.StdEncoding.EncodeToString(ephemeralDER)
	envelope.IV = base64.StdEncoding.EncodeToString(iv)
	envelope.Ciphertext = base64.StdEncoding.EncodeToString(gcm.Seal(nil, iv, blobKey, aad))
	return envelope
}

func TestBlobSealKeyIsSignedAndOpensSealedEnvelopes(t *testing.T) {
	tlsKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealKey, err := newBlobSealKey(tlsKey, "node-1")
	if err != nil {
		t.Fatalf("newBlobSealKey: %v", err)
	}

	// The phone checks this signature with the attested TLS key.
	signature, err := base64.StdEncoding.DecodeString(sealKey.Signature)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(blobSealKeyDomain + "node-1\n" + sealKey.PublicKey))
	if !ecdsa.VerifyASN1(&tlsKey.PublicKey, digest[:], signature) {
		t.Fatal("seal key signature does not verify with the TLS key")
	}
	otherNode := sha256.Sum256([]byte(blobSealKeyDomain + "node-2\n" + sealKey.PublicKey))
	if ecdsa.VerifyASN1(&tlsKey.PublicKey, otherNode[:], signature) {
		t.Fatal("seal key signature verifies for another node")
	}

	blobKey := testBlobKey(9)
	node := &nodeAgent{blobSealKey: sealKey}
	plaintext, err := node.decryptBlobKeyEnvelope(sealedTestEnvelope(t, sealKey.PublicKey, blobKey),
		blobKeyVerifierForPlaintext(blobKey), time.Now().Add(time.Minute))
	if err != nil || !bytes.Equal(plaintext, blobKey) {
		t.Fatalf("sealed envelope: plaintext=%x err=%v", plaintext, err)
	}

	// A node without a seal key, such as one never attested, refuses them.
	if _, err := (&nodeAgent{}).decryptBlobKeyEnvelope(sealedTestEnvelope(t, sealKey.PublicKey, blobKey),
		blobKeyVerifierForPlaintext(blobKey), time.Now().Add(time.Minute)); err == nil {
		t.Fatal("a node without a seal key opened a sealed envelope")
	}
}
