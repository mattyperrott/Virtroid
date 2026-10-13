package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

type fakeSiaObject struct {
	meta siaObjectMeta
	data []byte
}

type fakeSiaObjects struct {
	mu        sync.Mutex
	objects   map[string]*fakeSiaObject
	available int64
	putErr    error
	prunes    int
}

func newFakeSiaObjects() *fakeSiaObjects {
	return &fakeSiaObjects{objects: make(map[string]*fakeSiaObject), available: 1 << 30}
}

func (f *fakeSiaObjects) remaining(context.Context) (int64, error) {
	return f.available, nil
}

func (f *fakeSiaObjects) put(_ context.Context, meta siaObjectMeta, r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if f.putErr != nil {
		return "", f.putErr
	}
	sum := sha256.Sum256(append([]byte(meta.SnapshotID), data...))
	id := hex.EncodeToString(sum[:])
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[id] = &fakeSiaObject{meta: meta, data: data}
	return id, nil
}

func (f *fakeSiaObjects) open(_ context.Context, objectID string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	object, ok := f.objects[objectID]
	if !ok {
		return nil, 0, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(object.data)), int64(len(object.data)), nil
}

func (f *fakeSiaObjects) remove(_ context.Context, objectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, objectID)
	return nil
}

func (f *fakeSiaObjects) list(context.Context) ([]siaObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var listed []siaObjectInfo
	for id, object := range f.objects {
		listed = append(listed, siaObjectInfo{ID: id, Meta: object.meta})
	}
	return listed, nil
}

func (f *fakeSiaObjects) prune(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes++
	return nil
}

func (f *fakeSiaObjects) close() error {
	return nil
}

func (f *fakeSiaObjects) snapshotIDs(runtimeID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, object := range f.objects {
		if object.meta.RuntimeID == runtimeID {
			ids = append(ids, object.meta.SnapshotID)
		}
	}
	return ids
}

func testSiaCredentials() siaCredentials {
	return siaCredentials{
		IndexerURL:  "https://indexer.example",
		AppKeySeed:  bytes.Repeat([]byte{9}, 32),
		DataShards:  2,
		TotalShards: 6,
	}
}

func newTestSiaStore(t *testing.T, objects siaObjects) *siaBlobStore {
	t.Helper()
	return &siaBlobStore{
		tempDir:   filepath.Join(t.TempDir(), "sia-tmp"),
		chunkSize: 32,
		accounts:  &siaAccounts{},
		credentials: func(context.Context, string) (siaCredentials, error) {
			return testSiaCredentials(), nil
		},
		connect: func(context.Context, siaCredentials) (siaObjects, error) {
			return objects, nil
		},
	}
}

func TestSiaBlobStoreRoundTripAndDelete(t *testing.T) {
	objects := newFakeSiaObjects()
	store := newTestSiaStore(t, objects)
	sourceDir := filepath.Join(t.TempDir(), "source")
	restoreDir := filepath.Join(t.TempDir(), "restore")
	writeFile(t, filepath.Join(sourceDir, "payload.txt"), strings.Repeat("x", 256))

	manifest, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(1))
	if err != nil {
		t.Fatalf("persistFromDir: %v", err)
	}
	if manifest.Store != blobStoreSia {
		t.Fatalf("manifest.Store = %q, want %q", manifest.Store, blobStoreSia)
	}
	if len(manifest.Chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(manifest.Chunks))
	}
	if _, err := parseSiaObjectID(manifest.SiaObject); err != nil {
		t.Fatalf("manifest.SiaObject: %v", err)
	}
	if entries, _ := os.ReadDir(store.tempDir); len(entries) != 0 {
		t.Fatalf("persist left %d entries in the temp dir", len(entries))
	}

	if err := store.restoreToDir(context.Background(), "runtime-1", manifest, restoreDir, testBlobKey(1)); err != nil {
		t.Fatalf("restoreToDir: %v", err)
	}
	assertFileContent(t, filepath.Join(restoreDir, "payload.txt"), strings.Repeat("x", 256))

	if err := store.deleteManifest(context.Background(), manifest); err != nil {
		t.Fatalf("deleteManifest: %v", err)
	}
	if ids := objects.snapshotIDs("runtime-1"); len(ids) != 0 {
		t.Fatalf("objects remained after deleteManifest: %v", ids)
	}
	if objects.prunes != 1 {
		t.Fatalf("prunes = %d, want 1", objects.prunes)
	}
}

