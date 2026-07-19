package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"virtroid/backend/internal/config"
)

func TestSnapshotRoundTrip(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	restoreDir := filepath.Join(root, "restore")
	snapshotPath := filepath.Join(root, "userdata.enc")
	key := testBlobKey(1)

	writeFile(t, filepath.Join(sourceDir, "misc", "settings.db"), "settings")
	writeFile(t, filepath.Join(sourceDir, "system", "users", "0.xml"), "<user />")

	size, err := writeSnapshot(snapshotPath, sourceDir, key)
	if err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}
	if size <= 0 {
		t.Fatalf("writeSnapshot size = %d, want > 0", size)
	}

	if err := restoreSnapshot(snapshotPath, restoreDir, key); err != nil {
		t.Fatalf("restoreSnapshot: %v", err)
	}

	assertFileContent(t, filepath.Join(restoreDir, "misc", "settings.db"), "settings")
	assertFileContent(t, filepath.Join(restoreDir, "system", "users", "0.xml"), "<user />")
}

func TestSnapshotRestoreWrongKeyDoesNotTouchTarget(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	restoreDir := filepath.Join(root, "restore")
	snapshotPath := filepath.Join(root, "userdata.enc")

	writeFile(t, filepath.Join(sourceDir, "app", "state.txt"), "secret")
	if _, err := writeSnapshot(snapshotPath, sourceDir, testBlobKey(1)); err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}
	writeFile(t, filepath.Join(restoreDir, "keep.txt"), "existing")

	err := restoreSnapshot(snapshotPath, restoreDir, testBlobKey(2))
	if err == nil {
		t.Fatal("restoreSnapshot with wrong key succeeded")
	}
	if !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("restoreSnapshot error = %q, want integrity failure", err.Error())
	}
	assertFileContent(t, filepath.Join(restoreDir, "keep.txt"), "existing")
	if _, statErr := os.Stat(filepath.Join(restoreDir, "app", "state.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("restored payload with wrong key, statErr=%v", statErr)
	}
}

func TestSnapshotRejectsSparseSourceBeyondUncompressedBudget(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	oversizedPath := filepath.Join(sourceDir, "oversized.sparse")
	file, err := os.Create(oversizedPath)
	if err != nil {
		t.Fatalf("create sparse source: %v", err)
	}
	if err := file.Truncate(maxRestoredSnapshotData + 1); err != nil {
		_ = file.Close()
		t.Skipf("filesystem cannot create a sparse test file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close sparse source: %v", err)
	}

	_, err = writeSnapshot(filepath.Join(root, "oversized.enc"), sourceDir, testBlobKey(1))
	if err == nil || !strings.Contains(err.Error(), "maximum uncompressed size") {
		t.Fatalf("writeSnapshot oversized sparse source error = %v, want source budget rejection", err)
	}
}

func TestPruneEphemeralAndroidStatePreservesKeystore(t *testing.T) {
	dataDir := t.TempDir()
	keystoreDB := filepath.Join(dataDir, "misc", "keystore", "persistent.sqlite")
	writeFile(t, keystoreDB, "synthetic-password-key-material")
	writeFile(t, filepath.Join(dataDir, "system", "dropbox", "system_server_lowmem.txt"), "dropbox")
	writeFile(t, filepath.Join(dataDir, "anr", "trace.txt"), "trace")
	writeFile(t, filepath.Join(dataDir, "tombstones", "tombstone_00"), "tombstone")

	if err := pruneEphemeralAndroidState(dataDir); err != nil {
		t.Fatalf("pruneEphemeralAndroidState: %v", err)
	}
	assertFileContent(t, keystoreDB, "synthetic-password-key-material")
	for _, removed := range []string{
		filepath.Join(dataDir, "system", "dropbox"),
		filepath.Join(dataDir, "anr"),
		filepath.Join(dataDir, "tombstones"),
	} {
		if _, err := os.Stat(removed); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s still exists after prune, err=%v", removed, err)
		}
	}
}

