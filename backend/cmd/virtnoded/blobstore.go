package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errSnapshotMissing = errors.New("snapshot missing")
var errBlobSnapshotTooLarge = errors.New("encrypted snapshot exceeds maximum size")
var errRuntimeStorageQuotaExceeded = errors.New("encrypted snapshot exceeds the account storage quota")

const (
	snapshotMagic             = "VRTBLOB1"
	runtimeSnapshotKeyContext = "VIRTROID-RUNTIME-SNAPSHOT-KEY-V2"
	runtimeBlobNamespaceCtx   = "VIRTROID-RUNTIME-BLOB-NAMESPACE-V3"
	runtimeManifestMACCtx     = "VIRTROID-RUNTIME-MANIFEST-MAC-V3"
	runtimeBoundEncryption    = "aes-ctr+hmac-sha256+runtime-kdf-v2"
	authenticatedEncryption   = "aes-ctr+hmac-sha256+runtime-kdf-v2+manifest-mac-v3"
	snapshotTagSize           = sha256.Size
	blobStoreLocal            = "local-disk"
	blobStoreRenterd          = "sia-renterd"
	blobChunkSize             = 4 << 20
	maxBlobChunkSize          = 16 << 20
	maxBlobSnapshotBytes      = 16 << 30
	maxBlobManifestChunks     = maxBlobSnapshotBytes / blobChunkSize
	maxRestoredSnapshotData   = 32 << 30
	maxRestoredFileCount      = 1_000_000
	maxArchivePathBytes       = 4096
	maxSnapshotDirectoryDepth = 256
	snapshotReadDirBatchSize  = 128
	maxRenterdListResponse    = 8 << 20
	maxRenterdGCObjectCount   = 100_000
)

type blobStore interface {
	kind() string
	persistFromDir(ctx context.Context, runtimeID, dataDir string, masterKey []byte) (*blobManifest, error)
	restoreToDir(ctx context.Context, runtimeID string, manifest *blobManifest, dataDir string, masterKey []byte) error
	clearRuntime(ctx context.Context, runtimeID string) error
	pruneRuntime(ctx context.Context, runtimeID, namespace, keepSnapshotID string) error
	deleteManifest(ctx context.Context, manifest *blobManifest) error
}

type blobManifest struct {
	Version           int           `json:"version"`
	RuntimeID         string        `json:"runtime_id,omitempty"`
	Namespace         string        `json:"namespace,omitempty"`
	Store             string        `json:"store"`
	Bucket            string        `json:"bucket,omitempty"`
	ObjectType        string        `json:"object_type"`
	SnapshotID        string        `json:"snapshot_id"`
	Generation        int64         `json:"generation,omitempty"`
	CreatedAt         time.Time     `json:"created_at"`
	ChunkSize         int64         `json:"chunk_size"`
	TotalBytes        int64         `json:"total_bytes"`
	Compression       string        `json:"compression"`
	Encryption        string        `json:"encryption"`
	Chunks            []blobChunk   `json:"chunks"`
	ManifestMAC       string        `json:"manifest_mac,omitempty"`
	MigrationFallback *blobManifest `json:"migration_fallback,omitempty"`
}

