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
	"strconv"
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
// refuse an older list that still approved a since-revoked image. A list
// expires, so one a server keeps serving after a newer one exists stops
// working; and it sets, per AMD product, the oldest firmware a VM's host may
// run. Older apps ignore the fields they don't know.
type List struct {
	Version      int            `json:"version"`
	Serial       int64          `json:"serial"`
	IssuedAt     string         `json:"issued_at"`
	ExpiresAt    string         `json:"expires_at,omitempty"`
	MinTCB       map[string]TCB `json:"min_tcb,omitempty"`
	Measurements []string       `json:"measurements"`
}

// TCB is a set of AMD firmware security patch levels, as a VM's attestation
// report and its VCEK certificate state them. FMC exists only on Turin.
type TCB struct {
	Bootloader int `json:"bootloader"`
	TEE        int `json:"tee"`
	SNP        int `json:"snp"`
	Microcode  int `json:"microcode"`
	FMC        int `json:"fmc,omitempty"`
}

// Products whose minimum firmware a list may set. Milan is absent: its
// attestation can be forged, so phones refuse it whatever its firmware.
var Products = []string{"Genoa", "Turin"}

// SignOptions are a list's optional fields.
type SignOptions struct {
	ExpiresAt time.Time
	MinTCB    map[string]TCB
}

// ParseTCB reads "bootloader=12,tee=0,snp=28,microcode=88[,fmc=1]".
func ParseTCB(raw string) (TCB, error) {
	var tcb TCB
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		level, err := strconv.Atoi(strings.TrimSpace(value))
		if !ok || err != nil || level < 0 || level > 255 {
			return TCB{}, fmt.Errorf("invalid firmware level %q", part)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case "bootloader":
			tcb.Bootloader = level
		case "tee":
			tcb.TEE = level
		case "snp":
			tcb.SNP = level
		case "microcode":
			tcb.Microcode = level
		case "fmc":
			tcb.FMC = level
		default:
			return TCB{}, fmt.Errorf("unknown firmware component %q", name)
		}
		seen[name] = true
	}
	for _, required := range []string{"bootloader", "tee", "snp", "microcode"} {
		if !seen[required] {
			return TCB{}, fmt.Errorf("firmware levels need %s", required)
		}
	}
	return tcb, nil
}

func validateMinTCB(minTCB map[string]TCB) error {
	for product, tcb := range minTCB {
		known := false
		for _, allowed := range Products {
			known = known || product == allowed
		}
		if !known {
			return fmt.Errorf("unknown AMD product %q", product)
		}
		for _, level := range []int{tcb.Bootloader, tcb.TEE, tcb.SNP, tcb.Microcode, tcb.FMC} {
			if level < 0 || level > 255 {
				return fmt.Errorf("%s firmware level %d is out of range", product, level)
			}
		}
	}
	return nil
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
func Sign(key *ecdsa.PrivateKey, serial int64, measurements []string, now time.Time, options SignOptions) (SignedList, error) {
	if serial <= 0 {
		return SignedList{}, errors.New("serial must be positive")
	}
	if err := validateMinTCB(options.MinTCB); err != nil {
		return SignedList{}, err
	}
	expiresAt := ""
	if !options.ExpiresAt.IsZero() {
		if !options.ExpiresAt.After(now) {
			return SignedList{}, errors.New("the list would already have expired")
		}
		expiresAt = options.ExpiresAt.UTC().Format(time.RFC3339)
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
		ExpiresAt:    expiresAt,
		MinTCB:       options.MinTCB,
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
	if list.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, list.ExpiresAt); err != nil {
			return List{}, errors.New("expires_at is not an RFC 3339 time")
		}
	}
	if err := validateMinTCB(list.MinTCB); err != nil {
		return List{}, err
	}
	return list, nil
}
