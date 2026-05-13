package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

const blobKeyEnvelopeAlgorithm = "P256_ECDH_HKDF_SHA256_AESGCM_V1"

type blobKeyEnvelopePayload struct {
	Version            int    `json:"version"`
	Algorithm          string `json:"algorithm"`
	LeaseID            string `json:"lease_id"`
	Operation          string `json:"operation"`
	RuntimeID          string `json:"runtime_id"`
	HostID             string `json:"host_id"`
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	IV                 string `json:"iv"`
	Ciphertext         string `json:"ciphertext"`
}

func (n *nodeAgent) decryptBlobKeyEnvelope(envelope blobKeyEnvelopePayload, expiresAt time.Time) ([]byte, error) {
	if expiresAt.IsZero() || time.Now().UTC().After(expiresAt.UTC()) {
		return nil, errors.New("runtime blob key envelope has expired")
	}
	if envelope.Version != 1 {
		return nil, errors.New("unsupported blob key envelope version")
	}
	if strings.TrimSpace(envelope.Algorithm) != blobKeyEnvelopeAlgorithm {
		return nil, errors.New("unsupported blob key envelope algorithm")
	}

	sharedSecret, err := n.blobKeyEnvelopeSharedSecret(envelope.EphemeralPublicKey)
	if err != nil {
		return nil, err
	}
	aad := blobKeyEnvelopeAAD(envelope)
	salt := sha256.Sum256(aad)
	key := hkdfSHA256(sharedSecret, salt[:], []byte("virtroid-blob-key-envelope-v1"), 32)

	iv, err := decodeEnvelopeBase64(envelope.IV)
	if err != nil {
		return nil, fmt.Errorf("decode blob key envelope iv: %w", err)
	}
	ciphertext, err := decodeEnvelopeBase64(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode blob key envelope ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, iv, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt blob key envelope: %w", err)
	}
	if len(plaintext) != 32 {
		return nil, errors.New("blob key envelope plaintext has invalid length")
	}
	return plaintext, nil
}

func (n *nodeAgent) blobKeyEnvelopeSharedSecret(ephemeralPublicKey string) ([]byte, error) {
	if n.nodePrivateKey == nil {
		return nil, errors.New("node private key is required to decrypt blob key envelopes")
	}
	peer, err := parseECDHPublicKey(ephemeralPublicKey)
	if err != nil {
		return nil, err
	}
	scalar := n.nodePrivateKey.D.FillBytes(make([]byte, 32))
	privateKey, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		return nil, fmt.Errorf("load node ecdh private key: %w", err)
	}
	sharedSecret, err := privateKey.ECDH(peer)
	if err != nil {
		return nil, fmt.Errorf("derive blob key envelope secret: %w", err)
	}
	return sharedSecret, nil
}

func parseECDHPublicKey(publicKeyMaterial string) (*ecdh.PublicKey, error) {
	der, err := decodeEnvelopeBase64(publicKeyMaterial)
	if err != nil {
		return nil, fmt.Errorf("decode blob key envelope public key: %w", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse blob key envelope public key: %w", err)
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return nil, errors.New("blob key envelope public key must be P-256")
	}
	encoded := elliptic.Marshal(elliptic.P256(), publicKey.X, publicKey.Y)
	peer, err := ecdh.P256().NewPublicKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("load blob key envelope public key: %w", err)
	}
	return peer, nil
}

func blobKeyEnvelopeAAD(envelope blobKeyEnvelopePayload) []byte {
	return []byte(strings.Join([]string{
		"VIRTROID-BLOB-KEY-ENVELOPE-V1",
		strings.TrimSpace(envelope.LeaseID),
		strings.TrimSpace(envelope.Operation),
		strings.TrimSpace(envelope.RuntimeID),
		strings.TrimSpace(envelope.HostID),
	}, "\n"))
}

func decodeEnvelopeBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty base64 value")
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return nil, errors.New("invalid base64 value")
}

func hkdfSHA256(secret, salt, info []byte, length int) []byte {
	prk := hmacDigest(sha256.New, salt, secret)
	var output []byte
	var previous []byte
	counter := byte(1)
	for len(output) < length {
		mac := hmac.New(sha256.New, prk)
		mac.Write(previous)
		mac.Write(info)
		mac.Write([]byte{counter})
		previous = mac.Sum(nil)
		output = append(output, previous...)
		counter++
	}
	return output[:length]
}

func hmacDigest(newHash func() hash.Hash, key, payload []byte) []byte {
	mac := hmac.New(newHash, key)
	mac.Write(payload)
	return mac.Sum(nil)
}