type blobChunk struct {
	Index  int    `json:"index"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type persistedBlob struct {
	Manifest      *blobManifest
	SnapshotAt    *time.Time
	ClearExisting bool
}

type sessionPersistencePlan struct {
	dataDir   string
	masterKey []byte
	store     blobStore
}

type boundedSnapshotWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedSnapshotWriter) Write(payload []byte) (int, error) {
	if int64(len(payload)) > w.remaining {
		if w.remaining <= 0 {
			return 0, errBlobSnapshotTooLarge
		}
		written, err := w.writer.Write(payload[:w.remaining])
		w.remaining -= int64(written)
		if err != nil {
			return written, err
		}
		return written, errBlobSnapshotTooLarge
	}
	written, err := w.writer.Write(payload)
	w.remaining -= int64(written)
	return written, err
}

type blobPreflightReport struct {
	Store         string               `json:"store"`
	OK            bool                 `json:"ok"`
	WalletAddress string               `json:"wallet_address,omitempty"`
	Checks        []blobPreflightCheck `json:"checks"`
}

type blobPreflightCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type localBlobStore struct {
	root      string
	chunkSize int64
}

type renterdBlobStore struct {
	workerURL     string
	password      string
	bucket        string
	walletAddress string
	minShards     int
	totalShards   int
	contractSet   string
	chunkSize     int64
	httpClient    *http.Client
	cleanupPath   string
	cleanupMu     *sync.Mutex
}

func (n *nodeAgent) blobStores() map[string]blobStore {
	stores := map[string]blobStore{
		blobStoreLocal: &localBlobStore{
			root:      filepath.Join(n.cfg.RuntimeRoot, "_blobstore", "local"),
			chunkSize: blobChunkSize,
		},
	}
	if strings.TrimSpace(n.cfg.RenterdWorkerURL) != "" {
		stores[blobStoreRenterd] = &renterdBlobStore{
			workerURL:     strings.TrimRight(strings.TrimSpace(n.cfg.RenterdWorkerURL), "/"),
			password:      n.cfg.RenterdPassword,
			bucket:        defaultBlobBucket(n.cfg.RenterdBucket),
			walletAddress: strings.TrimSpace(n.cfg.RenterdWalletAddress),
			minShards:     n.cfg.RenterdMinShards,
			totalShards:   n.cfg.RenterdTotalShards,
			contractSet:   strings.TrimSpace(n.cfg.RenterdContractSet),
			chunkSize:     blobChunkSize,
			httpClient:    &http.Client{Timeout: 10 * time.Minute},
			cleanupPath:   filepath.Join(n.cfg.RuntimeRoot, "_blobstore", "pending-renterd-deletes.json"),
			cleanupMu:     &n.blobCleanupMu,
		}
	}
	return stores
}

func (n *nodeAgent) blobStore(kind string) (blobStore, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = blobStoreLocal
	}
	store, ok := n.blobStores()[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported blob store kind %q", kind)
	}
	return store, nil
}

func (n *nodeAgent) blobStoreForManifest(manifest *blobManifest) (blobStore, error) {
	if manifest != nil && strings.TrimSpace(manifest.Store) != "" {
		return n.blobStore(manifest.Store)
	}
	return n.blobStore(n.cfg.BlobStoreKind)
}

func (n *nodeAgent) runBlobPreflight(ctx context.Context) blobPreflightReport {
	return n.runBlobPreflightForKind(ctx, n.cfg.BlobStoreKind)
}

func (n *nodeAgent) runBlobPreflightForKind(ctx context.Context, kind string) blobPreflightReport {
	report := blobPreflightReport{
		Store: strings.TrimSpace(kind),
		OK:    true,
	}
	if report.Store == "" {
		report.Store = blobStoreLocal
	}

	store, err := n.blobStore(report.Store)
	if err != nil {
		report.addCheck("blob_store", "fail", err.Error())
		return report
	}

	switch typed := store.(type) {
	case *localBlobStore:
		typed.preflight(ctx, &report)
	case *renterdBlobStore:
		typed.preflight(ctx, &report)
	default:
		report.addCheck("blob_store", "fail", "unsupported blob store implementation")
	}
	return report
}

func (r *blobPreflightReport) addCheck(name, status, detail string) {
	if status == "fail" {
		r.OK = false
	}
	r.Checks = append(r.Checks, blobPreflightCheck{
		Name:   name,
		Status: status,
		Detail: detail,
	})
}

func (n *nodeAgent) prepareSessionData(ctx context.Context, runtime runtimeAssignment) (bool, error) {
	runtimeRoot := filepath.Join(n.cfg.RuntimeRoot, runtime.ID)
	dataDir := filepath.Join(runtimeRoot, "data")
	manifest, err := parseBlobManifest(runtime.BlobManifestJSON)
	if err != nil {
		return false, err
	}
	masterKey, err := n.runtimeBlobKeyWithContext(ctx, runtime)
	if err != nil {
		return false, err
	}
	defer clearBytes(masterKey)

	if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
		return false, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return false, err
	}

	if manifest != nil {
		store, err := n.blobStoreForManifest(manifest)
		if err != nil {
			return false, err
		}
		if err := os.RemoveAll(dataDir); err != nil {
			return false, err
		}
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return false, err
		}
		if err := store.restoreToDir(ctx, runtime.ID, manifest, dataDir, masterKey); err != nil {
			fallback := manifest.MigrationFallback
			if fallback == nil {
				return false, err
			}
			fallbackStore, fallbackErr := n.blobStoreForManifest(fallback)
			if fallbackErr != nil {
				return false, errors.Join(err, fallbackErr)
			}
			if resetErr := os.RemoveAll(dataDir); resetErr != nil {
				return false, errors.Join(err, resetErr)
			}
			if resetErr := os.MkdirAll(dataDir, 0o755); resetErr != nil {
				return false, errors.Join(err, resetErr)
			}
			if fallbackErr := fallbackStore.restoreToDir(ctx, runtime.ID, fallback, dataDir, masterKey); fallbackErr != nil {
				return false, errors.Join(err, fmt.Errorf("restore migration fallback: %w", fallbackErr))
			}
			_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", "Primary userdata blob restore failed; the verified migration fallback was restored.")
		} else if fallback := manifest.MigrationFallback; fallback != nil {
			if fallbackStore, fallbackErr := n.blobStoreForManifest(fallback); fallbackErr == nil {
				if cleanupErr := fallbackStore.deleteManifest(context.WithoutCancel(ctx), fallback); cleanupErr != nil {
					_ = n.appendRuntimeLog(ctx, runtime.ID, "node", "warn", "Primary userdata blob restored, but its encrypted migration fallback could not yet be deleted.")
				}
			}
		}
		if err := repairLegacyAndroidSystemOwnership(dataDir); err != nil {
			return false, err
		}
		if err := pruneEphemeralAndroidState(dataDir); err != nil {
			return false, err
		}
		return true, nil
	}

	snapshotPath := legacySnapshotPath(n.cfg.RuntimeRoot, runtime.ID)
	if fileExists(snapshotPath) {
		if err := os.RemoveAll(dataDir); err != nil {
			return false, err
		}
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return false, err
		}
		if err := restoreSnapshot(snapshotPath, dataDir, masterKey); err != nil {
			return false, err
		}
		if err := repairLegacyAndroidSystemOwnership(dataDir); err != nil {
			return false, err
		}
		if err := pruneEphemeralAndroidState(dataDir); err != nil {
			return false, err
		}
		return true, nil
	}

	return directoryHasEntries(dataDir), nil
}

func pruneEphemeralAndroidState(dataDir string) error {
	paths := []string{
		filepath.Join(dataDir, "anr"),
		filepath.Join(dataDir, "system", "dropbox"),
		filepath.Join(dataDir, "tombstones"),
	}

	for _, target := range paths {
		if err := os.RemoveAll(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (n *nodeAgent) prepareSessionPersistence(ctx context.Context, runtime runtimeAssignment) (*sessionPersistencePlan, error) {
	dataDir := filepath.Join(n.cfg.RuntimeRoot, runtime.ID, "data")
	plan := &sessionPersistencePlan{dataDir: dataDir}
	if !runtime.BlobAutoSnapshot {
		return plan, nil
	}
	masterKey, err := n.runtimeBlobKeyWithContext(ctx, runtime)
	if err != nil {
		return nil, err
	}
	plan.masterKey = masterKey
	store, err := n.blobStore(n.cfg.BlobStoreKind)
	if err != nil {
		clearBytes(masterKey)
		return nil, err
	}
	report := n.runBlobPreflightForKind(ctx, store.kind())
	if !report.OK {
		clearBytes(masterKey)
		return nil, fmt.Errorf("blob store %s is not ready: %s", store.kind(), summarizeBlobPreflightFailures(report))
	}
	plan.store = store
	return plan, nil
}

func (n *nodeAgent) persistSessionData(ctx context.Context, runtime runtimeAssignment, plan *sessionPersistencePlan) (*persistedBlob, error) {
	if plan == nil {
		return nil, errors.New("session persistence plan is required")
	}
	if !directoryHasEntries(plan.dataDir) || !runtime.BlobAutoSnapshot {
		return &persistedBlob{ClearExisting: true}, nil
	}
	if plan.store == nil {
		return nil, errors.New("session persistence store is not prepared")
	}

	manifest, err := plan.store.persistFromDir(ctx, runtime.ID, plan.dataDir, plan.masterKey)
	if err != nil {
		return nil, err
	}
	previous, err := parseBlobManifest(runtime.BlobManifestJSON)
	if err != nil {
		_ = plan.store.deleteManifest(context.WithoutCancel(ctx), manifest)
		return nil, err
	}
	manifest.Generation = 1
	if previous != nil {
		if previous.Generation < 0 || previous.Generation == math.MaxInt64 {
			_ = plan.store.deleteManifest(context.WithoutCancel(ctx), manifest)
			return nil, errors.New("blob manifest generation cannot be advanced")
		}
		manifest.Generation = previous.Generation + 1
	}
	if previous != nil && previous.Store != manifest.Store {
		manifest.MigrationFallback = previous
	}
	if err := authenticateBlobManifest(manifest, plan.masterKey, runtime.ID); err != nil {
		_ = plan.store.deleteManifest(context.WithoutCancel(ctx), manifest)
		return nil, err
	}
	if err := enforceRuntimeStorageQuota(runtime, manifest); err != nil {
		cleanupErr := plan.store.deleteManifest(context.WithoutCancel(ctx), manifest)
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("delete over-quota blob candidate: %w", cleanupErr)
		}
		return nil, errors.Join(err, cleanupErr)
	}
	now := time.Now().UTC()
	return &persistedBlob{
		Manifest:   manifest,
		SnapshotAt: &now,
	}, nil
}

func enforceRuntimeStorageQuota(runtime runtimeAssignment, candidate *blobManifest) error {
	if runtime.StorageBytesLimit == nil || runtime.StorageBytesUsed == nil {
		return nil
	}
	limit := *runtime.StorageBytesLimit
	used := *runtime.StorageBytesUsed
	if limit < 0 || used < 0 {
		return errors.New("control plane returned invalid runtime storage quota metadata")
	}
	candidateBytes, err := manifestStorageBytes(candidate)
	if err != nil {
		return err
	}
	current, err := parseBlobManifest(runtime.BlobManifestJSON)
	if err != nil {
		return err
	}
	currentBytes, err := manifestStorageBytes(current)
	if err != nil {
		return err
	}
	otherRuntimeBytes := used - currentBytes
	if otherRuntimeBytes < 0 {
		otherRuntimeBytes = 0
	}
	if limit <= 0 || candidateBytes > limit-otherRuntimeBytes {
		return fmt.Errorf(
			"%w: candidate=%d other_runtimes=%d limit=%d",
			errRuntimeStorageQuotaExceeded,
			candidateBytes,
			otherRuntimeBytes,
			limit,
		)
	}
	return nil
}

func manifestStorageBytes(manifest *blobManifest) (int64, error) {
	if manifest == nil {
		return 0, nil
	}
	if manifest.TotalBytes <= 0 || manifest.TotalBytes > maxBlobSnapshotBytes {
		return 0, fmt.Errorf("blob manifest has invalid total size %d", manifest.TotalBytes)
	}
	total := manifest.TotalBytes
	if fallback := manifest.MigrationFallback; fallback != nil {
		if fallback.MigrationFallback != nil {
			return 0, errors.New("blob manifest migration fallback cannot be nested")
		}
		if fallback.TotalBytes <= 0 || fallback.TotalBytes > maxBlobSnapshotBytes {
			return 0, fmt.Errorf("blob migration fallback has invalid total size %d", fallback.TotalBytes)
		}
		if total > math.MaxInt64-fallback.TotalBytes {
			return 0, errors.New("blob manifest storage usage overflows")
		}
		total += fallback.TotalBytes
	}
	return total, nil
}

func summarizeBlobPreflightFailures(report blobPreflightReport) string {
	var failures []string
	for _, check := range report.Checks {
		if check.Status == "fail" {
			failures = append(failures, check.Name+"="+check.Detail)
		}
	}
	if len(failures) == 0 {
		return "preflight failed"
	}
	return strings.Join(failures, "; ")
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (n *nodeAgent) clearSnapshot(runtime runtimeAssignment) error {
	if err := n.clearManifestChunks(runtime); err != nil {
		return err
	}
	for _, store := range n.blobStores() {
		if err := store.clearRuntime(context.Background(), runtime.ID); err != nil {
			return err
		}
	}
	snapshotPath := legacySnapshotPath(n.cfg.RuntimeRoot, runtime.ID)
	if err := os.Remove(snapshotPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (n *nodeAgent) cleanupBlobStorage(runtime runtimeAssignment, retained *blobManifest) error {
	if err := os.Remove(legacySnapshotPath(n.cfg.RuntimeRoot, runtime.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	stores := n.blobStores()
	if retained == nil {
		if err := n.clearManifestChunks(runtime); err != nil {
			return err
		}
		for _, store := range stores {
			if err := store.clearRuntime(context.Background(), runtime.ID); err != nil {
				return err
			}
		}
		return nil
	}
	retainedStore, err := n.blobStoreForManifest(retained)
	if err != nil {
		return err
	}
	previous, err := parseBlobManifest(runtime.BlobManifestJSON)
	if err != nil {
		return err
	}
	retainsPreviousAsFallback := sameBlobGeneration(previous, retained.MigrationFallback)
	if previous != nil && !retainsPreviousAsFallback && (previous.Store != retained.Store || previous.Bucket != retained.Bucket || previous.SnapshotID != retained.SnapshotID) {
		if err := validateBlobManifestForRuntime(previous, runtime.ID); err != nil {
			return err
		}
		previousStore, err := n.blobStoreForManifest(previous)
		if err != nil {
			return err
		}
		if err := previousStore.deleteManifest(context.Background(), previous); err != nil {
			return err
		}
	}
	for kind, store := range stores {
		if kind == retainedStore.kind() {
			if err := store.pruneRuntime(context.Background(), runtime.ID, retained.Namespace, retained.SnapshotID); err != nil {
				return err
			}
			continue
		}
		if retained.MigrationFallback != nil && kind == retained.MigrationFallback.Store {
			continue
		}
		if err := store.clearRuntime(context.Background(), runtime.ID); err != nil {
			return err
		}
	}
	return nil
}

func (n *nodeAgent) clearManifestChunks(runtime runtimeAssignment) error {
	manifest, err := parseBlobManifest(runtime.BlobManifestJSON)
	if err != nil {
		return err
	}
	if manifest == nil || len(manifest.Chunks) == 0 {
		return nil
	}
	if err := validateBlobManifestForRuntime(manifest, runtime.ID); err != nil {
		return err
	}
	store, err := n.blobStoreForManifest(manifest)
	if err != nil {
		return err
	}
	if err := store.deleteManifest(context.Background(), manifest); err != nil {
		return err
	}
	if fallback := manifest.MigrationFallback; fallback != nil {
		fallbackStore, err := n.blobStoreForManifest(fallback)
		if err != nil {
			return err
		}
		return fallbackStore.deleteManifest(context.Background(), fallback)
	}
	return nil
}

func sameBlobGeneration(left, right *blobManifest) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Store == right.Store && left.Bucket == right.Bucket && left.RuntimeID == right.RuntimeID && left.Namespace == right.Namespace && left.SnapshotID == right.SnapshotID
}

func (n *nodeAgent) runtimeBlobKeyWithContext(ctx context.Context, runtime runtimeAssignment) ([]byte, error) {
	if cached, ok := n.cachedRuntimeBlobKey(runtime.ID); ok {
		return cached, nil
	}
	envelope, verifier, expiresAt, err := n.fetchActiveBlobKey(ctx, runtime.ID)
	if err != nil {
		return nil, err
	}
	key, err := n.decryptBlobKeyEnvelope(envelope, verifier, expiresAt)
	if err != nil {
		return nil, err
	}
	n.cacheRuntimeBlobKey(runtime.ID, key)
	return key, nil
}

func (n *nodeAgent) cachedRuntimeBlobKey(runtimeID string) ([]byte, bool) {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return nil, false
	}
	n.runtimeBlobKeyMu.Lock()
	defer n.runtimeBlobKeyMu.Unlock()
	key, ok := n.runtimeBlobKeys[runtimeID]
	if !ok || len(key) == 0 {
		return nil, false
	}
	return append([]byte(nil), key...), true
}

func (n *nodeAgent) cacheRuntimeBlobKey(runtimeID string, key []byte) {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" || len(key) == 0 {
		return
	}
	n.runtimeBlobKeyMu.Lock()
	defer n.runtimeBlobKeyMu.Unlock()
	if n.runtimeBlobKeys == nil {
		n.runtimeBlobKeys = make(map[string][]byte)
	}
	if previous := n.runtimeBlobKeys[runtimeID]; len(previous) > 0 {
		clearBytes(previous)
	}
	n.runtimeBlobKeys[runtimeID] = append([]byte(nil), key...)
}

func (n *nodeAgent) clearCachedRuntimeBlobKey(runtimeID string) {
	n.runtimeBlobKeyMu.Lock()
	defer n.runtimeBlobKeyMu.Unlock()
	if key := n.runtimeBlobKeys[strings.TrimSpace(runtimeID)]; len(key) > 0 {
		clearBytes(key)
	}
	delete(n.runtimeBlobKeys, strings.TrimSpace(runtimeID))
}

func (n *nodeAgent) clearAllCachedRuntimeBlobKeys() {
	n.runtimeBlobKeyMu.Lock()
	defer n.runtimeBlobKeyMu.Unlock()
	for runtimeID, key := range n.runtimeBlobKeys {
		clearBytes(key)
		delete(n.runtimeBlobKeys, runtimeID)
	}
}

func (n *nodeAgent) fetchActiveBlobKey(ctx context.Context, runtimeID string) (blobKeyEnvelopePayload, string, time.Time, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		n.cfg.ControlPlaneURL+"/api/v1/internal/runtimes/"+url.PathEscape(runtimeID)+"/blob-key",
		nil,
	)
	if err != nil {
		return blobKeyEnvelopePayload{}, "", time.Time{}, err
	}
	if err := n.signControlPlaneRequest(req, nil, false); err != nil {
		return blobKeyEnvelopePayload{}, "", time.Time{}, err
	}

	resp, err := n.controlPlane.Do(req)
	if err != nil {
		return blobKeyEnvelopePayload{}, "", time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return blobKeyEnvelopePayload{}, "", time.Time{}, fmt.Errorf("fetch active blob key envelope: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	var payload struct {
		BlobKeyEnvelope blobKeyEnvelopePayload `json:"blob_key_envelope"`
		BlobKeyVerifier string                 `json:"blob_key_verifier"`
		BlobKeyExpires  time.Time              `json:"blob_key_expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return blobKeyEnvelopePayload{}, "", time.Time{}, err
	}
	if strings.TrimSpace(payload.BlobKeyEnvelope.Ciphertext) == "" ||
		strings.TrimSpace(payload.BlobKeyVerifier) == "" ||
		payload.BlobKeyExpires.IsZero() {
		return blobKeyEnvelopePayload{}, "", time.Time{}, errors.New("blob key envelope handoff response is incomplete")
	}
	return payload.BlobKeyEnvelope, strings.TrimSpace(payload.BlobKeyVerifier), payload.BlobKeyExpires, nil
}

