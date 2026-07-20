package callbackauth

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"virtroid/backend/internal/nodeauth"
)

const (
	SignatureContext = "VIRTROID-CONTROL-CALLBACK-SIGNATURE-V1"

	HeaderTimestamp  = "X-Virtroid-Control-Timestamp"
	HeaderNonce      = "X-Virtroid-Control-Nonce"
	HeaderBodySHA256 = "X-Virtroid-Control-Body-SHA256"
	HeaderSignature  = "X-Virtroid-Control-Signature"
)

var (
	ErrMissingPrivateKey = errors.New("control-plane callback private key is not configured")
	ErrInvalidSignature  = errors.New("control-plane callback signature is invalid")
)

func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func Canonical(method, requestURI, timestamp, nonce, bodyHash string) string {
	return strings.Join([]string{
		SignatureContext,
		strings.ToUpper(method),
		requestURI,
		strings.TrimSpace(timestamp),
		strings.TrimSpace(nonce),
		strings.TrimSpace(bodyHash),
	}, "\n")
}

func Sign(privateKey *ecdsa.PrivateKey, method, requestURI, timestamp, nonce string, body []byte) (string, string, error) {
	if privateKey == nil {
		return "", "", ErrMissingPrivateKey
	}
	bodyHash := BodyHash(body)
	digest := sha256.Sum256([]byte(Canonical(method, requestURI, timestamp, nonce, bodyHash)))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		return "", "", err
	}
	return bodyHash, base64.RawURLEncoding.EncodeToString(signature), nil
}

func Verify(publicKeyMaterial, method, requestURI, timestamp, nonce, bodyHash, signatureMaterial string) error {
	publicKey, err := nodeauth.ParsePublicKey(publicKeyMaterial)
	if err != nil {
		return err
	}
	signature, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(signatureMaterial))
	if err != nil {
		return ErrInvalidSignature
	}
	digest := sha256.Sum256([]byte(Canonical(method, requestURI, timestamp, nonce, bodyHash)))
	if !ecdsa.VerifyASN1(publicKey, digest[:], signature) {
		return ErrInvalidSignature
	}
	return nil
}

func ApplySignedHeaders(req *http.Request, privateKey *ecdsa.PrivateKey, body []byte, timestamp, nonce string) error {
	bodyHash, signature, err := Sign(privateKey, req.Method, req.URL.RequestURI(), timestamp, nonce, body)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderTimestamp, strings.TrimSpace(timestamp))
	req.Header.Set(HeaderNonce, strings.TrimSpace(nonce))
	req.Header.Set(HeaderBodySHA256, bodyHash)
	req.Header.Set(HeaderSignature, signature)
	return nil
}