func TestSiaBlobStoreRestoreDetectsTamperingAndWrongSize(t *testing.T) {
	objects := newFakeSiaObjects()
	store := newTestSiaStore(t, objects)
	sourceDir := filepath.Join(t.TempDir(), "source")
	writeFile(t, filepath.Join(sourceDir, "payload.txt"), strings.Repeat("y", 200))
	manifest, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(2))
	if err != nil {
		t.Fatalf("persistFromDir: %v", err)
	}
	object := objects.objects[manifest.SiaObject]
	original := bytes.Clone(object.data)

	object.data[len(object.data)/2] ^= 0xff
	err = store.restoreToDir(context.Background(), "runtime-1", manifest, filepath.Join(t.TempDir(), "restore"), testBlobKey(2))
	if err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("restore of tampered object = %v, want integrity mismatch", err)
	}

	object.data = append(bytes.Clone(original), 'z')
	err = store.restoreToDir(context.Background(), "runtime-1", manifest, filepath.Join(t.TempDir(), "restore"), testBlobKey(2))
	if err == nil || !strings.Contains(err.Error(), "manifest says") {
		t.Fatalf("restore of longer object = %v, want size mismatch", err)
	}
}

func TestSiaBlobStoreRejectsManifestFromAnotherRuntime(t *testing.T) {
	objects := newFakeSiaObjects()
	store := newTestSiaStore(t, objects)
	sourceDir := filepath.Join(t.TempDir(), "source")
	writeFile(t, filepath.Join(sourceDir, "payload.txt"), "state")
	manifest, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(3))
	if err != nil {
		t.Fatalf("persistFromDir: %v", err)
	}
	if err := store.restoreToDir(context.Background(), "runtime-2", manifest, t.TempDir(), testBlobKey(3)); err == nil {
		t.Fatal("restore accepted another runtime's manifest")
	}
}

func TestSiaBlobStorePruneRuntimeKeepsCurrentSnapshotOnly(t *testing.T) {
	objects := newFakeSiaObjects()
	store := newTestSiaStore(t, objects)
	sourceDir := filepath.Join(t.TempDir(), "source")
	writeFile(t, filepath.Join(sourceDir, "payload.txt"), "first")
	first, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(4))
	if err != nil {
		t.Fatalf("persist first: %v", err)
	}
	writeFile(t, filepath.Join(sourceDir, "payload.txt"), "second")
	second, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(4))
	if err != nil {
		t.Fatalf("persist second: %v", err)
	}
	other, err := store.persistFromDir(context.Background(), "runtime-2", sourceDir, testBlobKey(5))
	if err != nil {
		t.Fatalf("persist other runtime: %v", err)
	}

	if err := store.pruneRuntime(context.Background(), "runtime-1", second.Namespace, second.SnapshotID); err != nil {
		t.Fatalf("pruneRuntime: %v", err)
	}
	if ids := objects.snapshotIDs("runtime-1"); len(ids) != 1 || ids[0] != second.SnapshotID {
		t.Fatalf("runtime-1 snapshots after prune = %v, want only %s (first was %s)", ids, second.SnapshotID, first.SnapshotID)
	}
	if ids := objects.snapshotIDs("runtime-2"); len(ids) != 1 || ids[0] != other.SnapshotID {
		t.Fatalf("runtime-2 snapshots after prune = %v, want %s", ids, other.SnapshotID)
	}
	if objects.prunes != 1 {
		t.Fatalf("prunes = %d, want 1", objects.prunes)
	}

	if err := store.clearRuntime(context.Background(), "runtime-1"); err != nil {
		t.Fatalf("clearRuntime: %v", err)
	}
	if ids := objects.snapshotIDs("runtime-1"); len(ids) != 0 {
		t.Fatalf("runtime-1 snapshots after clear = %v", ids)
	}
	if ids := objects.snapshotIDs("runtime-2"); len(ids) != 1 {
		t.Fatalf("clearRuntime removed another runtime's snapshot: %v", ids)
	}
}