func parseBlobManifest(raw *string) (*blobManifest, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}

	var manifest blobManifest
	if err := json.Unmarshal([]byte(*raw), &manifest); err != nil {
		return nil, fmt.Errorf("decode blob manifest: %w", err)
	}
	if manifest.Store == "" {
		manifest.Store = blobStoreLocal
	}
	return &manifest, nil
}

func marshalBlobManifest(manifest *blobManifest) string {
	if manifest == nil {
		return ""
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return ""
	}
	return string(payload)
}

func legacySnapshotPath(runtimeRoot, runtimeID string) string {
	return filepath.Join(runtimeRoot, runtimeID, "blob", "userdata.enc")
}

func (s *localBlobStore) kind() string {
	return blobStoreLocal
}

func (s *localBlobStore) preflight(ctx context.Context, report *blobPreflightReport) {
	if err := ctx.Err(); err != nil {
		report.addCheck("context", "fail", err.Error())
		return
	}
	if strings.TrimSpace(s.root) == "" {
		report.addCheck("root", "fail", "local blob root is empty")
		return
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		report.addCheck("root", "fail", err.Error())
		return
	}
	testPath := filepath.Join(s.root, ".preflight")
	if err := os.WriteFile(testPath, []byte("ok\n"), 0o600); err != nil {
		report.addCheck("write", "fail", err.Error())
		return
	}
	if err := os.Remove(testPath); err != nil {
		report.addCheck("cleanup", "warn", err.Error())
	}
	report.addCheck("local_disk", "pass", s.root)
}

func defaultBlobBucket(bucket string) string {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return "virtroid"
	}
	return bucket
}

func (s *localBlobStore) persistFromDir(ctx context.Context, runtimeID, dataDir string, masterKey []byte) (manifest *blobManifest, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.chunkSize <= 0 || s.chunkSize > maxBlobChunkSize {
		return nil, fmt.Errorf("invalid local blob chunk size %d", s.chunkSize)
	}

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("virtroid-%s-%d.enc", runtimeID, time.Now().UnixNano()))
	defer os.Remove(tempPath)

	snapshotKey := deriveRuntimeSnapshotKey(masterKey, runtimeID)
	defer clearBytes(snapshotKey)
	totalBytes, err := writeSnapshotWithContext(ctx, tempPath, dataDir, snapshotKey)
	if err != nil {
		return nil, err
	}

	snapshotID, err := newSnapshotID()
	if err != nil {
		return nil, err
	}
	namespace := deriveRuntimeBlobNamespace(masterKey, runtimeID)
	runtimeBaseDir := filepath.Join(s.root, namespace)
	if err := os.MkdirAll(runtimeBaseDir, 0o755); err != nil {
		return nil, err
	}
	runtimeDir := filepath.Join(runtimeBaseDir, snapshotID)
	stagingDir := filepath.Join(runtimeBaseDir, "."+snapshotID+".tmp")
	if err := os.Mkdir(stagingDir, 0o700); err != nil {
		return nil, err
	}

	file, err := os.Open(tempPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	manifest = &blobManifest{
		Version:     3,
		RuntimeID:   runtimeID,
		Namespace:   namespace,
		Store:       s.kind(),
		ObjectType:  "runtime-userdata",
		SnapshotID:  snapshotID,
		CreatedAt:   time.Now().UTC(),
		ChunkSize:   s.chunkSize,
		TotalBytes:  totalBytes,
		Compression: "gzip",
		Encryption:  authenticatedEncryption,
	}
	committed := false
	defer func() {
		if !committed {
			cleanupErr := os.RemoveAll(stagingDir)
			if cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("clean partial local snapshot: %w", cleanupErr))
			}
		}
	}()

	buffer := make([]byte, s.chunkSize)
	for index := 0; ; index++ {
		if index >= maxBlobManifestChunks {
			return nil, fmt.Errorf("snapshot exceeds maximum chunk count %d", maxBlobManifestChunks)
		}
		readBytes, readErr := io.ReadFull(file, buffer)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if errors.Is(readErr, io.ErrUnexpectedEOF) && readBytes == 0 {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, readErr
		}

		chunkPayload := bytes.Clone(buffer[:readBytes])
		chunkSum := sha256.Sum256(chunkPayload)
		chunkName := fmt.Sprintf("chunk-%05d.bin", index)
		chunkPath := filepath.Join(stagingDir, chunkName)
		if err := writeFileAndSync(chunkPath, chunkPayload, 0o600); err != nil {
			return nil, err
		}
		manifest.Chunks = append(manifest.Chunks, blobChunk{
			Index:  index,
			Key:    filepath.ToSlash(filepath.Join(namespace, snapshotID, chunkName)),
			Size:   int64(readBytes),
			SHA256: hex.EncodeToString(chunkSum[:]),
		})

		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}

	if len(manifest.Chunks) == 0 {
		return nil, errors.New("snapshot produced no chunks")
	}
	if err := authenticateBlobManifest(manifest, masterKey, runtimeID); err != nil {
		return nil, fmt.Errorf("authenticate local blob manifest: %w", err)
	}
	if err := validateBlobManifestForRuntime(manifest, runtimeID); err != nil {
		return nil, fmt.Errorf("validate completed local blob manifest: %w", err)
	}
	if err := syncDirectory(stagingDir); err != nil {
		return nil, fmt.Errorf("sync staged local snapshot: %w", err)
	}
	if err := os.Rename(stagingDir, runtimeDir); err != nil {
		return nil, fmt.Errorf("commit staged local snapshot: %w", err)
	}
	if err := syncDirectory(runtimeBaseDir); err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("sync local snapshot generation: %w", err)
	}
	committed = true
	return manifest, nil
}

func (s *localBlobStore) restoreToDir(ctx context.Context, runtimeID string, manifest *blobManifest, dataDir string, masterKey []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manifest == nil {
		return errSnapshotMissing
	}
	if manifest.Store != s.kind() {
		return fmt.Errorf("unsupported blob store kind %q", manifest.Store)
	}
	if err := validateBlobManifestForRuntime(manifest, runtimeID); err != nil {
		return err
	}
	if err := verifyBlobManifestAuthentication(manifest, masterKey, runtimeID); err != nil {
		return err
	}

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("virtroid-restore-%s-%d.enc", runtimeID, time.Now().UnixNano()))
	defer os.Remove(tempPath)

	tempFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	for _, chunk := range manifest.Chunks {
		chunkPath, err := s.blobObjectPath(chunk.Key)
		if err != nil {
			tempFile.Close()
			return err
		}
		chunkFile, err := os.Open(chunkPath)
		if err != nil {
			tempFile.Close()
			return err
		}
		copyErr := copyExpectedBlobChunk(tempFile, chunkFile, chunk)
		closeErr := chunkFile.Close()
		if copyErr != nil {
			tempFile.Close()
			return copyErr
		}
		if closeErr != nil {
			tempFile.Close()
			return closeErr
		}
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	snapshotKey := snapshotKeyForManifest(masterKey, runtimeID, manifest)
	defer clearBytes(snapshotKey)
	return restoreSnapshot(tempPath, dataDir, snapshotKey)
}