func TestLocalBlobStoreDetectsChunkTampering(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	restoreDir := filepath.Join(root, "restore")
	store := &localBlobStore{
		root:      filepath.Join(root, "blobstore"),
		chunkSize: 16,
	}

	writeFile(t, filepath.Join(sourceDir, "payload.bin"), strings.Repeat("a", 128))
	writeFile(t, filepath.Join(restoreDir, "keep.txt"), "existing")

	manifest, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(1))
	if err != nil {
		t.Fatalf("persistFromDir: %v", err)
	}
	if len(manifest.Chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(manifest.Chunks))
	}

	chunkPath, err := store.blobObjectPath(manifest.Chunks[0].Key)
	if err != nil {
		t.Fatalf("blobObjectPath: %v", err)
	}
	payload, err := os.ReadFile(chunkPath)
	if err != nil {
		t.Fatalf("read chunk: %v", err)
	}
	payload[0] ^= 0xff
	if err := os.WriteFile(chunkPath, payload, 0o600); err != nil {
		t.Fatalf("tamper chunk: %v", err)
	}

	err = store.restoreToDir(context.Background(), "runtime-1", manifest, restoreDir, testBlobKey(1))
	if err == nil {
		t.Fatal("restoreToDir succeeded with tampered chunk")
	}
	if !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("restoreToDir error = %q, want chunk integrity mismatch", err.Error())
	}
	assertFileContent(t, filepath.Join(restoreDir, "keep.txt"), "existing")
}

func TestLocalBlobStoreRejectsTraversalChunkKey(t *testing.T) {
	store := &localBlobStore{root: t.TempDir(), chunkSize: 16}
	manifest := &blobManifest{
		Version:     1,
		Store:       blobStoreLocal,
		ObjectType:  "runtime-userdata",
		SnapshotID:  "snapshot",
		ChunkSize:   16,
		TotalBytes:  1,
		Compression: "gzip",
		Encryption:  "aes-ctr+hmac-sha256",
		Chunks: []blobChunk{{
			Index:  0,
			Key:    "../outside",
			Size:   1,
			SHA256: strings.Repeat("0", 64),
		}},
	}

	err := store.restoreToDir(context.Background(), "runtime-1", manifest, filepath.Join(t.TempDir(), "restore"), testBlobKey(1))
	if err == nil {
		t.Fatal("restoreToDir accepted traversal chunk key")
	}
	if !strings.Contains(err.Error(), "does not match expected runtime path") {
		t.Fatalf("restoreToDir error = %q, want runtime path validation", err.Error())
	}
}

func TestRuntimeBoundSnapshotRejectsCrossRuntimeTransplant(t *testing.T) {
	root := t.TempDir()
	store := &localBlobStore{root: filepath.Join(root, "blobstore"), chunkSize: 32}
	sourceDir := filepath.Join(root, "source")
	writeFile(t, filepath.Join(sourceDir, "private.txt"), "runtime-a-private-state")
	masterKey := testBlobKey(4)

	manifest, err := store.persistFromDir(context.Background(), "runtime-a", sourceDir, masterKey)
	if err != nil {
		t.Fatalf("persistFromDir: %v", err)
	}
	if manifest.Version != 2 || manifest.RuntimeID != "runtime-a" {
		t.Fatalf("manifest domain = version %d runtime %q, want v2 runtime-a", manifest.Version, manifest.RuntimeID)
	}

	transplanted := *manifest
	transplanted.RuntimeID = "runtime-b"
	transplanted.Chunks = append([]blobChunk(nil), manifest.Chunks...)
	for index := range transplanted.Chunks {
		sourcePath, err := store.blobObjectPath(manifest.Chunks[index].Key)
		if err != nil {
			t.Fatalf("source blobObjectPath: %v", err)
		}
		transplanted.Chunks[index].Key = filepath.ToSlash(filepath.Join("runtime-b", manifest.SnapshotID, filepath.Base(manifest.Chunks[index].Key)))
		targetPath, err := store.blobObjectPath(transplanted.Chunks[index].Key)
		if err != nil {
			t.Fatalf("target blobObjectPath: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			t.Fatalf("mkdir transplanted generation: %v", err)
		}
		payload, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("read source chunk: %v", err)
		}
		if err := os.WriteFile(targetPath, payload, 0o600); err != nil {
			t.Fatalf("write transplanted chunk: %v", err)
		}
	}

	err = store.restoreToDir(context.Background(), "runtime-b", &transplanted, filepath.Join(root, "restore"), masterKey)
	if err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("cross-runtime transplant error = %v, want runtime-bound integrity failure", err)
	}
}