func TestSiaBlobStoreCleanupIsNoopWithoutSia(t *testing.T) {
	store := newTestSiaStore(t, newFakeSiaObjects())
	store.credentials = func(context.Context, string) (siaCredentials, error) {
		return siaCredentials{}, errSiaNotConfigured
	}
	if err := store.clearRuntime(context.Background(), "runtime-1"); err != nil {
		t.Fatalf("clearRuntime without sia: %v", err)
	}
	if err := store.pruneRuntime(context.Background(), "runtime-1", "", "snapshot"); err != nil {
		t.Fatalf("pruneRuntime without sia: %v", err)
	}
}

func TestSiaBlobStoreRefusesFullQuotaAndFailedUpload(t *testing.T) {
	objects := newFakeSiaObjects()
	store := newTestSiaStore(t, objects)
	sourceDir := filepath.Join(t.TempDir(), "source")
	writeFile(t, filepath.Join(sourceDir, "payload.txt"), "state")

	objects.available = 10
	if _, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(6)); !errors.Is(err, errRuntimeStorageQuotaExceeded) {
		t.Fatalf("persist to a full account = %v, want the quota error", err)
	}
	objects.available = 1 << 30
	objects.putErr = errors.New("not enough shards")
	if _, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(6)); err == nil || !strings.Contains(err.Error(), "not enough shards") {
		t.Fatalf("persist with failing upload = %v", err)
	}
	if ids := objects.snapshotIDs("runtime-1"); len(ids) != 0 {
		t.Fatalf("failed persists left objects: %v", ids)
	}
}

func TestSiaPruneWaitsForUploads(t *testing.T) {
	objects := newFakeSiaObjects()
	var lock sync.RWMutex
	lock.RLock()
	if err := pruneSiaAccount(context.Background(), objects, &lock); err != nil {
		t.Fatalf("pruneSiaAccount during upload: %v", err)
	}
	if objects.prunes != 0 {
		t.Fatal("pruned while an upload held the account")
	}
	lock.RUnlock()
	if err := pruneSiaAccount(context.Background(), objects, &lock); err != nil {
		t.Fatalf("pruneSiaAccount: %v", err)
	}
	if objects.prunes != 1 {
		t.Fatalf("prunes = %d, want 1", objects.prunes)
	}
}

func TestSiaAccountsShareOneClientPerAccount(t *testing.T) {
	connects := 0
	objects := newFakeSiaObjects()
	store := newTestSiaStore(t, objects)
	store.connect = func(context.Context, siaCredentials) (siaObjects, error) {
		connects++
		return objects, nil
	}
	for _, runtimeID := range []string{"runtime-1", "runtime-2", "runtime-1"} {
		if _, _, err := store.account(context.Background(), runtimeID); err != nil {
			t.Fatalf("account(%s): %v", runtimeID, err)
		}
	}
	if connects != 1 {
		t.Fatalf("connects = %d, want 1 for one account", connects)
	}
}

func TestChunkHasherSplitsAtChunkBoundaries(t *testing.T) {
	for _, size := range []int{1, 31, 32, 33, 64, 70} {
		payload := bytes.Repeat([]byte("abcdefgh"), 9)[:size]
		hasher := &chunkHasher{reader: bytes.NewReader(payload), chunkSize: 32, keyPrefix: "ns/snap"}
		if _, err := io.Copy(io.Discard, hasher); err != nil {
			t.Fatalf("size %d: copy: %v", size, err)
		}
		chunks := hasher.finish()
		if hasher.total != int64(size) {
			t.Fatalf("size %d: total = %d", size, hasher.total)
		}
		var rebuilt int64
		for index, chunk := range chunks {
			start := int64(index) * 32
			want := sha256.Sum256(payload[start : start+chunk.Size])
			if chunk.SHA256 != hex.EncodeToString(want[:]) {
				t.Fatalf("size %d: chunk %d hash mismatch", size, index)
			}
			if wantKey := fmt.Sprintf("ns/snap/chunk-%05d.bin", index); chunk.Key != wantKey {
				t.Fatalf("size %d: chunk %d key = %q, want %q", size, index, chunk.Key, wantKey)
			}
			rebuilt += chunk.Size
		}
		if rebuilt != int64(size) || len(chunks) != (size+31)/32 {
			t.Fatalf("size %d: %d chunks covering %d bytes", size, len(chunks), rebuilt)
		}
	}
}

