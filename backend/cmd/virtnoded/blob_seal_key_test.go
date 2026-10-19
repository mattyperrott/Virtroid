package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

// phoneRuntimeKeys derives a runtime's keys the way the phone does.
func phoneRuntimeKeys(master []byte, runtimeID string) []byte {
	namespace, _ := hex.DecodeString(deriveRuntimeBlobNamespace(master, runtimeID))
	keys := append([]byte(nil), deriveRuntimeSnapshotKey(master, runtimeID)...)
	keys = append(keys, runtimeManifestMACKey(master, runtimeID)...)
	return append(keys, namespace...)
}

// sealedTestEnvelope seals plaintext to a seal key the way the phone does.
func sealedTestEnvelope(t *testing.T, sealPublicDER, plaintext []byte) blobKeyEnvelopePayload {
	t.Helper()
	parsed, err := x509.ParsePKIXPublicKey(sealPublicDER)
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
	envelope.Ciphertext = base64.StdEncoding.EncodeToString(gcm.Seal(nil, iv, plaintext, aad))
	return envelope
}

func TestRuntimeKeysMatchTheAccountKeyDerivations(t *testing.T) {
	master := testBlobKey(4)
	keys := phoneRuntimeKeys(master, "runtime-1")
	if len(keys) != runtimeKeysSize {
		t.Fatalf("runtime keys are %d bytes", len(keys))
	}
	// Whoever derives them, snapshots read and write the same.
	if !bytes.Equal(deriveRuntimeSnapshotKey(keys, "runtime-1"), deriveRuntimeSnapshotKey(master, "runtime-1")) ||
		!bytes.Equal(runtimeManifestMACKey(keys, "runtime-1"), runtimeManifestMACKey(master, "runtime-1")) ||
		deriveRuntimeBlobNamespace(keys, "runtime-1") != deriveRuntimeBlobNamespace(master, "runtime-1") {
		t.Fatal("runtime keys give different snapshot keys than the account key")
	}
	// They are one runtime's: another runtime's differ.
	if bytes.Equal(phoneRuntimeKeys(master, "runtime-2"), keys) {
		t.Fatal("two runtimes share keys")
	}
	// With the notification key appended, the snapshot keys are unchanged.
	withNotification := append(append([]byte(nil), keys...), bytes.Repeat([]byte{5}, 32)...)
	if !bytes.Equal(deriveRuntimeSnapshotKey(withNotification, "runtime-1"), deriveRuntimeSnapshotKey(master, "runtime-1")) ||
		deriveRuntimeBlobNamespace(withNotification, "runtime-1") != deriveRuntimeBlobNamespace(master, "runtime-1") ||
		!bytes.Equal(runtimeNotificationKey(withNotification), bytes.Repeat([]byte{5}, 32)) ||
		runtimeNotificationKey(keys) != nil || runtimeNotificationKey(master) != nil {
		t.Fatal("the notification key changed the snapshot keys or wasn't found")
	}
	if snapshotKeyForManifest(keys, "runtime-1", &blobManifest{Version: 1}) != nil {
		t.Fatal("runtime keys stood in for the account key of a version 1 snapshot")
	}
}

func TestSealedEnvelopesCarryRuntimeKeysToTheSealKey(t *testing.T) {
	sealKey, err := newBlobSealKey()
	if err != nil {
		t.Fatal(err)
	}
	keys := phoneRuntimeKeys(testBlobKey(9), "runtime-1")
	node := &nodeAgent{blobSealKey: sealKey}
	plaintext, err := node.decryptBlobKeyEnvelope(sealedTestEnvelope(t, sealKey.publicDER, keys),
		runtimeKeysVerifierForPlaintext(keys), time.Now().Add(time.Minute))
	if err != nil || !bytes.Equal(plaintext, keys) {
		t.Fatalf("sealed runtime keys: plaintext=%x err=%v", plaintext, err)
	}
	// Not the account key: a sealed envelope must carry runtime keys.
	master := testBlobKey(9)
	if _, err := node.decryptBlobKeyEnvelope(sealedTestEnvelope(t, sealKey.publicDER, master),
		blobKeyVerifierForPlaintext(master), time.Now().Add(time.Minute)); err == nil {
		t.Fatal("a sealed envelope carried the account key")
	}
	// A node without a seal key, such as one never attested, refuses them.
	if _, err := (&nodeAgent{}).decryptBlobKeyEnvelope(sealedTestEnvelope(t, sealKey.publicDER, keys),
		runtimeKeysVerifierForPlaintext(keys), time.Now().Add(time.Minute)); err == nil {
		t.Fatal("a node without a seal key opened a sealed envelope")
	}
}