func (s *localBlobStore) deleteManifest(ctx context.Context, manifest *blobManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manifest == nil {
		return nil
	}
	for _, chunk := range manifest.Chunks {
		chunkPath, err := s.blobObjectPath(chunk.Key)
		if err != nil {
			return err
		}
		if err := os.Remove(chunkPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if len(manifest.Chunks) > 0 {
		firstPath, err := s.blobObjectPath(manifest.Chunks[0].Key)
		if err != nil {
			return err
		}
		_ = os.Remove(filepath.Dir(firstPath))
		_ = os.Remove(filepath.Dir(filepath.Dir(firstPath)))
	}
	return nil
}

func validateBlobManifest(manifest *blobManifest) error {
	return validateBlobManifestForRuntime(manifest, "")
}

func validateBlobManifestForRuntime(manifest *blobManifest, runtimeID string) error {
	if manifest == nil {
		return errSnapshotMissing
	}
	if manifest.Version != 1 && manifest.Version != 2 && manifest.Version != 3 {
		return fmt.Errorf("unsupported blob manifest version %d", manifest.Version)
	}
	if manifest.Version >= 2 {
		if strings.TrimSpace(manifest.RuntimeID) == "" {
			return errors.New("runtime-bound blob manifest has no runtime id")
		}
		if runtimeID != "" && manifest.RuntimeID != runtimeID {
			return fmt.Errorf("blob manifest runtime id %q does not match %q", manifest.RuntimeID, runtimeID)
		}
	}
	if manifest.Version == 3 {
		if manifest.Generation < 0 {
			return errors.New("authenticated blob manifest has an invalid generation")
		}
		if len(manifest.Namespace) != sha256.Size*2 {
			return errors.New("authenticated blob manifest has an invalid namespace")
		}
		if _, err := hex.DecodeString(manifest.Namespace); err != nil {
			return errors.New("authenticated blob manifest namespace is not hexadecimal")
		}
		if len(manifest.ManifestMAC) != sha256.Size*2 {
			return errors.New("authenticated blob manifest has an invalid manifest MAC")
		}
		if _, err := hex.DecodeString(manifest.ManifestMAC); err != nil {
			return errors.New("authenticated blob manifest MAC is not hexadecimal")
		}
	} else if manifest.Namespace != "" || manifest.ManifestMAC != "" {
		return errors.New("legacy blob manifest contains authenticated-manifest fields")
	}
	if manifest.ObjectType != "runtime-userdata" {
		return fmt.Errorf("unsupported blob object type %q", manifest.ObjectType)
	}
	expectedEncryption := "aes-ctr+hmac-sha256"
	if manifest.Version == 2 {
		expectedEncryption = runtimeBoundEncryption
	} else if manifest.Version == 3 {
		expectedEncryption = authenticatedEncryption
	}
	if manifest.Encryption != expectedEncryption {
		return fmt.Errorf("unsupported blob encryption %q", manifest.Encryption)
	}
	if manifest.Compression != "gzip" {
		return fmt.Errorf("unsupported blob compression %q", manifest.Compression)
	}
	if len(manifest.Chunks) == 0 {
		return errors.New("blob manifest has no chunks")
	}
	if len(manifest.Chunks) > maxBlobManifestChunks {
		return fmt.Errorf("blob manifest has too many chunks: %d", len(manifest.Chunks))
	}
	if manifest.ChunkSize <= 0 || manifest.ChunkSize > maxBlobChunkSize {
		return fmt.Errorf("blob manifest has invalid chunk size %d", manifest.ChunkSize)
	}
	if manifest.TotalBytes <= 0 || manifest.TotalBytes > maxBlobSnapshotBytes {
		return fmt.Errorf("blob manifest has invalid total size %d", manifest.TotalBytes)
	}
	if err := validateSnapshotID(manifest.SnapshotID); err != nil {
		return err
	}
	var totalBytes int64
	for index, chunk := range manifest.Chunks {
		if chunk.Index != index {
			return fmt.Errorf("blob manifest chunk index mismatch at %d", index)
		}
		if err := validateBlobChunkKey(manifest, runtimeID, index, chunk.Key); err != nil {
			return err
		}
		if chunk.Size <= 0 || chunk.Size > manifest.ChunkSize || chunk.Size > maxBlobChunkSize {
			return fmt.Errorf("blob manifest chunk %d has invalid size", index)
		}
		if len(chunk.SHA256) != sha256.Size*2 {
			return fmt.Errorf("blob manifest chunk %d has invalid hash", index)
		}
		if totalBytes > maxBlobSnapshotBytes-chunk.Size {
			return errors.New("blob manifest byte total overflows size limit")
		}
		totalBytes += chunk.Size
	}
	if manifest.TotalBytes != totalBytes {
		return fmt.Errorf("blob manifest byte total mismatch")
	}
	if manifest.MigrationFallback != nil {
		if manifest.Version != 3 {
			return errors.New("legacy blob manifest contains a migration fallback")
		}
		if manifest.MigrationFallback.MigrationFallback != nil {
			return errors.New("blob manifest migration fallback cannot be nested")
		}
		if manifest.MigrationFallback.Store == manifest.Store {
			return errors.New("blob manifest migration fallback must use a different store")
		}
		if err := validateBlobManifestForRuntime(manifest.MigrationFallback, runtimeID); err != nil {
			return fmt.Errorf("validate blob migration fallback: %w", err)
		}
	}
	return nil
}

func copyExpectedBlobChunk(dst io.Writer, src io.Reader, chunk blobChunk) error {
	if chunk.Size <= 0 || chunk.Size > maxBlobChunkSize {
		return fmt.Errorf("blob chunk %d has invalid expected size %d", chunk.Index, chunk.Size)
	}
	expectedHash, err := hex.DecodeString(chunk.SHA256)
	if err != nil || len(expectedHash) != sha256.Size {
		return fmt.Errorf("blob chunk %d has invalid expected hash", chunk.Index)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(dst, hasher), io.LimitReader(src, chunk.Size+1))
	if err != nil {
		return fmt.Errorf("read blob chunk %d: %w", chunk.Index, err)
	}
	if written != chunk.Size {
		return fmt.Errorf("blob chunk %d size mismatch: got %d want %d", chunk.Index, written, chunk.Size)
	}
	if !hmac.Equal(hasher.Sum(nil), expectedHash) {
		return fmt.Errorf("blob chunk %d integrity mismatch", chunk.Index)
	}
	return nil
}

func validateSnapshotID(snapshotID string) error {
	if strings.TrimSpace(snapshotID) == "" {
		return errors.New("blob manifest has empty snapshot id")
	}
	if strings.TrimSpace(snapshotID) != snapshotID ||
		snapshotID == "." ||
		snapshotID == ".." ||
		strings.Contains(snapshotID, "/") ||
		strings.Contains(snapshotID, "\\") {
		return fmt.Errorf("invalid blob snapshot id %q", snapshotID)
	}
	return nil
}

func validateBlobChunkKey(manifest *blobManifest, runtimeID string, index int, key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("blob manifest chunk %d has empty key", index)
	}
	if strings.TrimSpace(key) != key || filepath.IsAbs(filepath.FromSlash(key)) {
		return fmt.Errorf("blob manifest chunk %d has invalid key %q", index, key)
	}
	if runtimeID != "" {
		prefix := runtimeID
		if manifest != nil && manifest.Version == 3 {
			prefix = manifest.Namespace
		}
		expected := filepath.ToSlash(filepath.Join(prefix, manifest.SnapshotID, fmt.Sprintf("chunk-%05d.bin", index)))
		if key != expected {
			return fmt.Errorf("blob manifest chunk %d key %q does not match expected runtime path", index, key)
		}
	}
	return nil
}

func (s *localBlobStore) blobObjectPath(key string) (string, error) {
	relativePath := filepath.FromSlash(strings.TrimSpace(key))
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("invalid absolute blob object key %q", key)
	}
	return secureJoin(s.root, relativePath)
}

func (s *renterdBlobStore) kind() string {
	return blobStoreRenterd
}

func (s *renterdBlobStore) preflight(ctx context.Context, report *blobPreflightReport) {
	if address := strings.TrimSpace(s.walletAddress); address != "" {
		report.WalletAddress = address
	}
	if strings.TrimSpace(s.workerURL) == "" {
		report.addCheck("worker_url", "fail", "NODE_SIA_RENTERD_WORKER_URL is required")
		return
	}
	report.addCheck("worker_url", "pass", s.workerURL)
	if strings.TrimSpace(s.password) == "" {
		report.addCheck("api_password", "fail", "renterd API password secret is required")
		return
	}
	report.addCheck("api_password", "pass", "configured")
	if s.minShards < 0 || s.totalShards < 0 || (s.totalShards > 0 && s.minShards > s.totalShards) {
		report.addCheck("redundancy", "fail", fmt.Sprintf("invalid min/total shard configuration %d/%d", s.minShards, s.totalShards))
		return
	}
	if (s.minShards == 0) != (s.totalShards == 0) {
		report.addCheck("redundancy", "fail", "min and total shards must either both be configured or both use renterd defaults")
		return
	}
	if s.totalShards > 0 {
		report.addCheck("redundancy", "pass", fmt.Sprintf("%d-of-%d shards", s.minShards, s.totalShards))
	} else {
		report.addCheck("redundancy", "pass", "renterd upload defaults")
	}

	var consensus map[string]any
	if err := s.getRenterdJSON(ctx, "/api/bus/consensus/state", &consensus); err != nil {
		report.addCheck("consensus_state", "fail", err.Error())
	} else if synced, found := findBoolValue(consensus, "synced"); found && !synced {
		report.addCheck("consensus_state", "fail", "renterd consensus is not synced")
	} else if found {
		report.addCheck("consensus_state", "pass", "synced")
	} else {
		report.addCheck("consensus_state", "warn", "endpoint reachable; synced field was not present")
	}

	var autopilot map[string]any
	if err := s.getRenterdJSON(ctx, "/api/autopilot/state", &autopilot); err != nil {
		report.addCheck("autopilot", "fail", err.Error())
	} else if enabled, found := findBoolValue(autopilot, "enabled"); !found {
		report.addCheck("autopilot", "fail", "autopilot state did not report whether maintenance is enabled")
	} else if !enabled {
		report.addCheck("autopilot", "fail", "renterd autopilot is disabled; contracts and data health will not be maintained")
	} else {
		report.addCheck("autopilot", "pass", "enabled")
	}

	var buckets []struct {
		Name   string `json:"name"`
		Policy struct {
			PublicReadAccess bool `json:"publicReadAccess"`
		} `json:"policy"`
	}
	if err := s.getRenterdJSON(ctx, "/api/bus/buckets", &buckets); err != nil {
		report.addCheck("bucket", "fail", err.Error())
	} else {
		found := false
		for _, bucket := range buckets {
			if bucket.Name != s.bucketName() {
				continue
			}
			found = true
			if bucket.Policy.PublicReadAccess {
				report.addCheck("bucket", "fail", fmt.Sprintf("bucket %q permits public reads", bucket.Name))
			} else {
				report.addCheck("bucket", "pass", fmt.Sprintf("bucket %q exists and is private", bucket.Name))
			}
			break
		}
		if !found {
			report.addCheck("bucket", "fail", fmt.Sprintf("bucket %q does not exist", s.bucketName()))
		}
	}

	var wallet map[string]any
	if err := s.getRenterdJSON(ctx, "/api/bus/wallet", &wallet); err != nil {
		report.addCheck("wallet", "fail", err.Error())
	} else if nonZeroCurrencyValue(wallet, "siacoins", "balance", "spendable") {
		if report.WalletAddress == "" {
			report.WalletAddress = findStringValue(wallet, "address", "walletAddress", "receiveAddress")
		}
		report.addCheck("wallet", "pass", "funded")
	} else {
		if report.WalletAddress == "" {
			report.WalletAddress = findStringValue(wallet, "address", "walletAddress", "receiveAddress")
		}
		report.addCheck("wallet", "fail", "wallet endpoint reachable; non-zero spendable balance was not detected")
	}
	if report.WalletAddress == "" {
		report.WalletAddress = s.fetchWalletAddress(ctx)
	}

	contracts, err := s.activeContracts(ctx)
	if err != nil {
		report.addCheck("active_contracts", "fail", err.Error())
	} else if len(contracts) == 0 {
		report.addCheck("active_contracts", "fail", "no active renterd contracts")
	} else if s.totalShards > 0 && len(contracts) < s.totalShards {
		report.addCheck("active_contracts", "fail", fmt.Sprintf("%d active contracts; at least %d are required for configured total shards", len(contracts), s.totalShards))
	} else {
		report.addCheck("active_contracts", "pass", fmt.Sprintf("%d active contracts", len(contracts)))
	}
	if report.OK {
		remaining, err := s.drainPendingDeletions(ctx)
		switch {
		case err != nil:
			report.addCheck("pending_deletions", "warn", fmt.Sprintf("%d cleanup records remain: %v", remaining, err))
		case remaining > 0:
			report.addCheck("pending_deletions", "warn", fmt.Sprintf("%d cleanup records remain", remaining))
		default:
			report.addCheck("pending_deletions", "pass", "no deferred renterd deletions")
		}
	}
}