func TestValidateBlobManifestChecksSiaObject(t *testing.T) {
	objects := newFakeSiaObjects()
	store := newTestSiaStore(t, objects)
	sourceDir := filepath.Join(t.TempDir(), "source")
	writeFile(t, filepath.Join(sourceDir, "payload.txt"), "state")
	manifest, err := store.persistFromDir(context.Background(), "runtime-1", sourceDir, testBlobKey(7))
	if err != nil {
		t.Fatalf("persistFromDir: %v", err)
	}

	missing := *manifest
	missing.SiaObject = ""
	if err := validateBlobManifestForRuntime(&missing, "runtime-1"); err == nil {
		t.Fatal("accepted a sia manifest without an object id")
	}
	local := *manifest
	local.Store = blobStoreLocal
	if err := validateBlobManifestForRuntime(&local, "runtime-1"); err == nil {
		t.Fatal("accepted a local manifest that names a sia object")
	}
}

func TestSiaCredentialsValidate(t *testing.T) {
	valid := testSiaCredentials()
	if err := valid.validate(); err != nil {
		t.Fatalf("valid credentials: %v", err)
	}
	for name, mutate := range map[string]func(*siaCredentials){
		"url":       func(c *siaCredentials) { c.IndexerURL = "indexer.example" },
		"seed":      func(c *siaCredentials) { c.AppKeySeed = c.AppKeySeed[:16] },
		"no parity": func(c *siaCredentials) { c.TotalShards = c.DataShards },
		"too many":  func(c *siaCredentials) { c.TotalShards = 256 },
		"no data":   func(c *siaCredentials) { c.DataShards = 0 },
	} {
		credentials := testSiaCredentials()
		mutate(&credentials)
		if err := credentials.validate(); err == nil {
			t.Fatalf("%s: invalid credentials accepted", name)
		}
	}
}

