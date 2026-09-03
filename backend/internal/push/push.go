package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const pushEnvelopeContext = "VIRTROID-PUSH-ENVELOPE-V1"

func EncryptEnvelope(publicKeyMaterial string, payload []byte) (string, error) {
	ecdsaPublicKey, err := parseEncryptionPublicKey(publicKeyMaterial)
	if err != nil {
		return "", err
	}
	recipient, err := ecdsaPublicKey.ECDH()
	if err != nil {
		return "", errors.New("convert notification encryption public key")
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return "", errors.New("derive notification envelope key")
	}
	key, err := hkdf.Key(sha256.New, shared, nil, pushEnvelopeContext, 32)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := aead.Seal(nil, nonce, payload, []byte(pushEnvelopeContext))
	rawEphemeral := ephemeral.PublicKey().Bytes()
	x, y := elliptic.Unmarshal(elliptic.P256(), rawEphemeral)
	if x == nil || y == nil {
		return "", errors.New("encode ephemeral notification key")
	}
	ephemeralDER, err := x509.MarshalPKIXPublicKey(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y})
	if err != nil {
		return "", err
	}
	envelope, err := json.Marshal(map[string]any{
		"v":                    1,
		"ephemeral_public_key": base64.RawURLEncoding.EncodeToString(ephemeralDER),
		"nonce":                base64.RawURLEncoding.EncodeToString(nonce),
		"ciphertext":           base64.RawURLEncoding.EncodeToString(ciphertext),
	})
	if err != nil {
		return "", err
	}
	return string(envelope), nil
}

func ValidateEncryptionPublicKey(publicKeyMaterial string) error {
	_, err := parseEncryptionPublicKey(publicKeyMaterial)
	return err
}

func parseEncryptionPublicKey(publicKeyMaterial string) (*ecdsa.PublicKey, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(publicKeyMaterial))
	if err != nil {
		return nil, errors.New("invalid notification encryption public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(decoded)
	if err != nil {
		return nil, errors.New("invalid notification encryption public key")
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return nil, errors.New("notification encryption key must be P-256")
	}
	return publicKey, nil
}