func TestSealReportBindsTheKeyToTheChallenge(t *testing.T) {
	sealKey, err := newBlobSealKey()
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{7}, blobSealNonceSize)
	want := sha512.Sum512(append(append([]byte(blobSealReportDomain), sealKey.publicDER...), nonce...))
	if blobSealReportData(sealKey.publicDER, nonce) != want {
		t.Fatal("seal report data is not SHA-512(domain || key || nonce)")
	}
	other := bytes.Repeat([]byte{8}, blobSealNonceSize)
	if blobSealReportData(sealKey.publicDER, other) == want {
		t.Fatal("seal report data ignores the challenge")
	}
}

// The phone derives the same viewer key from the account key, and the node
// from either the account key (older handoffs) or the sealed runtime keys.
func TestRuntimeViewerKeyMatchesAcrossHandoffs(t *testing.T) {
	accountKey := bytes.Repeat([]byte{0x42}, 32)
	runtimeID := "rt-viewer-vector"
	fromAccount := runtimeViewerKey(accountKey, runtimeID)
	runtimeKeys := append(deriveRuntimeSnapshotKey(accountKey, runtimeID), runtimeManifestMACKey(accountKey, runtimeID)...)
	namespace, err := hex.DecodeString(deriveRuntimeBlobNamespace(accountKey, runtimeID))
	if err != nil {
		t.Fatal(err)
	}
	runtimeKeys = append(runtimeKeys, namespace...)
	if fromRuntimeKeys := runtimeViewerKey(runtimeKeys, runtimeID); !bytes.Equal(fromAccount, fromRuntimeKeys) {
		t.Fatal("viewer key differs between account-key and runtime-key handoffs")
	}
	if got := hex.EncodeToString(fromAccount); got != "b39725a7f60e32ebca2e9db9e8d9ff5c92a4eefb070fbc2229c78a6045b551f6" {
		t.Fatalf("viewer key vector = %s", got)
	}
}

func TestThePhonesSnapshotFloorArrivesSealedWithTheKeys(t *testing.T) {
	sealKey, err := newBlobSealKey()
	if err != nil {
		t.Fatal(err)
	}
	keys := append(phoneRuntimeKeys(testBlobKey(9), "runtime-1"), bytes.Repeat([]byte{5}, 32)...)
	plaintext := binary.BigEndian.AppendUint64(append([]byte(nil), keys...), 7)
	node := &nodeAgent{blobSealKey: sealKey}
	opened, err := node.decryptBlobKeyEnvelope(sealedTestEnvelope(t, sealKey.publicDER, plaintext),
		runtimeKeysVerifierForPlaintext(plaintext), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	gotKeys, floor, hasFloor := splitSnapshotFloor(opened)
	if !hasFloor || floor != 7 || !bytes.Equal(gotKeys, keys) {
		t.Fatalf("split = %x, %d, %v", gotKeys, floor, hasFloor)
	}
	if _, _, hasFloor := splitSnapshotFloor(keys); hasFloor {
		t.Fatal("keys from an older phone carried a floor")
	}
	node.noteSnapshotFloor("runtime-1", 7)
	node.noteSnapshotFloor("runtime-1", 3)
	if floor, strict := node.snapshotFloor("runtime-1"); !strict || floor != 7 {
		t.Fatalf("floor = %d, %v; the highest a phone sent is kept", floor, strict)
	}
	if _, strict := node.snapshotFloor("runtime-2"); strict {
		t.Fatal("a runtime no phone sent a floor for is strict")
	}
}

func TestSnapshotsBelowThePhonesFloorAreRefused(t *testing.T) {
	if err := checkSnapshotFloor(&blobManifest{Version: 3, Generation: 7}, 7); err != nil {
		t.Fatalf("the newest snapshot the phone saw was refused: %v", err)
	}
	if err := checkSnapshotFloor(&blobManifest{Version: 3, Generation: 6}, 7); err == nil {
		t.Fatal("an older snapshot was accepted")
	}
	if err := checkSnapshotFloor(&blobManifest{Version: 2, Generation: 9}, 7); err == nil {
		t.Fatal("an unauthenticated manifest was accepted")
	}
	if err := checkSnapshotFloor(nil, 7); err != nil {
		t.Fatalf("a runtime with no snapshot was refused: %v", err)
	}
	for _, tc := range []struct {
		previous *blobManifest
		floor    int64
		strict   bool
		want     int64
	}{
		{nil, 0, false, 1},
		{&blobManifest{Generation: 5}, 0, false, 6},
		{&blobManifest{Generation: 5}, 9, true, 10},
		{nil, 9, true, 10},
		{&blobManifest{Generation: 12}, 9, true, 13},
		{&blobManifest{Generation: 5}, 9, false, 6},
	} {
		got, err := nextSnapshotGeneration(tc.previous, tc.floor, tc.strict)
		if err != nil || got != tc.want {
			t.Errorf("nextSnapshotGeneration(%v, %d, %v) = %d, %v; want %d", tc.previous, tc.floor, tc.strict, got, err, tc.want)
		}
	}
}