func (s *renterdBlobStore) activeContracts(ctx context.Context) ([]json.RawMessage, error) {
	values := url.Values{}
	values.Set("filtermode", "active")
	var contracts []json.RawMessage
	status, err := s.doRenterdRequest(ctx, http.MethodGet, "/api/bus/contracts", values, nil, "", &contracts)
	if err == nil {
		return contracts, nil
	}
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		return nil, err
	}
	var legacy []json.RawMessage
	if legacyErr := s.getRenterdJSON(ctx, "/api/bus/contracts/active", &legacy); legacyErr != nil {
		return nil, errors.Join(err, legacyErr)
	}
	return legacy, nil
}

func (s *renterdBlobStore) fetchWalletAddress(ctx context.Context) string {
	var value any
	if err := s.getRenterdJSON(ctx, "/api/bus/wallet/address", &value); err != nil {
		return ""
	}
	return normalizeWalletAddress(value)
}

func (s *renterdBlobStore) persistFromDir(ctx context.Context, runtimeID, dataDir string, masterKey []byte) (manifest *blobManifest, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.workerURL) == "" {
		return nil, errors.New("renterd worker url is required")
	}
	if s.chunkSize <= 0 || s.chunkSize > maxBlobChunkSize {
		return nil, fmt.Errorf("invalid renterd blob chunk size %d", s.chunkSize)
	}

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("virtroid-%s-%d.enc", runtimeID, time.Now().UnixNano()))
	defer os.Remove(tempPath)

	snapshotKey := deriveRuntimeSnapshotKey(masterKey, runtimeID)
	defer clearBytes(snapshotKey)
	totalBytes, err := writeSnapshotWithContext(ctx, tempPath, dataDir, snapshotKey)
	if err != nil {
		return nil, err
	}

	snapshotID, err := newSnapshotID()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(tempPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	manifest = &blobManifest{
		Version:     3,
		RuntimeID:   runtimeID,
		Namespace:   deriveRuntimeBlobNamespace(masterKey, runtimeID),
		Store:       s.kind(),
		Bucket:      s.bucketName(),
		ObjectType:  "runtime-userdata",
		SnapshotID:  snapshotID,
		CreatedAt:   time.Now().UTC(),
		ChunkSize:   s.chunkSize,
		TotalBytes:  totalBytes,
		Compression: "gzip",
		Encryption:  authenticatedEncryption,
	}
	cleanupCandidate := manifest
	committed := false
	defer func() {
		if !committed && len(cleanupCandidate.Chunks) > 0 {
			cleanupManifest := *cleanupCandidate
			cleanupManifest.Chunks = append([]blobChunk(nil), cleanupCandidate.Chunks...)
			cleanupManifest.TotalBytes = 0
			for _, chunk := range cleanupManifest.Chunks {
				cleanupManifest.TotalBytes += chunk.Size
			}
			if cleanupAuthErr := authenticateBlobManifest(&cleanupManifest, masterKey, runtimeID); cleanupAuthErr != nil {
				err = errors.Join(err, fmt.Errorf("authenticate partial renterd cleanup record: %w", cleanupAuthErr))
			} else if cleanupErr := s.deleteManifest(context.WithoutCancel(ctx), &cleanupManifest); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("clean partial renterd snapshot: %w", cleanupErr))
			}
		}
	}()

	buffer := make([]byte, s.chunkSize)
	for index := 0; ; index++ {
		if index >= maxBlobManifestChunks {
			return nil, fmt.Errorf("snapshot exceeds maximum chunk count %d", maxBlobManifestChunks)
		}
		readBytes, readErr := io.ReadFull(file, buffer)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if errors.Is(readErr, io.ErrUnexpectedEOF) && readBytes == 0 {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, readErr
		}

		chunkPayload := bytes.Clone(buffer[:readBytes])
		chunkSum := sha256.Sum256(chunkPayload)
		chunkName := fmt.Sprintf("chunk-%05d.bin", index)
		chunkKey := filepath.ToSlash(filepath.Join(manifest.Namespace, snapshotID, chunkName))
		manifest.Chunks = append(manifest.Chunks, blobChunk{
			Index:  index,
			Key:    chunkKey,
			Size:   int64(readBytes),
			SHA256: hex.EncodeToString(chunkSum[:]),
		})
		if err := s.putObject(ctx, chunkKey, chunkPayload); err != nil {
			return nil, err
		}

		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}

	if len(manifest.Chunks) == 0 {
		return nil, errors.New("snapshot produced no chunks")
	}
	if err := authenticateBlobManifest(manifest, masterKey, runtimeID); err != nil {
		return nil, fmt.Errorf("authenticate renterd blob manifest: %w", err)
	}
	if err := validateBlobManifestForRuntime(manifest, runtimeID); err != nil {
		return nil, fmt.Errorf("validate completed renterd blob manifest: %w", err)
	}
	committed = true
	return manifest, nil
}

func (s *renterdBlobStore) restoreToDir(ctx context.Context, runtimeID string, manifest *blobManifest, dataDir string, masterKey []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manifest == nil {
		return errSnapshotMissing
	}
	if manifest.Store != s.kind() {
		return fmt.Errorf("unsupported blob store kind %q", manifest.Store)
	}
	if err := validateBlobManifestForRuntime(manifest, runtimeID); err != nil {
		return err
	}
	if err := verifyBlobManifestAuthentication(manifest, masterKey, runtimeID); err != nil {
		return err
	}

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("virtroid-restore-%s-%d.enc", runtimeID, time.Now().UnixNano()))
	defer os.Remove(tempPath)

	tempFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	for _, chunk := range manifest.Chunks {
		if err := s.copyObject(ctx, manifestBucket(manifest, s.bucketName()), chunk, tempFile); err != nil {
			tempFile.Close()
			return err
		}
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	snapshotKey := snapshotKeyForManifest(masterKey, runtimeID, manifest)
	defer clearBytes(snapshotKey)
	return restoreSnapshot(tempPath, dataDir, snapshotKey)
}

func (s *renterdBlobStore) clearRuntime(ctx context.Context, runtimeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtimeID == "" {
		return nil
	}
	return s.deletePrefix(ctx, runtimeID+"/")
}

func (s *renterdBlobStore) pruneRuntime(ctx context.Context, runtimeID, namespace, keepSnapshotID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtimeID == "" || keepSnapshotID == "" {
		return nil
	}
	prefixRoot := strings.TrimSpace(runtimeID)
	if strings.TrimSpace(namespace) != "" {
		prefixRoot = strings.TrimSpace(namespace)
	}
	runtimePrefix := filepath.ToSlash(filepath.Join(prefixRoot)) + "/"
	keepPrefix := filepath.ToSlash(filepath.Join(prefixRoot, keepSnapshotID)) + "/"
	keys, err := s.listObjectKeys(ctx, runtimePrefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, runtimePrefix) {
			return fmt.Errorf("renterd returned object %q outside requested prefix %q", key, runtimePrefix)
		}
		if strings.HasPrefix(key, keepPrefix) {
			continue
		}
		if err := s.deleteObject(ctx, s.bucketName(), key); err != nil {
			return err
		}
	}
	return nil
}

func (s *renterdBlobStore) putObject(ctx context.Context, key string, payload []byte) error {
	requestURL, err := s.objectURL(s.bucketName(), key)
	if err != nil {
		return err
	}
	values := requestURL.Query()
	if s.minShards > 0 {
		values.Set("minshards", strconv.Itoa(s.minShards))
	}
	if s.totalShards > 0 {
		values.Set("totalshards", strconv.Itoa(s.totalShards))
	}
	if s.contractSet != "" {
		values.Set("contractset", s.contractSet)
	}
	requestURL.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(payload))
	s.authorize(req)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("renterd upload object %q failed: status=%d body=%s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *renterdBlobStore) copyObject(ctx context.Context, bucket string, chunk blobChunk, dst io.Writer) error {
	requestURL, err := s.objectURL(bucket, chunk.Key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	s.authorize(req)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("renterd download object %q failed: status=%d body=%s", chunk.Key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if resp.ContentLength >= 0 && resp.ContentLength != chunk.Size {
		return fmt.Errorf("renterd download object %q size mismatch: content-length=%d manifest=%d", chunk.Key, resp.ContentLength, chunk.Size)
	}
	return copyExpectedBlobChunk(dst, resp.Body, chunk)
}

func (s *renterdBlobStore) deletePrefix(ctx context.Context, prefix string) error {
	if strings.TrimSpace(prefix) == "" {
		return errors.New("refusing to delete an empty renterd object prefix")
	}
	payload, err := json.Marshal(map[string]string{
		"bucket": s.bucketName(),
		"prefix": prefix,
	})
	if err != nil {
		return err
	}
	status, err := s.doRenterdRequest(ctx, http.MethodPost, "/api/bus/objects/remove", nil, bytes.NewReader(payload), "application/json", nil)
	if err == nil {
		return nil
	}
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		return err
	}

	values := url.Values{}
	values.Set("bucket", s.bucketName())
	values.Set("batch", "true")
	legacyPath := "/api/bus/objects/" + url.PathEscape(strings.TrimPrefix(prefix, "/"))
	_, legacyErr := s.doRenterdRequest(ctx, http.MethodDelete, legacyPath, values, nil, "", nil)
	if legacyErr != nil {
		return errors.Join(err, legacyErr)
	}
	return nil
}

func (s *renterdBlobStore) deleteManifest(ctx context.Context, manifest *blobManifest) error {
	if manifest == nil {
		return nil
	}
	if err := s.deleteManifestObjects(ctx, manifest); err != nil {
		journalErr := s.enqueuePendingDeletion(manifest)
		return errors.Join(err, journalErr)
	}
	return nil
}

func (s *renterdBlobStore) listObjectKeys(ctx context.Context, prefix string) ([]string, error) {
	keys, status, err := s.listObjectKeysV2(ctx, prefix)
	if err == nil {
		return keys, nil
	}
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		return nil, err
	}
	legacyKeys, legacyErr := s.listObjectKeysV1(ctx, prefix)
	if legacyErr != nil {
		return nil, errors.Join(err, legacyErr)
	}
	return legacyKeys, nil
}

