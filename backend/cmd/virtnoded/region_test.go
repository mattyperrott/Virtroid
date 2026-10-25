package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// sealRegionForTest seals as the phone does (RegionSeal.kt).
func sealRegionForTest(t *testing.T, key []byte, runtimeID, plaintext string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{7}, sealedRegionNonceSize)
	sealed := append([]byte(sealedRegionMagic), nonce...)
	sealed = aead.Seal(sealed, nonce, []byte(plaintext), sealedRegionAAD(runtimeID))
	return base64.RawURLEncoding.EncodeToString(sealed)
}

func TestOpenSealedRegionReadsOnlyThisRuntimesRegion(t *testing.T) {
	material := bytes.Repeat([]byte{0x42}, 32)
	runtimeID := "11111111-1111-1111-1111-111111111111"
	key := runtimeRegionKey(material, runtimeID)
	sealed := sealRegionForTest(t, key, runtimeID,
		`{"version":1,"latitude":52.52,"longitude":13.405,"timezone":"Europe/Berlin","label":"Berlin, Germany"}`)

	region, err := openSealedRegion(key, runtimeID, sealed)
	if err != nil {
		t.Fatalf("openSealedRegion: %v", err)
	}
	if region.Latitude != 52.52 || region.Longitude != 13.405 || region.Timezone != "Europe/Berlin" {
		t.Fatalf("region = %+v", region)
	}
	if _, err := openSealedRegion(key, "22222222-2222-2222-2222-222222222222", sealed); err == nil {
		t.Fatal("a region sealed for another runtime opened")
	}
	otherKey := runtimeRegionKey(bytes.Repeat([]byte{0x43}, 32), runtimeID)
	if _, err := openSealedRegion(otherKey, runtimeID, sealed); err == nil {
		t.Fatal("a region opened with another account's key")
	}
	for name, plaintext := range map[string]string{
		"shell in timezone": `{"version":1,"latitude":1,"longitude":1,"timezone":"UTC; reboot"}`,
		"path timezone":     `{"version":1,"latitude":1,"longitude":1,"timezone":"../../etc/passwd"}`,
		"latitude":          `{"version":1,"latitude":91,"longitude":1,"timezone":"UTC"}`,
		"longitude":         `{"version":1,"latitude":1,"longitude":-181,"timezone":"UTC"}`,
		"version":           `{"version":2,"latitude":1,"longitude":1,"timezone":"UTC"}`,
	} {
		if _, err := openSealedRegion(key, runtimeID, sealRegionForTest(t, key, runtimeID, plaintext)); err == nil {
			t.Fatalf("%s: invalid region accepted", name)
		}
	}
}

func TestOpenSealedRegionChecksCoordinates(t *testing.T) {
	material := bytes.Repeat([]byte{0x42}, 32)
	runtimeID := "11111111-1111-1111-1111-111111111111"
	key := runtimeRegionKey(material, runtimeID)
	open := func(latitude, longitude string) error {
		plaintext := `{"version":1,"latitude":` + latitude + `,"longitude":` + longitude + `,"timezone":"UTC"}`
		_, err := openSealedRegion(key, runtimeID, sealRegionForTest(t, key, runtimeID, plaintext))
		return err
	}
	// The edges of the globe are places too.
	for _, coordinates := range [][2]string{{"90", "180"}, {"-90", "-180"}, {"0", "0"}, {"-33.8688", "151.2093"}} {
		if err := open(coordinates[0], coordinates[1]); err != nil {
			t.Fatalf("%v refused: %v", coordinates, err)
		}
	}
	for _, coordinates := range [][2]string{
		{"90.0000001", "0"}, {"-90.5", "0"}, {"0", "180.0000001"}, {"0", "-181"},
		{"1e400", "0"}, {"0", "-1e400"}, {`"52.52"`, "13.4"},
	} {
		if err := open(coordinates[0], coordinates[1]); err == nil {
			t.Fatalf("%v accepted", coordinates)
		}
	}
}

func TestTimezoneNamePattern(t *testing.T) {
	for _, name := range []string{"UTC", "Europe/Berlin", "America/Argentina/Buenos_Aires", "Etc/GMT+5", "America/Port-au-Prince"} {
		if !timezoneNamePattern.MatchString(name) {
			t.Fatalf("%q refused", name)
		}
	}
	for _, name := range []string{"", "/Europe", "Europe/", "Europe Berlin", "a/b/c/d", "$(id)", "Europe/" + strings.Repeat("x", 40)} {
		if timezoneNamePattern.MatchString(name) {
			t.Fatalf("%q accepted", name)
		}
	}
}

func TestShouldApplyRegionOncePerChangeAndRetriesFailures(t *testing.T) {
	node := &nodeAgent{}
	now := time.Now()
	if !node.shouldApplyRegion("rt", "a", now) {
		t.Fatal("first region not applied")
	}
	node.finishRegionAttempt("rt", "a", true)
	if node.shouldApplyRegion("rt", "a", now.Add(time.Hour)) {
		t.Fatal("an applied region was applied again")
	}
	if !node.shouldApplyRegion("rt", "b", now) {
		t.Fatal("a changed region was not applied")
	}
	node.finishRegionAttempt("rt", "b", false)
	if node.shouldApplyRegion("rt", "b", now.Add(time.Second)) {
		t.Fatal("a failed region was retried at once")
	}
	if !node.shouldApplyRegion("rt", "b", now.Add(regionRetryInterval+time.Second)) {
		t.Fatal("a failed region was not retried")
	}
	node.forgetRegion("rt")
	if !node.shouldApplyRegion("rt", "b", now) {
		t.Fatal("a forgotten region was not applied on the next start")
	}
}

// The phone's RegionSealTest opens this same vector.
func TestSealedRegionVector(t *testing.T) {
	key := runtimeRegionKey(bytes.Repeat([]byte{0x42}, 32), "rt-region-vector")
	if got := hex.EncodeToString(key); got != "8374bee8c6b12bc495481ac46b6648f3673752ba8b1ed12db473b6689be28c2a" {
		t.Fatalf("region key = %s", got)
	}
	const sealed = "VlJUUkVHMQoHBwcHBwcHBwcHBwdY6Nm5LscVNdk3Ui_JMIFT5uiUGdeyapmBo-D350VTyYKiF_OLL-rEJNqCRDOO8mZV_rMht6TTy6kSeXkZKBKAn4szNPcuz2fSlK1V1ZPBTb4n40ImBK38RmpqScENfbwCkAlO4QeGxI7b1N__8YLmInBu4xO9"
	if got := sealRegionForTest(t, key, "rt-region-vector", `{"version":1,"latitude":52.52,"longitude":13.405,"timezone":"Europe/Berlin","label":"Berlin, Germany"}`); got != sealed {
		t.Fatalf("sealed = %s", got)
	}
}