func TestFetchSiaCredentials(t *testing.T) {
	seed := bytes.Repeat([]byte{4}, 32)
	configured := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/runtimes/runtime-1/sia-credentials" {
			http.NotFound(w, r)
			return
		}
		if !configured {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"sia_not_configured"}`))
			return
		}
		writeJSONForTest(t, w, map[string]any{
			"indexer_url":  "https://indexer.example/",
			"app_key_seed": base64.StdEncoding.EncodeToString(seed),
			"data_shards":  10,
			"total_shards": 30,
		})
	}))
	defer server.Close()
	node := &nodeAgent{
		cfg:          config.NodeConfig{ControlPlaneURL: server.URL},
		controlPlane: &http.Client{Timeout: 5 * time.Second},
	}

	credentials, err := node.fetchSiaCredentials(context.Background(), "runtime-1")
	if err != nil {
		t.Fatalf("fetchSiaCredentials: %v", err)
	}
	if credentials.IndexerURL != "https://indexer.example" || !bytes.Equal(credentials.AppKeySeed, seed) ||
		credentials.DataShards != 10 || credentials.TotalShards != 30 {
		t.Fatalf("credentials = %+v", credentials)
	}

	configured = false
	if _, err := node.fetchSiaCredentials(context.Background(), "runtime-1"); !errors.Is(err, errSiaNotConfigured) {
		t.Fatalf("fetch without sia = %v, want errSiaNotConfigured", err)
	}
	if _, err := node.fetchSiaCredentials(context.Background(), "runtime-2"); err == nil || errors.Is(err, errSiaNotConfigured) {
		t.Fatalf("fetch for unknown runtime = %v, want a plain error", err)
	}
}

func TestLocalToSiaMigrationRestoresOnAnotherNode(t *testing.T) {
	objects := newFakeSiaObjects()
	runtimeID := "runtime-migration"
	key := testBlobKey(8)

	oldRoot := t.TempDir()
	oldData := filepath.Join(oldRoot, runtimeID, "data")
	writeFile(t, filepath.Join(oldData, "app", "state.db"), "durable user state")
	localStore := &localBlobStore{root: filepath.Join(oldRoot, "_blobstore", "local"), chunkSize: 32}
	localManifest, err := localStore.persistFromDir(context.Background(), runtimeID, oldData, key)
	if err != nil {
		t.Fatalf("persist local snapshot: %v", err)
	}

	oldNode := newSiaTestNode(t, oldRoot, runtimeID, objects)
	runtime := runtimeAssignment{
		ID:               runtimeID,
		BlobAutoSnapshot: true,
		BlobManifestJSON: stringPtr(marshalBlobManifest(localManifest)),
	}
	persisted, err := oldNode.persistSessionData(context.Background(), runtime, &sessionPersistencePlan{
		dataDir:   oldData,
		masterKey: append([]byte(nil), key...),
		store:     oldNode.siaStore(),
	})
	if err != nil {
		t.Fatalf("persist to sia: %v", err)
	}
	if persisted.Manifest.Store != blobStoreSia || !sameBlobGeneration(persisted.Manifest.MigrationFallback, localManifest) {
		t.Fatal("sia manifest did not keep the local snapshot as its migration fallback")
	}

	// A fresh VM restores the runtime from Sia alone.
	newRoot := t.TempDir()
	newNode := newSiaTestNode(t, newRoot, runtimeID, objects)
	runtime.BlobManifestJSON = stringPtr(marshalBlobManifest(persisted.Manifest))
	newNode.cacheRuntimeBlobKey(runtimeID, key)
	restored, err := newNode.prepareSessionData(context.Background(), runtime)
	if err != nil {
		t.Fatalf("prepareSessionData on the new node: %v", err)
	}
	if !restored {
		t.Fatal("prepareSessionData reported nothing restored")
	}
	assertFileContent(t, filepath.Join(newRoot, runtimeID, "data", "app", "state.db"), "durable user state")
}

// newSiaTestNode is a node set up for Sia whose account client is the fake.
func newSiaTestNode(t *testing.T, root, runtimeID string, objects siaObjects) *nodeAgent {
	t.Helper()
	node := &nodeAgent{
		cfg:          config.NodeConfig{RuntimeRoot: root, BlobStoreKind: blobStoreSia},
		controlPlane: &http.Client{Timeout: time.Second},
	}
	credentials := testSiaCredentials()
	node.siaAccounts.credentials = map[string]cachedSiaCredentials{
		runtimeID: {credentials: credentials, expiresAt: time.Now().Add(time.Hour)},
	}
	node.siaAccounts.clients = map[string]siaObjects{credentials.account(): objects}
	node.siaAccounts.locks = map[string]*sync.RWMutex{credentials.account(): {}}
	return node
}

func TestSiaNodeKeepsSnapshotsLocalWithoutAnIndexer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"sia_not_configured"}`))
	}))
	defer server.Close()
	node := &nodeAgent{
		cfg: config.NodeConfig{
			ControlPlaneURL: server.URL,
			RuntimeRoot:     t.TempDir(),
			BlobStoreKind:   blobStoreSia,
		},
		controlPlane: &http.Client{Timeout: 5 * time.Second},
	}
	runtime := runtimeAssignment{ID: "runtime-1", BlobAutoSnapshot: true}
	node.cacheRuntimeBlobKey(runtime.ID, testBlobKey(9))
	plan, err := node.prepareSessionPersistence(context.Background(), runtime)
	if err != nil {
		t.Fatalf("prepareSessionPersistence: %v", err)
	}
	defer clearBytes(plan.masterKey)
	if plan.store.kind() != blobStoreLocal {
		t.Fatalf("store = %s, want %s without an indexer", plan.store.kind(), blobStoreLocal)
	}

	sia := newSiaTestNode(t, t.TempDir(), runtime.ID, newFakeSiaObjects())
	sia.cacheRuntimeBlobKey(runtime.ID, testBlobKey(9))
	plan, err = sia.prepareSessionPersistence(context.Background(), runtime)
	if err != nil {
		t.Fatalf("prepareSessionPersistence with an indexer: %v", err)
	}
	defer clearBytes(plan.masterKey)
	if plan.store.kind() != blobStoreSia {
		t.Fatalf("store = %s, want %s", plan.store.kind(), blobStoreSia)
	}
}