func (s *renterdBlobStore) listObjectKeysV2(ctx context.Context, prefix string) ([]string, int, error) {
	var keys []string
	marker := ""
	for {
		values := url.Values{}
		values.Set("bucket", s.bucketName())
		values.Set("limit", "1000")
		values.Set("sortby", "name")
		values.Set("sortdir", "asc")
		if marker != "" {
			values.Set("marker", marker)
		}
		var page struct {
			HasMore    bool   `json:"hasMore"`
			NextMarker string `json:"nextMarker"`
			Objects    []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"objects"`
		}
		apiPath := "/api/bus/objects/" + url.PathEscape(strings.TrimPrefix(prefix, "/"))
		status, err := s.doRenterdRequest(ctx, http.MethodGet, apiPath, values, nil, "", &page)
		if err != nil {
			return nil, status, err
		}
		for _, object := range page.Objects {
			key := strings.TrimSpace(object.Key)
			if key == "" {
				key = strings.TrimSpace(object.Name)
			}
			if key == "" {
				return nil, status, errors.New("renterd object listing returned an empty key")
			}
			keys = append(keys, key)
			if len(keys) > maxRenterdGCObjectCount {
				return nil, status, fmt.Errorf("renterd object listing exceeded GC limit %d", maxRenterdGCObjectCount)
			}
		}
		if !page.HasMore {
			return keys, status, nil
		}
		if page.NextMarker == "" || page.NextMarker == marker {
			return nil, status, errors.New("renterd object listing did not advance its marker")
		}
		marker = page.NextMarker
	}
}

func (s *renterdBlobStore) listObjectKeysV1(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	marker := ""
	for {
		payload, err := json.Marshal(map[string]any{
			"bucket":  s.bucketName(),
			"limit":   1000,
			"prefix":  prefix,
			"marker":  marker,
			"sortBy":  "name",
			"sortDir": "asc",
		})
		if err != nil {
			return nil, err
		}
		var page struct {
			HasMore    bool   `json:"hasMore"`
			NextMarker string `json:"nextMarker"`
			Objects    []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"objects"`
		}
		_, err = s.doRenterdRequest(ctx, http.MethodPost, "/api/bus/objects/list", nil, bytes.NewReader(payload), "application/json", &page)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Objects {
			key := strings.TrimSpace(object.Key)
			if key == "" {
				key = strings.TrimSpace(object.Name)
			}
			if key == "" {
				return nil, errors.New("legacy renterd object listing returned an empty key")
			}
			keys = append(keys, key)
			if len(keys) > maxRenterdGCObjectCount {
				return nil, fmt.Errorf("legacy renterd object listing exceeded GC limit %d", maxRenterdGCObjectCount)
			}
		}
		if !page.HasMore {
			return keys, nil
		}
		if page.NextMarker == "" || page.NextMarker == marker {
			return nil, errors.New("legacy renterd object listing did not advance its marker")
		}
		marker = page.NextMarker
	}
}

func (s *renterdBlobStore) doRenterdRequest(
	ctx context.Context,
	method string,
	apiPath string,
	values url.Values,
	body io.Reader,
	contentType string,
	target any,
) (int, error) {
	requestURL, err := url.Parse(strings.TrimRight(s.workerURL, "/") + apiPath)
	if err != nil {
		return 0, err
	}
	if requestURL.Scheme == "" || requestURL.Host == "" {
		return 0, fmt.Errorf("invalid renterd worker url %q", s.workerURL)
	}
	if values != nil {
		requestURL.RawQuery = values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	s.authorize(req)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, fmt.Errorf("renterd %s %s failed: status=%d body=%s", method, apiPath, resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxRenterdListResponse+1))
	if err := decoder.Decode(target); err != nil {
		return resp.StatusCode, fmt.Errorf("decode renterd %s %s: %w", method, apiPath, err)
	}
	return resp.StatusCode, nil
}

func (s *renterdBlobStore) deleteObject(ctx context.Context, bucket, key string) error {
	requestURL, err := s.objectURL(bucket, key)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, requestURL.String(), nil)
	if err != nil {
		return err
	}
	s.authorize(req)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("renterd delete object %q failed: status=%d body=%s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *renterdBlobStore) getRenterdJSON(ctx context.Context, apiPath string, target any) error {
	requestURL, err := url.Parse(strings.TrimRight(s.workerURL, "/"))
	if err != nil {
		return err
	}
	if requestURL.Scheme == "" || requestURL.Host == "" {
		return fmt.Errorf("invalid renterd worker url %q", s.workerURL)
	}
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + apiPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	s.authorize(req)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("renterd GET %s failed: status=%d body=%s", apiPath, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode renterd GET %s: %w", apiPath, err)
	}
	return nil
}

func (s *renterdBlobStore) objectURL(bucket, key string) (*url.URL, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("renterd object key is required")
	}
	parsed, err := url.Parse(strings.TrimRight(s.workerURL, "/"))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid renterd worker url %q", s.workerURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/worker/objects/" + escapeObjectPath(key)
	values := parsed.Query()
	values.Set("bucket", defaultBlobBucket(bucket))
	parsed.RawQuery = values.Encode()
	return parsed, nil
}

func (s *renterdBlobStore) bucketName() string {
	return defaultBlobBucket(s.bucket)
}

func (s *renterdBlobStore) authorize(req *http.Request) {
	if s.password != "" {
		req.SetBasicAuth("", s.password)
	}
}

func escapeObjectPath(key string) string {
	parts := strings.Split(filepath.ToSlash(strings.TrimSpace(key)), "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func manifestBucket(manifest *blobManifest, fallback string) string {
	if manifest != nil && strings.TrimSpace(manifest.Bucket) != "" {
		return manifest.Bucket
	}
	return fallback
}

func findBoolValue(value any, key string) (bool, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for currentKey, currentValue := range typed {
			if strings.EqualFold(currentKey, key) {
				if boolValue, ok := currentValue.(bool); ok {
					return boolValue, true
				}
			}
			if nested, ok := findBoolValue(currentValue, key); ok {
				return nested, true
			}
		}
	case []any:
		for _, item := range typed {
			if nested, ok := findBoolValue(item, key); ok {
				return nested, true
			}
		}
	}
	return false, false
}

func findStringValue(value any, keys ...string) string {
	wanted := map[string]struct{}{}
	for _, key := range keys {
		wanted[strings.ToLower(key)] = struct{}{}
	}
	return findStringValueByKeys(value, wanted)
}

func findStringValueByKeys(value any, wanted map[string]struct{}) string {
	switch typed := value.(type) {
	case map[string]any:
		for key, currentValue := range typed {
			if _, ok := wanted[strings.ToLower(key)]; ok {
				if found := normalizeWalletAddress(currentValue); found != "" {
					return found
				}
			}
			if nested := findStringValueByKeys(currentValue, wanted); nested != "" {
				return nested
			}
		}
	case []any:
		for _, item := range typed {
			if nested := findStringValueByKeys(item, wanted); nested != "" {
				return nested
			}
		}
	}
	return ""
}

func normalizeWalletAddress(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"address", "walletAddress", "receiveAddress"} {
			if found, ok := typed[key]; ok {
				if value := normalizeWalletAddress(found); value != "" {
					return value
				}
			}
		}
	case []any:
		for _, item := range typed {
			if value := normalizeWalletAddress(item); value != "" {
				return value
			}
		}
	}
	return ""
}

func nonZeroCurrencyValue(value any, keys ...string) bool {
	wanted := map[string]struct{}{}
	for _, key := range keys {
		wanted[strings.ToLower(key)] = struct{}{}
	}
	return containsNonZeroCurrencyValue(value, wanted)
}

func containsNonZeroCurrencyValue(value any, wanted map[string]struct{}) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, currentValue := range typed {
			if _, ok := wanted[strings.ToLower(key)]; ok && valueLooksNonZero(currentValue) {
				return true
			}
			if containsNonZeroCurrencyValue(currentValue, wanted) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if containsNonZeroCurrencyValue(item, wanted) {
				return true
			}
		}
	}
	return false
}

func valueLooksNonZero(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimLeft(strings.TrimSpace(typed), "0") != ""
	case float64:
		return typed > 0
	case int:
		return typed > 0
	case int64:
		return typed > 0
	case json.Number:
		return strings.TrimLeft(strings.TrimSpace(typed.String()), "0") != ""
	}
	return false
}

func (s *localBlobStore) clearRuntime(ctx context.Context, runtimeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtimeID == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.root, runtimeID))
}

func (s *localBlobStore) pruneRuntime(ctx context.Context, runtimeID, namespace, keepSnapshotID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	prefixRoot := strings.TrimSpace(runtimeID)
	if strings.TrimSpace(namespace) != "" {
		prefixRoot = strings.TrimSpace(namespace)
	}
	runtimeDir := filepath.Join(s.root, prefixRoot)
	entries, err := os.ReadDir(runtimeDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keepSnapshotID {
			continue
		}
		if err := os.RemoveAll(filepath.Join(runtimeDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func newSnapshotID() (string, error) {
	randomBytes := make([]byte, 6)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(randomBytes)), nil
}

func writeSnapshot(snapshotPath, dataDir string, masterKey []byte) (int64, error) {
	return writeSnapshotWithContext(context.Background(), snapshotPath, dataDir, masterKey)
}

func writeSnapshotWithContext(ctx context.Context, snapshotPath, dataDir string, masterKey []byte) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		return 0, err
	}

	file, err := os.CreateTemp(filepath.Dir(snapshotPath), ".snapshot-*.tmp")
	if err != nil {
		return 0, err
	}
	tempPath := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(tempPath)
		return 0, err
	}

	closeWithError := func(sourceErr error) (int64, error) {
		_ = file.Close()
		_ = os.Remove(tempPath)
		return 0, sourceErr
	}

	boundedWriter := &boundedSnapshotWriter{writer: file, remaining: maxBlobSnapshotBytes}
	header, writer, err := newEncryptedWriter(boundedWriter, masterKey)
	if err != nil {
		return closeWithError(err)
	}
	if _, err := boundedWriter.Write(header); err != nil {
		return closeWithError(err)
	}

	gzipWriter := gzip.NewWriter(writer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := addDirectoryToTar(ctx, tarWriter, dataDir); err != nil {
		return closeWithError(err)
	}
	if err := tarWriter.Close(); err != nil {
		return closeWithError(err)
	}
	if err := gzipWriter.Close(); err != nil {
		return closeWithError(err)
	}
	if err := writer.Close(); err != nil {
		return closeWithError(err)
	}
	if err := file.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := file.Close(); err != nil {
		return closeWithError(err)
	}
	if err := os.Rename(tempPath, snapshotPath); err != nil {
		return 0, err
	}
	if err := syncDirectory(filepath.Dir(snapshotPath)); err != nil {
		return 0, err
	}

	info, err := os.Stat(snapshotPath)
	if err != nil {
		return 0, err
	}
	if info.Size() > maxBlobSnapshotBytes {
		_ = os.Remove(snapshotPath)
		return 0, errBlobSnapshotTooLarge
	}
	return info.Size(), nil
}

func restoreSnapshot(snapshotPath, dataDir string, masterKey []byte) error {
	file, err := os.Open(snapshotPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return errSnapshotMissing
		}
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxBlobSnapshotBytes {
		return fmt.Errorf("snapshot size %d exceeds maximum %d", info.Size(), maxBlobSnapshotBytes)
	}
	headerLen := int64(len(snapshotMagic) + 32 + aes.BlockSize)
	if info.Size() <= headerLen+snapshotTagSize {
		return fmt.Errorf("snapshot is truncated")
	}

	header := make([]byte, headerLen)
	if _, err := io.ReadFull(file, header); err != nil {
		return err
	}
	if string(header[:len(snapshotMagic)]) != snapshotMagic {
		return fmt.Errorf("snapshot header mismatch")
	}

	salt := header[len(snapshotMagic) : len(snapshotMagic)+32]
	nonce := header[len(snapshotMagic)+32:]
	encryptionKey, macKey := deriveBlobKeys(masterKey, salt)
	defer clearBytes(encryptionKey)
	defer clearBytes(macKey)
	ciphertextLen := info.Size() - headerLen - snapshotTagSize

	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, macKey)
	_, _ = mac.Write(header)

	if _, err := io.Copy(mac, io.LimitReader(file, ciphertextLen)); err != nil {
		return err
	}
	tag := make([]byte, snapshotTagSize)
	if _, err := io.ReadFull(file, tag); err != nil {
		return err
	}
	if !hmac.Equal(tag, mac.Sum(nil)) {
		return fmt.Errorf("snapshot integrity check failed")
	}
	if _, err := file.Seek(headerLen, io.SeekStart); err != nil {
		return err
	}

	restoreDir := fmt.Sprintf("%s.restore-%d", dataDir, time.Now().UnixNano())
	if err := os.RemoveAll(restoreDir); err != nil {
		return err
	}
	defer os.RemoveAll(restoreDir)
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		return err
	}

	ciphertextReader := io.LimitReader(file, ciphertextLen)
	streamReader := &cipher.StreamReader{
		S: cipher.NewCTR(block, nonce),
		R: ciphertextReader,
	}

	gzipReader, err := gzip.NewReader(streamReader)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	if err := extractTarToDir(tar.NewReader(gzipReader), restoreDir); err != nil {
		return err
	}

	if err := os.RemoveAll(dataDir); err != nil {
		return err
	}
	if err := os.Rename(restoreDir, dataDir); err != nil {
		return err
	}
	return nil
}