func TestValidateBlobManifestRejectsWrongRuntimeChunkKey(t *testing.T) {
	manifest := &blobManifest{
		Version:     1,
		Store:       blobStoreLocal,
		ObjectType:  "runtime-userdata",
		SnapshotID:  "snapshot",
		ChunkSize:   16,
		TotalBytes:  1,
		Compression: "gzip",
		Encryption:  "aes-ctr+hmac-sha256",
		Chunks: []blobChunk{{
			Index:  0,
			Key:    "other-runtime/snapshot/chunk-00000.bin",
			Size:   1,
			SHA256: strings.Repeat("0", 64),
		}},
	}

	err := validateBlobManifestForRuntime(manifest, "runtime-1")
	if err == nil {
		t.Fatal("validateBlobManifestForRuntime accepted a chunk key for another runtime")
	}
	if !strings.Contains(err.Error(), "does not match expected runtime path") {
		t.Fatalf("validateBlobManifestForRuntime error = %q, want runtime path validation", err.Error())
	}
}

func TestExtractTarRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name:     "escape",
		Typeflag: tar.TypeSymlink,
		Linkname: "../../outside",
		Mode:     0o777,
	}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}

	err := extractTarToDir(tar.NewReader(bytes.NewReader(archive.Bytes())), root)
	if err == nil {
		t.Fatal("extractTarToDir accepted symlink escaping restore root")
	}
	if !strings.Contains(err.Error(), "escapes restore root") {
		t.Fatalf("extractTarToDir error = %q, want symlink escape rejection", err.Error())
	}
}

func TestExtractTarRejectsWriteThroughExistingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{
		Name:     "linkdir/payload.txt",
		Typeflag: tar.TypeReg,
		Mode:     0o600,
		Size:     int64(len("payload")),
	}); err != nil {
		t.Fatalf("write file header: %v", err)
	}
	if _, err := writer.Write([]byte("payload")); err != nil {
		t.Fatalf("write file payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}

	err := extractTarToDir(tar.NewReader(bytes.NewReader(archive.Bytes())), root)
	if err == nil {
		t.Fatal("extractTarToDir wrote through an existing symlink")
	}
	if !strings.Contains(err.Error(), "crosses symlink") {
		t.Fatalf("extractTarToDir error = %q, want symlink crossing rejection", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(outside, "payload.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("payload escaped through symlink, statErr=%v", statErr)
	}
}

func TestRuntimeBlobKeyRejectsExpiredKey(t *testing.T) {
	expiredAt := time.Now().UTC().Add(-time.Second)

	_, err := (&nodeAgent{}).decryptBlobKeyEnvelope(blobKeyEnvelopePayload{}, blobKeyVerifierForPlaintext(testBlobKey(1)), expiredAt)
	if err == nil {
		t.Fatal("runtimeBlobKey accepted expired envelope")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("runtimeBlobKey error = %q, want expired", err.Error())
	}
}

func TestRuntimeBlobKeyRejectsVerifierMismatch(t *testing.T) {
	runtimeID := "11111111-1111-1111-1111-111111111111"
	nodePrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	envelope := testBlobKeyEnvelope(t, nodePrivateKey, runtimeID, "host-1", "stop", "lease-1", testBlobKey(1))
	node := &nodeAgent{nodePrivateKey: nodePrivateKey}

	_, err = node.decryptBlobKeyEnvelope(envelope, blobKeyVerifierForPlaintext(testBlobKey(2)), time.Now().UTC().Add(time.Minute))
	if err == nil {
		t.Fatal("decryptBlobKeyEnvelope accepted a plaintext key that did not match the expected verifier")
	}
	if !strings.Contains(err.Error(), "does not match expected verifier") {
		t.Fatalf("decryptBlobKeyEnvelope error = %q, want verifier mismatch", err.Error())
	}
}

func TestRuntimeBlobKeyAcceptsVerifierMatch(t *testing.T) {
	runtimeID := "11111111-1111-1111-1111-111111111111"
	blobKey := testBlobKey(1)
	nodePrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	envelope := testBlobKeyEnvelope(t, nodePrivateKey, runtimeID, "host-1", "stop", "lease-1", blobKey)
	node := &nodeAgent{nodePrivateKey: nodePrivateKey}

	got, err := node.decryptBlobKeyEnvelope(envelope, blobKeyVerifierForPlaintext(blobKey), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatalf("decryptBlobKeyEnvelope returned error: %v", err)
	}
	if !bytes.Equal(got, blobKey) {
		t.Fatalf("decryptBlobKeyEnvelope returned %x, want %x", got, blobKey)
	}
}

func TestRuntimeBlobKeyCacheCopiesAndZeroesKeys(t *testing.T) {
	runtimeID := "11111111-1111-1111-1111-111111111111"
	node := &nodeAgent{}
	original := testBlobKey(7)
	want := append([]byte(nil), original...)

	node.cacheRuntimeBlobKey(runtimeID, original)
	original[0] ^= 0xff

	got, ok := node.cachedRuntimeBlobKey(runtimeID)
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("cachedRuntimeBlobKey = %x, %v; want an independent copy %x", got, ok, want)
	}
	got[1] ^= 0xff
	gotAgain, ok := node.cachedRuntimeBlobKey(runtimeID)
	if !ok || !bytes.Equal(gotAgain, want) {
		t.Fatalf("caller mutation changed cached key: %x", gotAgain)
	}

	stored := node.runtimeBlobKeys[runtimeID]
	node.clearCachedRuntimeBlobKey(runtimeID)
	if _, ok := node.cachedRuntimeBlobKey(runtimeID); ok {
		t.Fatal("cleared runtime key remained available")
	}
	for index, value := range stored {
		if value != 0 {
			t.Fatalf("cached key byte %d was not zeroed", index)
		}
	}
}

func TestRuntimeBlobKeyUsesLiveSessionCacheWithoutControlPlaneHandoff(t *testing.T) {
	runtimeID := "11111111-1111-1111-1111-111111111111"
	node := &nodeAgent{}
	want := testBlobKey(9)
	node.cacheRuntimeBlobKey(runtimeID, want)

	got, err := node.runtimeBlobKeyWithContext(context.Background(), runtimeAssignment{ID: runtimeID})
	if err != nil {
		t.Fatalf("runtimeBlobKeyWithContext returned error with a live cached key: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("runtimeBlobKeyWithContext = %x, want %x", got, want)
	}
	clearBytes(got)
	stillCached, ok := node.cachedRuntimeBlobKey(runtimeID)
	if !ok || !bytes.Equal(stillCached, want) {
		t.Fatal("clearing a borrowed key copy erased the live-session cache")
	}
}

func TestRenterdBlobStoreRoundTripAndDelete(t *testing.T) {
	server, objects := newRenterdTestServer(t, "secret")
	sourceDir := filepath.Join(t.TempDir(), "source")
	restoreDir := filepath.Join(t.TempDir(), "restore")
	store := &renterdBlobStore{
		workerURL:  server.URL,
		password:   "secret",
		bucket:     "virtroid-test",
		chunkSize:  32,
		httpClient: server.Client(),
	}

	writeFile(t, filepath.Join(sourceDir, "payload.txt"), strings.Repeat("x", 256))
	manifest, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(1))
	if err != nil {
		t.Fatalf("persistFromDir: %v", err)
	}
	if manifest.Store != blobStoreRenterd {
		t.Fatalf("manifest.Store = %q, want %q", manifest.Store, blobStoreRenterd)
	}
	if manifest.Bucket != "virtroid-test" {
		t.Fatalf("manifest.Bucket = %q, want virtroid-test", manifest.Bucket)
	}
	if len(manifest.Chunks) < 2 {
		t.Fatalf("expected multiple renterd chunks, got %d", len(manifest.Chunks))
	}

	if err := store.restoreToDir(context.Background(), "runtime-1", manifest, restoreDir, testBlobKey(1)); err != nil {
		t.Fatalf("restoreToDir: %v", err)
	}
	assertFileContent(t, filepath.Join(restoreDir, "payload.txt"), strings.Repeat("x", 256))

	if err := store.deleteManifest(context.Background(), manifest); err != nil {
		t.Fatalf("deleteManifest: %v", err)
	}
	objects.mu.Lock()
	defer objects.mu.Unlock()
	for key := range objects.items {
		t.Fatalf("object %q remained after deleteManifest", key)
	}
}

func TestRenterdBlobStoreRequiresAuth(t *testing.T) {
	server, _ := newRenterdTestServer(t, "secret")
	store := &renterdBlobStore{
		workerURL:  server.URL,
		password:   "wrong",
		bucket:     "virtroid-test",
		chunkSize:  32,
		httpClient: server.Client(),
	}

	err := store.putObject(context.Background(), "runtime-1/snapshot/chunk.bin", []byte("payload"))
	if err == nil {
		t.Fatal("putObject succeeded with wrong renterd password")
	}
	if !strings.Contains(err.Error(), "status=401") {
		t.Fatalf("putObject error = %q, want status=401", err.Error())
	}
}

func TestRenterdPreflightPassesWhenReady(t *testing.T) {
	server := newRenterdPreflightServer(t, "secret", true, true)
	node := &nodeAgent{
		cfg: configForRenterdTest(server.URL, "secret"),
	}

	report := node.runBlobPreflight(context.Background())
	if !report.OK {
		t.Fatalf("runBlobPreflight OK=false, checks=%+v", report.Checks)
	}
	assertPreflightStatus(t, report, "consensus_state", "pass")
	assertPreflightStatus(t, report, "wallet", "pass")
	assertPreflightStatus(t, report, "active_contracts", "pass")
}

func TestRenterdPreflightReportsConfiguredWalletAddress(t *testing.T) {
	server := newRenterdPreflightServer(t, "secret", true, true)
	cfg := configForRenterdTest(server.URL, "secret")
	cfg.RenterdWalletAddress = "addr:configured"
	node := &nodeAgent{cfg: cfg}

	report := node.runBlobPreflight(context.Background())
	if report.WalletAddress != "addr:configured" {
		t.Fatalf("WalletAddress = %q, want configured address", report.WalletAddress)
	}
}

func TestRenterdPreflightFailsWithoutContracts(t *testing.T) {
	server := newRenterdPreflightServer(t, "secret", true, false)
	node := &nodeAgent{
		cfg: configForRenterdTest(server.URL, "secret"),
	}

	report := node.runBlobPreflight(context.Background())
	if report.OK {
		t.Fatalf("runBlobPreflight OK=true, want false")
	}
	assertPreflightStatus(t, report, "active_contracts", "fail")
}

func TestEnsureRuntimeStoppedPersistsDataWhenContainerMissing(t *testing.T) {
	t.Setenv("NODE_AGENT_CONTAINER_NAME", "virtnoded")
	root := t.TempDir()
	runtimeID := "11111111-1111-1111-1111-111111111111"
	dataDir := filepath.Join(root, runtimeID, "data")
	writeFile(t, filepath.Join(dataDir, "app", "secret.db"), "plaintext")
	nodePrivateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	envelope := testBlobKeyEnvelope(t, nodePrivateKey, runtimeID, "host-1", "stop", "lease-1", testBlobKey(1))

	var blobKeyFetches int
	var statusUpdates int
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blob-key"):
			if got := r.URL.Query().Get("host_id"); got != "" {
				t.Fatalf("blob-key host_id = %q, want no caller-supplied host", got)
			}
			blobKeyFetches++
			writeJSONForTest(t, w, map[string]any{
				"blob_key_envelope":   envelope,
				"blob_key_verifier":   blobKeyVerifierForPlaintext(testBlobKey(1)),
				"blob_key_expires_at": time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/status"):
			statusUpdates++
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/logs"):
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected control-plane request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer controlPlane.Close()

	node := &nodeAgent{
		cfg: config.NodeConfig{
			NodeID:            "host-1",
			ControlPlaneURL:   controlPlane.URL,
			RuntimeRoot:       root,
			BlobStoreKind:     blobStoreLocal,
			DockerNetworkName: "virtroid-guests",
		},
		controlPlane: controlPlane.Client(),
		docker: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader(`{"message":"not found"}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
		nodePrivateKey: nodePrivateKey,
	}

	if err := node.ensureRuntimeStopped(context.Background(), runtimeAssignment{
		ID:               runtimeID,
		BlobAutoSnapshot: true,
	}, false); err != nil {
		t.Fatalf("ensureRuntimeStopped returned error: %v", err)
	}
	if blobKeyFetches == 0 {
		t.Fatal("ensureRuntimeStopped did not fetch a blob key for existing data")
	}
	if statusUpdates == 0 {
		t.Fatal("ensureRuntimeStopped did not report stopped status")
	}
	if directoryHasEntries(dataDir) {
		t.Fatal("plaintext data directory still has entries after stop")
	}
	if !directoryHasEntries(filepath.Join(root, "_blobstore", "local")) {
		t.Fatal("encrypted local blobstore was not populated")
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("read %s = %q, want %q", path, string(got), want)
	}
}

func testBlobKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed
	}
	return key
}

func testBlobKeyEnvelope(t *testing.T, nodePrivateKey *ecdsa.PrivateKey, runtimeID, hostID, operation, leaseID string, blobKey []byte) blobKeyEnvelopePayload {
	t.Helper()
	ephemeralKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey ephemeral: %v", err)
	}
	ephemeralScalar := ephemeralKey.D.FillBytes(make([]byte, 32))
	ephemeralECDH, err := ecdh.P256().NewPrivateKey(ephemeralScalar)
	if err != nil {
		t.Fatalf("NewPrivateKey ephemeral: %v", err)
	}
	nodePublicBytes := elliptic.Marshal(elliptic.P256(), nodePrivateKey.PublicKey.X, nodePrivateKey.PublicKey.Y)
	nodeECDH, err := ecdh.P256().NewPublicKey(nodePublicBytes)
	if err != nil {
		t.Fatalf("NewPublicKey node: %v", err)
	}
	sharedSecret, err := ephemeralECDH.ECDH(nodeECDH)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}

	envelope := blobKeyEnvelopePayload{
		Version:   1,
		Algorithm: blobKeyEnvelopeAlgorithm,
		LeaseID:   leaseID,
		Operation: operation,
		RuntimeID: runtimeID,
		HostID:    hostID,
	}
	aad := blobKeyEnvelopeAAD(envelope)
	salt := sha256.Sum256(aad)
	wrappingKey := hkdfSHA256(sharedSecret, salt[:], []byte("virtroid-blob-key-envelope-v1"), 32)
	block, err := aes.NewCipher(wrappingKey)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("NewGCM: %v", err)
	}
	iv := bytes.Repeat([]byte{7}, gcm.NonceSize())
	ciphertext := gcm.Seal(nil, iv, blobKey, aad)
	publicDER, err := x509.MarshalPKIXPublicKey(&ephemeralKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	envelope.EphemeralPublicKey = base64.StdEncoding.EncodeToString(publicDER)
	envelope.IV = base64.StdEncoding.EncodeToString(iv)
	envelope.Ciphertext = base64.StdEncoding.EncodeToString(ciphertext)
	return envelope
}

func configForRenterdTest(serverURL, password string) config.NodeConfig {
	return config.NodeConfig{
		BlobStoreKind:    blobStoreRenterd,
		RenterdWorkerURL: serverURL,
		RenterdPassword:  password,
		RenterdBucket:    "virtroid-test",
		RuntimeRoot:      filepath.Join(os.TempDir(), "virtroid-test"),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func writeJSONForTest(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

type renterdTestObjects struct {
	mu    sync.Mutex
	items map[string][]byte
}

func newRenterdTestServer(t *testing.T, password string) (*httptest.Server, *renterdTestObjects) {
	t.Helper()
	objects := &renterdTestObjects{items: make(map[string][]byte)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotPassword, ok := r.BasicAuth()
		if !ok || gotPassword != password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		prefix := "/api/worker/objects/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		objectKey := r.URL.Query().Get("bucket") + "/" + strings.TrimPrefix(r.URL.Path, prefix)

		objects.mu.Lock()
		defer objects.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			objects.items[objectKey] = payload
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			payload, ok := objects.items[objectKey]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(payload)
		case http.MethodDelete:
			delete(objects.items, objectKey)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, objects
}

func newRenterdPreflightServer(t *testing.T, password string, synced bool, hasContracts bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotPassword, ok := r.BasicAuth()
		if !ok || gotPassword != password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/bus/consensus/state":
			_ = json.NewEncoder(w).Encode(map[string]any{"synced": synced})
		case "/api/bus/wallet":
			_ = json.NewEncoder(w).Encode(map[string]any{"siacoins": "1000000000000000000000000"})
		case "/api/bus/contracts/active":
			if hasContracts {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "contract-1"}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func assertPreflightStatus(t *testing.T, report blobPreflightReport, name, want string) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			if check.Status != want {
				t.Fatalf("preflight check %s status = %q, want %q; checks=%+v", name, check.Status, want, report.Checks)
			}
			return
		}
	}
	t.Fatalf("preflight check %s missing; checks=%+v", name, report.Checks)
}
