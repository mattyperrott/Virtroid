package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// A runtime's region is where its Android believes it is: a place, served to
// the custom AOSP image's GNSS HAL by the runtime's location feed (gnss.go),
// and that place's timezone, applied here. Phones seal it with a key derived
// from the runtime's viewer key, so the control plane storing it learns
// neither.
const (
	sealedRegionMagic       = "VRTREG1\n"
	sealedRegionNonceSize   = 12
	runtimeRegionKeyContext = "VIRTROID-RUNTIME-REGION-KEY-V1"
	maxSealedRegionBytes    = 2048
	regionRetryInterval     = time.Minute
)

// IANA names ("Europe/Berlin", "America/Argentina/Buenos_Aires", "UTC"),
// never anything a shell or Android would read as more than a name.
var timezoneNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]{0,31}(/[A-Za-z0-9][A-Za-z0-9_+-]{0,31}){0,2}$`)

type runtimeRegion struct {
	Version   int     `json:"version"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
}

func runtimeRegionKey(material []byte, runtimeID string) []byte {
	viewerKey := runtimeViewerKey(material, runtimeID)
	defer clearBytes(viewerKey)
	mac := hmac.New(sha256.New, viewerKey)
	_, _ = mac.Write([]byte(runtimeRegionKeyContext))
	return mac.Sum(nil)
}

// sealedRegionAAD binds a region to its runtime, so one sealed for another
// runtime does not open here.
func sealedRegionAAD(runtimeID string) []byte {
	return []byte("virtroid-region-v1\n" + strings.TrimSpace(runtimeID))
}

func openSealedRegion(key []byte, runtimeID, encoded string) (runtimeRegion, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return runtimeRegion{}, errors.New("sealed region is not base64url")
	}
	header := len(sealedRegionMagic) + sealedRegionNonceSize
	if len(sealed) > maxSealedRegionBytes || len(sealed) < header+16 || string(sealed[:len(sealedRegionMagic)]) != sealedRegionMagic {
		return runtimeRegion{}, errors.New("region is not sealed by the phone")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return runtimeRegion{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return runtimeRegion{}, err
	}
	plaintext, err := aead.Open(nil, sealed[len(sealedRegionMagic):header], sealed[header:], sealedRegionAAD(runtimeID))
	if err != nil {
		return runtimeRegion{}, errors.New("region was not sealed for this runtime")
	}
	var region runtimeRegion
	if err := json.Unmarshal(plaintext, &region); err != nil {
		return runtimeRegion{}, fmt.Errorf("sealed region: %w", err)
	}
	if region.Version != 1 {
		return runtimeRegion{}, fmt.Errorf("sealed region version %d is not supported", region.Version)
	}
	if math.IsNaN(region.Latitude) || math.Abs(region.Latitude) > 90 || math.IsNaN(region.Longitude) || math.Abs(region.Longitude) > 180 {
		return runtimeRegion{}, errors.New("sealed region has no valid coordinates")
	}
	if !timezoneNamePattern.MatchString(region.Timezone) {
		return runtimeRegion{}, errors.New("sealed region has no valid timezone")
	}
	return region, nil
}

func regionDigest(sealed string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(sealed)))
	return hex.EncodeToString(digest[:])
}

// shouldApplyRegion reports whether this sealed region still needs applying
// to the running runtime, and reserves the attempt.
func (n *nodeAgent) shouldApplyRegion(runtimeID, digest string, now time.Time) bool {
	n.regionMu.Lock()
	defer n.regionMu.Unlock()
	if n.regionApplied == nil {
		n.regionApplied = make(map[string]regionAttempt)
	}
	previous, ok := n.regionApplied[runtimeID]
	if ok && previous.digest == digest && (previous.succeeded || now.Before(previous.retryAt)) {
		return false
	}
	n.regionApplied[runtimeID] = regionAttempt{digest: digest, retryAt: now.Add(regionRetryInterval)}
	return true
}

func (n *nodeAgent) finishRegionAttempt(runtimeID, digest string, succeeded bool) {
	n.regionMu.Lock()
	defer n.regionMu.Unlock()
	if attempt, ok := n.regionApplied[runtimeID]; ok && attempt.digest == digest {
		attempt.succeeded = succeeded
		n.regionApplied[runtimeID] = attempt
	}
}

// forgetRegion makes the next start apply the region again.
func (n *nodeAgent) forgetRegion(runtimeID string) {
	n.regionMu.Lock()
	defer n.regionMu.Unlock()
	delete(n.regionApplied, runtimeID)
}

type regionAttempt struct {
	digest    string
	succeeded bool
	retryAt   time.Time
}

// ensureRuntimeRegion applies a running runtime's sealed region once per
// change. Without a region the runtime keeps whatever timezone it has.
func (n *nodeAgent) ensureRuntimeRegion(ctx context.Context, runtime runtimeAssignment, containerName string) error {
	if runtime.RegionSealed == nil || strings.TrimSpace(*runtime.RegionSealed) == "" {
		// A region taken away takes the place with it: no fix from now on.
		n.gnss.set(runtime.ID, nil)
		return nil
	}
	digest := regionDigest(*runtime.RegionSealed)
	if !n.shouldApplyRegion(runtime.ID, digest, time.Now()) {
		return nil
	}
	err := n.applyRuntimeRegion(ctx, runtime, containerName)
	n.finishRegionAttempt(runtime.ID, digest, err == nil)
	return err
}

func (n *nodeAgent) applyRuntimeRegion(ctx context.Context, runtime runtimeAssignment, containerName string) error {
	material, err := n.runtimeBlobKeyWithContext(ctx, runtime)
	if err != nil {
		return fmt.Errorf("the runtime's keys are not on this node: %w", err)
	}
	defer clearBytes(material)
	key := runtimeRegionKey(material, runtime.ID)
	defer clearBytes(key)
	region, err := openSealedRegion(key, runtime.ID, *runtime.RegionSealed)
	if err != nil {
		return err
	}
	n.gnss.set(runtime.ID, &gnssPlace{latitude: region.Latitude, longitude: region.Longitude})
	// Automatic detection would replace a manual zone if it ever had a
	// signal to go on; the runtime's zone is the one its owner chose.
	if _, err := n.execInContainerCapture(ctx, containerName, "0", nil, []string{"settings", "put", "global", "auto_time_zone", "0"}); err != nil {
		return fmt.Errorf("turn off automatic timezone: %w", err)
	}
	if _, err := n.execInContainerCaptureAny(ctx, containerName, "0", nil, [][]string{
		{"cmd", "alarm", "set-timezone", region.Timezone},
		{"setprop", "persist.sys.timezone", region.Timezone},
	}); err != nil {
		// The failed command names the zone, so it stays out of the error.
		return errors.New("Android refused the timezone")
	}
	current, err := n.getProp(ctx, containerName, "persist.sys.timezone")
	if err != nil {
		return fmt.Errorf("read timezone back: %w", err)
	}
	if strings.TrimSpace(current) != region.Timezone {
		return errors.New("the timezone did not change")
	}
	// Runtime logs reach the control plane, so they name neither the place
	// nor its timezone: both are what the seal hides.
	_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "info", "Runtime region applied.")
	return nil
}