type encryptedWriteCloser struct {
	writer io.Writer
	mac    hashState
	file   io.Writer
}

type hashState interface {
	Write(p []byte) (int, error)
	Sum(b []byte) []byte
}

func newEncryptedWriter(file io.Writer, masterKey []byte) ([]byte, io.WriteCloser, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aes.BlockSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}

	header := append([]byte(snapshotMagic), salt...)
	header = append(header, nonce...)
	encryptionKey, macKey := deriveBlobKeys(masterKey, salt)
	defer clearBytes(encryptionKey)
	defer clearBytes(macKey)
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, nil, err
	}

	mac := hmac.New(sha256.New, macKey)
	_, _ = mac.Write(header)
	streamWriter := &cipher.StreamWriter{
		S: cipher.NewCTR(block, nonce),
		W: io.MultiWriter(file, mac),
	}
	return header, &encryptedWriteCloser{
		writer: streamWriter,
		mac:    mac,
		file:   file,
	}, nil
}

func (w *encryptedWriteCloser) Write(payload []byte) (int, error) {
	return w.writer.Write(payload)
}

func (w *encryptedWriteCloser) Close() error {
	_, err := w.file.Write(w.mac.Sum(nil))
	return err
}

func deriveBlobKeys(masterKey, salt []byte) ([]byte, []byte) {
	encryptionMaterial := sha256.Sum256(append(bytes.Clone(masterKey), salt...))
	macMaterial := sha256.Sum256(append(bytes.Clone(salt), masterKey...))
	return encryptionMaterial[:], macMaterial[:]
}

func deriveRuntimeSnapshotKey(masterKey []byte, runtimeID string) []byte {
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte(runtimeSnapshotKeyContext))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strings.TrimSpace(runtimeID)))
	return mac.Sum(nil)
}

func deriveRuntimeBlobNamespace(masterKey []byte, runtimeID string) string {
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte(runtimeBlobNamespaceCtx))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strings.TrimSpace(runtimeID)))
	return hex.EncodeToString(mac.Sum(nil))
}

func runtimeManifestMACKey(masterKey []byte, runtimeID string) []byte {
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte(runtimeManifestMACCtx))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strings.TrimSpace(runtimeID)))
	return mac.Sum(nil)
}

func blobManifestMACPayload(manifest *blobManifest) ([]byte, error) {
	if manifest == nil {
		return nil, errSnapshotMissing
	}
	unsigned := *manifest
	unsigned.ManifestMAC = ""
	return json.Marshal(unsigned)
}

func authenticateBlobManifest(manifest *blobManifest, masterKey []byte, runtimeID string) error {
	if manifest == nil || manifest.Version != 3 {
		return errors.New("only version 3 blob manifests can be authenticated")
	}
	payload, err := blobManifestMACPayload(manifest)
	if err != nil {
		return err
	}
	key := runtimeManifestMACKey(masterKey, runtimeID)
	defer clearBytes(key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	manifest.ManifestMAC = hex.EncodeToString(mac.Sum(nil))
	return nil
}

func verifyBlobManifestAuthentication(manifest *blobManifest, masterKey []byte, runtimeID string) error {
	if manifest == nil || manifest.Version < 3 {
		return nil
	}
	provided, err := hex.DecodeString(manifest.ManifestMAC)
	if err != nil || len(provided) != sha256.Size {
		return errors.New("blob manifest authentication is invalid")
	}
	payload, err := blobManifestMACPayload(manifest)
	if err != nil {
		return err
	}
	key := runtimeManifestMACKey(masterKey, runtimeID)
	defer clearBytes(key)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return errors.New("blob manifest authentication failed")
	}
	if expected := deriveRuntimeBlobNamespace(masterKey, runtimeID); !hmac.Equal([]byte(manifest.Namespace), []byte(expected)) {
		return errors.New("blob manifest namespace does not match the runtime key")
	}
	return nil
}

func snapshotKeyForManifest(masterKey []byte, runtimeID string, manifest *blobManifest) []byte {
	if manifest != nil && manifest.Version >= 2 {
		return deriveRuntimeSnapshotKey(masterKey, runtimeID)
	}
	return append([]byte(nil), masterKey...)
}

func addDirectoryToTar(ctx context.Context, writer *tar.Writer, root string) (resultErr error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("snapshot source root must be a non-symlink directory")
	}
	// Keep both directory traversal and every subsequent open anchored to one
	// directory handle. Guest-controlled userdata may otherwise replace a path
	// between inspection and os.Open and redirect a root node process elsewhere.
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, sourceRoot.Close())
	}()
	openedRootInfo, err := sourceRoot.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect opened snapshot source root: %w", err)
	}
	if !openedRootInfo.IsDir() || !os.SameFile(rootInfo, openedRootInfo) {
		return errors.New("snapshot source root changed while it was being opened")
	}
	return addRootToTar(ctx, writer, sourceRoot)
}

func addRootToTar(ctx context.Context, writer *tar.Writer, sourceRoot *os.Root) error {
	return addRootToTarWithLimits(ctx, writer, sourceRoot, maxRestoredFileCount, snapshotReadDirBatchSize)
}

type snapshotTarWalker struct {
	ctx         context.Context
	writer      *tar.Writer
	maxEntries  int
	readBatch   int
	entryCount  int
	sourceBytes int64
}

func addRootToTarWithLimits(ctx context.Context, writer *tar.Writer, sourceRoot *os.Root, maxEntries, readBatch int) error {
	if ctx == nil || writer == nil || sourceRoot == nil {
		return errors.New("snapshot creation requires an archive writer and source root")
	}
	if maxEntries <= 0 || readBatch <= 0 {
		return errors.New("snapshot entry and directory-read limits must be positive")
	}
	walker := &snapshotTarWalker{
		ctx:        ctx,
		writer:     writer,
		maxEntries: maxEntries,
		readBatch:  readBatch,
	}
	return walker.walkDirectory(sourceRoot, "", 0)
}

func (w *snapshotTarWalker) walkDirectory(sourceRoot *os.Root, archivePrefix string, depth int) (resultErr error) {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if depth > maxSnapshotDirectoryDepth {
		return fmt.Errorf("snapshot source exceeds maximum directory depth %d", maxSnapshotDirectoryDepth)
	}
	directory, err := sourceRoot.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open snapshot source directory %q: %w", archivePrefix, err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, directory.Close())
	}()

	for {
		entries, readErr := directory.ReadDir(w.readBatch)
		for _, entry := range entries {
			if err := w.addEntry(sourceRoot, archivePrefix, entry, depth); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read snapshot source directory %q: %w", archivePrefix, readErr)
		}
		if len(entries) == 0 {
			return fmt.Errorf("read snapshot source directory %q returned no entries without EOF", archivePrefix)
		}
	}
}

func (w *snapshotTarWalker) addEntry(sourceRoot *os.Root, archivePrefix string, entry fs.DirEntry, depth int) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.entryCount++
	if w.entryCount > w.maxEntries {
		return fmt.Errorf("snapshot source contains more than %d entries", w.maxEntries)
	}

	entryName := entry.Name()
	relativePath := entryName
	if archivePrefix != "" {
		relativePath = archivePrefix + "/" + entryName
	}
	lstatInfo, err := sourceRoot.Lstat(entryName)
	if err != nil {
		return fmt.Errorf("inspect snapshot source %q: %w", relativePath, err)
	}
	if lstatInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snapshot source contains unsupported symlink %q", relativePath)
	}
	// Android leaves transient Unix-domain sockets such as ndebugsocket and
	// unsolzygotesocket in /data after the guest stops. They carry no persistent
	// state and cannot be represented safely in the encrypted tar snapshot, so
	// omit them without opening or following them. Continue rejecting every
	// other special file type below.
	if lstatInfo.Mode()&os.ModeSocket != 0 {
		return nil
	}
	if !lstatInfo.IsDir() && !lstatInfo.Mode().IsRegular() {
		return fmt.Errorf("snapshot source contains unsupported special file %q", relativePath)
	}
	if lstatInfo.IsDir() {
		return w.addDirectoryEntry(sourceRoot, relativePath, entryName, lstatInfo, depth)
	}
	return w.addRegularFileEntry(sourceRoot, relativePath, entryName, lstatInfo)
}

