package nodeauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
)

const (
	SignatureContext = "VIRTROID-NODE-SIGNATURE-V1"

	HeaderNodeID             = "X-Virtroid-Node-ID"
	HeaderTimestamp          = "X-Virtroid-Node-Timestamp"
	HeaderNonce              = "X-Virtroid-Node-Nonce"
	HeaderBodySHA256         = "X-Virtroid-Node-Body-SHA256"
	HeaderSignature          = "X-Virtroid-Node-Signature"
	HeaderPublicKey          = "X-Virtroid-Node-Public-Key"
	HeaderRegistrationSecret = "X-Virtroid-Node-Registration-Secret"
)

var (
	ErrMissingPrivateKey = errors.New("node private key is not configured")
	ErrInvalidPrivateKey = errors.New("node private key is invalid")
	ErrInvalidPublicKey  = errors.New("node public key is invalid")
	ErrInvalidSignature  = errors.New("node signature is invalid")
)

func LoadPrivateKey(material string) (*ecdsa.PrivateKey, string, error) {
	material = strings.TrimSpace(material)
	if material == "" {
		return nil, "", nil
	}

	keyDER, err := decodeKeyMaterial(material)
	if err != nil {
		return nil, "", ErrInvalidPrivateKey
	}

	parsed, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		if ecKey, ecErr := x509.ParseECPrivateKey(keyDER); ecErr == nil {
			if !isP256PublicKey(&ecKey.PublicKey) {
				return nil, "", ErrInvalidPrivateKey
			}
			publicKey, marshalErr := PublicKeyMaterial(&ecKey.PublicKey)
			if marshalErr != nil {
				return nil, "", marshalErr
			}
			return ecKey, publicKey, nil
		}
		return nil, "", ErrInvalidPrivateKey
	}

	privateKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, "", ErrInvalidPrivateKey
	}
	if !isP256PublicKey(&privateKey.PublicKey) {
		return nil, "", ErrInvalidPrivateKey
	}
	publicKey, err := PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		return nil, "", err
	}
	return privateKey, publicKey, nil
}

func ParsePublicKey(material string) (*ecdsa.PublicKey, error) {
	keyDER, err := decodeKeyMaterial(material)
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(keyDER)
	if err != nil {
		return nil, ErrInvalidPublicKey
	}
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, ErrInvalidPublicKey
	}
	if !isP256PublicKey(publicKey) {
		return nil, ErrInvalidPublicKey
	}
	return publicKey, nil
}

func isP256PublicKey(publicKey *ecdsa.PublicKey) bool {
	return publicKey != nil &&
		publicKey.Curve != nil &&
		publicKey.Curve.Params() != nil &&
		publicKey.Curve.Params().Name == elliptic.P256().Params().Name
}

// NormalizePublicKey returns the canonical base64-encoded PKIX form and its
// SHA-256 fingerprint. Registry comparisons use the canonical DER fingerprint
// rather than caller-controlled whitespace or PEM/base64 formatting.
func NormalizePublicKey(material string) (string, string, error) {
	publicKey, err := ParsePublicKey(material)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", "", ErrInvalidPublicKey
	}
	fingerprint := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(der), hex.EncodeToString(fingerprint[:]), nil
}

func PublicKeyMaterial(publicKey *ecdsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func Canonical(method, requestURI, nodeID, timestamp, nonce, bodyHash string) string {
	return strings.Join([]string{
		SignatureContext,
		strings.ToUpper(method),
		requestURI,
		strings.TrimSpace(nodeID),
		strings.TrimSpace(timestamp),
		strings.TrimSpace(nonce),
		strings.TrimSpace(bodyHash),
	}, "\n")
}

func Sign(privateKey *ecdsa.PrivateKey, method, requestURI, nodeID, timestamp, nonce string, body []byte) (string, string, error) {
	if privateKey == nil {
		return "", "", ErrMissingPrivateKey
	}
	bodyHash := BodyHash(body)
	canonical := Canonical(method, requestURI, nodeID, timestamp, nonce, bodyHash)
	digest := sha256.Sum256([]byte(canonical))
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	if err != nil {
		return "", "", err
	}
	return bodyHash, base64.RawURLEncoding.EncodeToString(signature), nil
}

func Verify(publicKeyMaterial, method, requestURI, nodeID, timestamp, nonce, bodyHash, signatureMaterial string) error {
	publicKey, err := ParsePublicKey(publicKeyMaterial)
	if err != nil {
		return err
	}
	signatureDER, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(signatureMaterial))
	if err != nil {
		return ErrInvalidSignature
	}
	canonical := Canonical(method, requestURI, nodeID, timestamp, nonce, bodyHash)
	digest := sha256.Sum256([]byte(canonical))
	if !ecdsa.VerifyASN1(publicKey, digest[:], signatureDER) {
		return ErrInvalidSignature
	}
	return nil
}

func ApplySignedHeaders(req *http.Request, privateKey *ecdsa.PrivateKey, nodeID string, body []byte, publicKey string, registrationSecret string, nowUnix string, nonce string) error {
	bodyHash, signature, err := Sign(privateKey, req.Method, req.URL.RequestURI(), nodeID, nowUnix, nonce, body)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderNodeID, strings.TrimSpace(nodeID))
	req.Header.Set(HeaderTimestamp, strings.TrimSpace(nowUnix))
	req.Header.Set(HeaderNonce, strings.TrimSpace(nonce))
	req.Header.Set(HeaderBodySHA256, bodyHash)
	req.Header.Set(HeaderSignature, signature)
	if strings.TrimSpace(publicKey) != "" {
		req.Header.Set(HeaderPublicKey, strings.TrimSpace(publicKey))
	}
	if strings.TrimSpace(registrationSecret) != "" {
		req.Header.Set(HeaderRegistrationSecret, strings.TrimSpace(registrationSecret))
	}
	return nil
}

func decodeKeyMaterial(material string) ([]byte, error) {
	material = strings.TrimSpace(material)
	if material == "" {
		return nil, ErrInvalidPublicKey
	}
	if strings.Contains(material, "-----BEGIN") {
		block, _ := pem.Decode([]byte(material))
		if block == nil {
			return nil, ErrInvalidPublicKey
		}
		return block.Bytes, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(material); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(material); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(material); err == nil {
		return decoded, nil
	}
	return nil, ErrInvalidPublicKey
}
