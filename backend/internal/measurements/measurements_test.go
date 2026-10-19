package measurements

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSignedListRoundTrip(t *testing.T) {
	key := newKey(t)
	a, b := strings.Repeat("ab", 48), strings.Repeat("CD", 48)
	signed, err := Sign(key, 7, []string{b, a, a}, time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := Verify(signed, []*ecdsa.PublicKey{&newKey(t).PublicKey, &key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	if list.Serial != 7 || list.IssuedAt != "2026-10-12T00:00:00Z" ||
		len(list.Measurements) != 2 || list.Measurements[0] != a || list.Measurements[1] != strings.ToLower(b) {
		t.Fatalf("list = %+v", list)
	}
}

func TestVerifyRejectsForgeries(t *testing.T) {
	key := newKey(t)
	signed, err := Sign(key, 1, []string{strings.Repeat("ab", 48)}, time.Now(), SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(signed, []*ecdsa.PublicKey{&newKey(t).PublicKey}); err == nil {
		t.Fatal("accepted a list signed by an untrusted key")
	}
	payload, _ := base64.StdEncoding.DecodeString(signed.Payload)
	tampered := signed
	tampered.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), `"serial":1`, `"serial":9`, 1)))
	if _, err := Verify(tampered, []*ecdsa.PublicKey{&key.PublicKey}); err == nil {
		t.Fatal("accepted a tampered payload")
	}
}

func TestSignRejectsBadInput(t *testing.T) {
	key := newKey(t)
	for name, measurements := range map[string][]string{
		"short":   {"abcd"},
		"not hex": {strings.Repeat("zz", 48)},
	} {
		if _, err := Sign(key, 1, measurements, time.Now(), SignOptions{}); err == nil {
			t.Errorf("%s measurement accepted", name)
		}
	}
	if _, err := Sign(key, 0, nil, time.Now(), SignOptions{}); err == nil {
		t.Error("serial 0 accepted")
	}
	if signed, err := Sign(key, 2, nil, time.Now(), SignOptions{}); err != nil {
		t.Errorf("an empty list (revoke everything) was refused: %v", err)
	} else if list, err := Verify(signed, []*ecdsa.PublicKey{&key.PublicKey}); err != nil || len(list.Measurements) != 0 {
		t.Errorf("empty list = %+v err=%v", list, err)
	}
}

func TestListsCarryExpiryAndMinimumFirmware(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	genoa, err := ParseTCB("bootloader=12, tee=0, snp=28, microcode=88")
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Sign(key, 5, []string{strings.Repeat("ab", 48)}, now, SignOptions{
		ExpiresAt: now.Add(60 * 24 * time.Hour),
		MinTCB:    map[string]TCB{"Genoa": genoa},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := Verify(signed, []*ecdsa.PublicKey{&key.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	if list.ExpiresAt != "2026-12-18T00:00:00Z" || list.MinTCB["Genoa"] != (TCB{Bootloader: 12, SNP: 28, Microcode: 88}) {
		t.Fatalf("list = %+v", list)
	}
	if _, err := Sign(key, 5, nil, now, SignOptions{MinTCB: map[string]TCB{"Milan": genoa}}); err == nil {
		t.Fatal("a minimum for Milan was signed")
	}
	if _, err := Sign(key, 5, nil, now, SignOptions{ExpiresAt: now}); err == nil {
		t.Fatal("an already expired list was signed")
	}
	for _, bad := range []string{"", "bootloader=12,tee=0,snp=28", "bootloader=12,tee=0,snp=28,microcode=256", "bootloader=x,tee=0,snp=28,microcode=1", "bl=1,tee=0,snp=28,microcode=1"} {
		if _, err := ParseTCB(bad); err == nil {
			t.Errorf("ParseTCB(%q) accepted", bad)
		}
	}
	if tcb, err := ParseTCB("fmc=1,bootloader=2,tee=3,snp=4,microcode=5"); err != nil || tcb.FMC != 1 {
		t.Fatalf("Turin levels = %+v, %v", tcb, err)
	}
}