func (w *snapshotTarWalker) addDirectoryEntry(sourceRoot *os.Root, relativePath, entryName string, lstatInfo fs.FileInfo, depth int) (resultErr error) {
	childRoot, err := sourceRoot.OpenRoot(entryName)
	if err != nil {
		return fmt.Errorf("open snapshot source directory %q: %w", relativePath, err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, childRoot.Close())
	}()
	info, err := childRoot.Stat(".")
	if err != nil {
		return fmt.Errorf("inspect opened snapshot source directory %q: %w", relativePath, err)
	}
	if !info.IsDir() || !os.SameFile(lstatInfo, info) {
		return fmt.Errorf("snapshot source directory %q changed between inspection and open", relativePath)
	}

	archiveName, err := canonicalArchiveRelativePath(relativePath, true)
	if err != nil {
		return fmt.Errorf("snapshot source path %q cannot be archived safely: %w", relativePath, err)
	}
	header, err := tar.FileInfoHeader(info, "")
	if err == nil {
		header.Name = archiveName + "/"
		err = populateTarOwnership(header, info)
	}
	if err != nil {
		return err
	}
	if err := w.writer.WriteHeader(header); err != nil {
		return err
	}
	return w.walkDirectory(childRoot, relativePath, depth+1)
}

func (w *snapshotTarWalker) addRegularFileEntry(sourceRoot *os.Root, relativePath, entryName string, lstatInfo fs.FileInfo) error {
	file, err := sourceRoot.OpenFile(entryName, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open snapshot source %q without following symlinks: %w", relativePath, err)
	}
	info, statErr := file.Stat()
	if statErr == nil && !os.SameFile(lstatInfo, info) {
		statErr = errors.New("snapshot source changed between inspection and open")
	}
	if statErr == nil && !info.Mode().IsRegular() {
		statErr = errors.New("snapshot source is an unsupported special file")
	}
	if statErr != nil {
		closeErr := file.Close()
		return fmt.Errorf("validate opened snapshot source %q: %w", relativePath, errors.Join(statErr, closeErr))
	}

	archiveName, err := canonicalArchiveRelativePath(relativePath, false)
	if err != nil {
		closeErr := file.Close()
		return fmt.Errorf("snapshot source path %q cannot be archived safely: %w", relativePath, errors.Join(err, closeErr))
	}
	if info.Size() < 0 || info.Size() > maxRestoredSnapshotData-w.sourceBytes {
		closeErr := file.Close()
		return errors.Join(
			fmt.Errorf("snapshot source exceeds maximum uncompressed size %d", maxRestoredSnapshotData),
			closeErr,
		)
	}
	w.sourceBytes += info.Size()

	header, err := tar.FileInfoHeader(info, "")
	if err == nil {
		header.Name = archiveName
		err = populateTarOwnership(header, info)
	}
	if err != nil {
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}
	if err := w.writer.WriteHeader(header); err != nil {
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}
	written, copyErr := io.CopyN(w.writer, file, info.Size())
	if copyErr == nil && written != info.Size() {
		copyErr = io.ErrUnexpectedEOF
	}
	if copyErr == nil {
		afterInfo, afterErr := file.Stat()
		if afterErr != nil {
			copyErr = afterErr
		} else if !os.SameFile(info, afterInfo) || afterInfo.Size() != info.Size() || !afterInfo.ModTime().Equal(info.ModTime()) {
			copyErr = errors.New("snapshot source changed while it was being read")
		}
	}
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("archive snapshot source %q: %w", relativePath, err)
	}
	return nil
}

func writeFileAndSync(path string, payload []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		err = nil
	}
	return errors.Join(err, closeErr)
}

func extractTarToDir(reader *tar.Reader, root string) (resultErr error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, rootHandle.Close())
	}()
	return extractTarToRoot(reader, rootHandle)
}

type extractedDirectoryMetadata struct {
	name    string
	uid     int
	gid     int
	mode    fs.FileMode
	modTime time.Time
}

func extractTarToRoot(reader *tar.Reader, root *os.Root) error {
	if reader == nil || root == nil {
		return errors.New("snapshot extraction requires an archive reader and root")
	}

	var restoredBytes int64
	fileCount := 0
	seenPaths := make(map[string]string)
	directories := make([]extractedDirectoryMetadata, 0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		fileCount++
		if fileCount > maxRestoredFileCount {
			return fmt.Errorf("snapshot contains more than %d archive entries", maxRestoredFileCount)
		}
		isDirectory := header.Typeflag == tar.TypeDir
		entryName, err := canonicalArchiveRelativePath(header.Name, isDirectory)
		if err != nil {
			return fmt.Errorf("snapshot contains invalid archive path %q: %w", header.Name, err)
		}
		collisionKey := entryName
		if previous, exists := seenPaths[collisionKey]; exists {
			return fmt.Errorf("snapshot contains duplicate archive path %q (previously %q)", header.Name, previous)
		}
		seenPaths[collisionKey] = header.Name

		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureRootPathHasNoSymlinks(root, entryName, true); err != nil {
				return err
			}
			if err := root.MkdirAll(path.Dir(entryName), 0o700); err != nil {
				return fmt.Errorf("create parent for archive directory %q: %w", header.Name, err)
			}
			if err := root.Mkdir(entryName, 0o700); err != nil {
				if !errors.Is(err, fs.ErrExist) {
					return fmt.Errorf("create archive directory %q: %w", header.Name, err)
				}
				info, statErr := root.Lstat(entryName)
				if statErr != nil {
					return fmt.Errorf("inspect archive directory %q: %w", header.Name, statErr)
				}
				if !info.IsDir() {
					return fmt.Errorf("archive directory %q collides with a non-directory", header.Name)
				}
			}
			directories = append(directories, extractedDirectoryMetadata{
				name:    entryName,
				uid:     header.Uid,
				gid:     header.Gid,
				mode:    fs.FileMode(header.Mode).Perm(),
				modTime: header.ModTime,
			})
		case tar.TypeSymlink:
			return fmt.Errorf("archive symlink %q is not supported", header.Name)
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxRestoredSnapshotData-restoredBytes {
				return fmt.Errorf("snapshot restored data exceeds maximum %d bytes", maxRestoredSnapshotData)
			}
			if err := ensureRootPathHasNoSymlinks(root, entryName, true); err != nil {
				return err
			}
			if err := root.MkdirAll(path.Dir(entryName), 0o700); err != nil {
				return fmt.Errorf("create parent for archive file %q: %w", header.Name, err)
			}
			file, err := root.OpenFile(entryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return fmt.Errorf("create archive file %q: %w", header.Name, err)
			}
			written, copyErr := io.CopyN(file, reader, header.Size)
			if copyErr == nil && written != header.Size {
				copyErr = io.ErrUnexpectedEOF
			}
			if copyErr == nil {
				copyErr = applyExtractedMetadata(file, header)
			}
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return fmt.Errorf("extract archive file %q: %w", header.Name, err)
			}
			restoredBytes += header.Size
		case tar.TypeLink:
			return fmt.Errorf("archive hardlink %q is not supported", header.Name)
		default:
			return fmt.Errorf("archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
	}

	sort.SliceStable(directories, func(left, right int) bool {
		leftDepth := strings.Count(directories[left].name, "/")
		rightDepth := strings.Count(directories[right].name, "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return directories[left].name < directories[right].name
	})
	for _, metadata := range directories {
		directory, err := root.Open(metadata.name)
		if err != nil {
			return fmt.Errorf("open extracted archive directory %q: %w", metadata.name, err)
		}
		info, statErr := directory.Stat()
		if statErr == nil && !info.IsDir() {
			statErr = fmt.Errorf("extracted archive path is no longer a directory")
		}
		metadataErr := statErr
		if metadataErr == nil {
			metadataErr = applyFileMetadata(directory, metadata.uid, metadata.gid, metadata.mode, metadata.modTime)
		}
		closeErr := directory.Close()
		if err := errors.Join(metadataErr, closeErr); err != nil {
			return fmt.Errorf("apply archive directory metadata %q: %w", metadata.name, err)
		}
	}
	return nil
}

func populateTarOwnership(header *tar.Header, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return nil
	}
	header.Uid = int(stat.Uid)
	header.Gid = int(stat.Gid)
	header.AccessTime = info.ModTime()
	header.ModTime = info.ModTime()
	header.ChangeTime = info.ModTime()
	return nil
}

func applyExtractedMetadata(file *os.File, header *tar.Header) error {
	if header == nil {
		return nil
	}
	return applyFileMetadata(file, header.Uid, header.Gid, fs.FileMode(header.Mode).Perm(), header.ModTime)
}

func applyFileMetadata(file *os.File, uid, gid int, mode fs.FileMode, modTime time.Time) error {
	if file == nil {
		return errors.New("cannot apply metadata to a nil file")
	}
	if err := file.Chown(uid, gid); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if err := file.Chmod(mode.Perm()); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if modTime.IsZero() {
		modTime = time.Now()
	}
	timestamps := []syscall.Timeval{
		syscall.NsecToTimeval(modTime.UnixNano()),
		syscall.NsecToTimeval(modTime.UnixNano()),
	}
	if err := syscall.Futimes(int(file.Fd()), timestamps); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	return nil
}

func canonicalArchiveRelativePath(name string, directory bool) (string, error) {
	if name == "" || len(name) > maxArchivePathBytes || strings.IndexByte(name, 0) >= 0 {
		return "", errors.New("archive path is empty, too long, or contains NUL")
	}
	if strings.Contains(name, `\`) {
		return "", errors.New("archive path contains a backslash")
	}

	normalized := name
	if directory {
		normalized = strings.TrimSuffix(normalized, "/")
	} else if strings.HasSuffix(normalized, "/") {
		return "", errors.New("non-directory archive path has a trailing slash")
	}
	if normalized == "" || path.IsAbs(normalized) || hasWindowsDrivePrefix(normalized) {
		return "", errors.New("archive path must be relative")
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("archive path escapes the extraction root")
	}
	if cleaned != normalized {
		return "", errors.New("archive path is not canonical")
	}
	return cleaned, nil
}

func hasWindowsDrivePrefix(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	first := name[0]
	return first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z'
}

func ensureRootPathHasNoSymlinks(root *os.Root, name string, includeLeaf bool) error {
	parts := strings.Split(name, "/")
	if !includeLeaf && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	current := ""
	for index, part := range parts {
		if current == "" {
			current = part
		} else {
			current = path.Join(current, part)
		}
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect archive path %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive path %q crosses symlink %q", name, current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("archive path %q crosses non-directory %q", name, current)
		}
	}
	return nil
}

func secureJoin(root, relativePath string) (string, error) {
	if filepath.IsAbs(filepath.FromSlash(relativePath)) {
		return "", fmt.Errorf("invalid absolute archive path %q", relativePath)
	}
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(filepath.Join(cleanRoot, relativePath))
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid archive path %q", relativePath)
	}
	return cleanPath, nil
}

func directoryHasEntries(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

func repairLegacyAndroidSystemOwnership(dataDir string) error {
	for _, relative := range []string{"system", "system_ce", "system_de"} {
		root := filepath.Join(dataDir, relative)
		if !fileExists(root) {
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := os.Lchown(path, 1000, 1000); err != nil && !errors.Is(err, fs.ErrPermission) {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
