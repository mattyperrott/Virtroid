// Package measurements signs and verifies the list of SEV-SNP launch
// measurements that phones accept for runtime VMs. The list is signed with an
// offline release key whose public half ships in the app, so the control plane
// can serve it without being able to change it.
package measurements

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Domain separates these signatures from anything else the key could sign.
const Domain = "virtroid-snp-measurements-v1\n"

const MeasurementHexLength = 96

// SignedList is the document the control plane serves.
type SignedList struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

// List is the signed content. Serial only ever increases, so a phone can
// refuse an older list that still approved a since-revoked image.
type List struct {
	Version      int      `json:"version"`
	Serial       int64    `json:"serial"`
	IssuedAt     string   `json:"issued_at"`
	Measurements []string `json:"measurements"`
}

func normalizeMeasurements(raw []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, measurement := range raw {
		measurement = strings.ToLower(strings.TrimSpace(measurement))
		if len(measurement) != MeasurementHexLength {
			return nil, fmt.Errorf("measurement %q is not 96 hex characters", measurement)
		}
		if _, err := hex.DecodeString(measurement); err != nil {
			return nil, fmt.Errorf("measurement %q is not hex", measurement)
		}
		if !seen[measurement] {
			seen[measurement] = true
			out = append(out, measurement)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Sign builds and signs a list.
func Sign(key *ecdsa.PrivateKey, serial int64, measurements []string, now time.Time) (SignedList, error) {
	if serial <= 0 {
		return SignedList{}, errors.New("serial must be positive")
	}
	normalized, err := normalizeMeasurements(measurements)
	if err != nil {
		return SignedList{}, err
	}
	if normalized == nil {
		normalized = []string{}
	}
	payload, err := json.Marshal(List{
		Version:      1,
		Serial:       serial,
		IssuedAt:     now.UTC().Format(time.RFC3339),
		Measurements: normalized,
	})
	if err != nil {
		return SignedList{}, err
	}
	digest := sha256.Sum256(append([]byte(Domain), payload...))
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return SignedList{}, err
	}
	return SignedList{
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(signature),
	}, nil
}

// Verify checks a signed list against any of the trusted public keys.
func Verify(signed SignedList, trusted []*ecdsa.PublicKey) (List, error) {
	payload, err := base64.StdEncoding.DecodeString(signed.Payload)
	if err != nil {
		return List{}, errors.New("payload is not base64")
	}
	signature, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil {
		return List{}, errors.New("signature is not base64")
	}
	digest := sha256.Sum256(append([]byte(Domain), payload...))
	verified := false
	for _, key := range trusted {
		if ecdsa.VerifyASN1(key, digest[:], signature) {
			verified = true
			break
		}
	}
	if !verified {
		return List{}, errors.New("signature does not match a trusted key")
	}
	var list List
	if err := json.Unmarshal(payload, &list); err != nil {
		return List{}, errors.New("payload is not a measurement list")
	}
	if list.Version != 1 || list.Serial <= 0 {
		return List{}, errors.New("unsupported measurement list version or serial")
	}
	if list.Measurements, err = normalizeMeasurements(list.Measurements); err != nil {
		return List{}, err
	}
	return list, nil
}