func TestSiaUploadFailureKeepsSnapshotsLocalUntilSiaWorks(t *testing.T) {
	objects := newFakeSiaObjects()
	root := t.TempDir()
	runtimeID := "runtime-1"
	key := testBlobKey(10)
	node := newSiaTestNode(t, root, runtimeID, objects)
	node.cacheRuntimeBlobKey(runtimeID, key)
	dataDir := filepath.Join(root, runtimeID, "data")
	runtime := runtimeAssignment{ID: runtimeID, BlobAutoSnapshot: true}
	persist := func(content string) *blobManifest {
		t.Helper()
		writeFile(t, filepath.Join(dataDir, "app", "state.db"), content)
		plan, err := node.prepareSessionPersistence(context.Background(), runtime)
		if err != nil {
			t.Fatalf("prepareSessionPersistence: %v", err)
		}
		defer clearBytes(plan.masterKey)
		persisted, err := node.persistSessionData(context.Background(), runtime, plan)
		if err != nil {
			t.Fatalf("persistSessionData: %v", err)
		}
		if err := node.cleanupBlobStorage(runtime, persisted.Manifest); err != nil {
			t.Fatalf("cleanupBlobStorage: %v", err)
		}
		runtime.BlobManifestJSON = stringPtr(marshalBlobManifest(persisted.Manifest))
		return persisted.Manifest
	}

	first := persist("first")
	if first.Store != blobStoreSia {
		t.Fatalf("first store = %s, want %s", first.Store, blobStoreSia)
	}
	objects.putErr = errors.New("no more hosts available")
	second := persist("second")
	if second.Store != blobStoreLocal || second.MigrationFallback == nil || second.MigrationFallback.Store != blobStoreSia {
		t.Fatalf("second = %s with fallback %+v, want local keeping the Sia generation", second.Store, second.MigrationFallback)
	}
	objects.putErr = nil
	third := persist("third")
	if third.Store != blobStoreSia || third.MigrationFallback == nil || third.MigrationFallback.Store != blobStoreLocal ||
		third.MigrationFallback.MigrationFallback != nil {
		t.Fatalf("third = %s with fallback %+v, want Sia keeping only the local generation", third.Store, third.MigrationFallback)
	}
	if err := validateBlobManifestForRuntime(third, runtimeID); err != nil {
		t.Fatalf("third manifest: %v", err)
	}
	if ids := objects.snapshotIDs(runtimeID); len(ids) != 1 || ids[0] != third.SnapshotID {
		t.Fatalf("Sia snapshots = %v, want only the third", ids)
	}

	// The kept fallback still authenticates on its own.
	fallbackDir := filepath.Join(t.TempDir(), "fallback")
	local, err := node.blobStore(blobStoreLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := local.restoreToDir(context.Background(), runtimeID, third.MigrationFallback, fallbackDir, key); err != nil {
		t.Fatalf("restore the local fallback: %v", err)
	}
	assertFileContent(t, filepath.Join(fallbackDir, "app", "state.db"), "second")
	if _, err := node.prepareSessionData(context.Background(), runtime); err != nil {
		t.Fatalf("prepareSessionData: %v", err)
	}
	assertFileContent(t, filepath.Join(dataDir, "app", "state.db"), "third")
}
