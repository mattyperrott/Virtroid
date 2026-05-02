package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"virtdroid/backend/internal/config"
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
	if !strings.Contains(err.Error(), "invalid archive path") {
		t.Fatalf("restoreToDir error = %q, want invalid path", err.Error())
	}
}

func TestRuntimeBlobKeyRejectsExpiredKey(t *testing.T) {
	encodedKey := base64.RawURLEncoding.EncodeToString(testBlobKey(1))
	expiredAt := time.Now().UTC().Add(-time.Second)

	_, err := decodeActiveBlobKey(encodedKey, expiredAt)
	if err == nil {
		t.Fatal("runtimeBlobKey accepted expired key")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("runtimeBlobKey error = %q, want expired", err.Error())
	}
}

func TestRenterdBlobStoreRoundTripAndDelete(t *testing.T) {
	server, objects := newRenterdTestServer(t, "secret")
	sourceDir := filepath.Join(t.TempDir(), "source")
	restoreDir := filepath.Join(t.TempDir(), "restore")
	store := &renterdBlobStore{
		workerURL:  server.URL,
		password:   "secret",
		bucket:     "virtdroid-test",
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
	if manifest.Bucket != "virtdroid-test" {
		t.Fatalf("manifest.Bucket = %q, want virtdroid-test", manifest.Bucket)
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
		bucket:     "virtdroid-test",
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

func configForRenterdTest(serverURL, password string) config.NodeConfig {
	return config.NodeConfig{
		BlobStoreKind:    blobStoreRenterd,
		RenterdWorkerURL: serverURL,
		RenterdPassword:  password,
		RenterdBucket:    "virtdroid-test",
		RuntimeRoot:      filepath.Join(os.TempDir(), "virtdroid-test"),
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
