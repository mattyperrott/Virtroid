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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var errSnapshotMissing = errors.New("snapshot missing")

const (
	snapshotMagic    = "VRTBLOB1"
	snapshotTagSize  = sha256.Size
	blobStoreLocal   = "local-disk"
	blobStoreRenterd = "sia-renterd"
	blobChunkSize    = 4 << 20
)

type blobStore interface {
	kind() string
	persistFromDir(ctx context.Context, runtimeID, dataDir string, masterKey []byte) (*blobManifest, error)
	restoreToDir(ctx context.Context, runtimeID string, manifest *blobManifest, dataDir string, masterKey []byte) error
	clearRuntime(ctx context.Context, runtimeID string) error
	pruneRuntime(ctx context.Context, runtimeID, keepSnapshotID string) error
}

type blobManifest struct {
	Version     int         `json:"version"`
	Store       string      `json:"store"`
	Bucket      string      `json:"bucket,omitempty"`
	ObjectType  string      `json:"object_type"`
	SnapshotID  string      `json:"snapshot_id"`
	CreatedAt   time.Time   `json:"created_at"`
	ChunkSize   int64       `json:"chunk_size"`
	TotalBytes  int64       `json:"total_bytes"`
	Compression string      `json:"compression"`
	Encryption  string      `json:"encryption"`
	Chunks      []blobChunk `json:"chunks"`
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

type blobPreflightReport struct {
	Store  string               `json:"store"`
	OK     bool                 `json:"ok"`
	Checks []blobPreflightCheck `json:"checks"`
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
	workerURL   string
	password    string
	bucket      string
	minShards   int
	totalShards int
	contractSet string
	chunkSize   int64
	httpClient  *http.Client
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
			workerURL:   strings.TrimRight(strings.TrimSpace(n.cfg.RenterdWorkerURL), "/"),
			password:    n.cfg.RenterdPassword,
			bucket:      defaultBlobBucket(n.cfg.RenterdBucket),
			minShards:   n.cfg.RenterdMinShards,
			totalShards: n.cfg.RenterdTotalShards,
			contractSet: strings.TrimSpace(n.cfg.RenterdContractSet),
			chunkSize:   blobChunkSize,
			httpClient:  &http.Client{Timeout: 10 * time.Minute},
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
	report := blobPreflightReport{
		Store: strings.TrimSpace(n.cfg.BlobStoreKind),
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

func (n *nodeAgent) prepareSessionData(runtime runtimeAssignment) (bool, error) {
	runtimeRoot := filepath.Join(n.cfg.RuntimeRoot, runtime.ID)
	dataDir := filepath.Join(runtimeRoot, "data")
	manifest, err := parseBlobManifest(runtime.BlobManifestJSON)
	if err != nil {
		return false, err
	}
	masterKey, err := n.runtimeBlobKey(runtime)
	if err != nil {
		return false, err
	}

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
		if err := store.restoreToDir(context.Background(), runtime.ID, manifest, dataDir, masterKey); err != nil {
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
		filepath.Join(dataDir, "misc", "keystore", "persistent.sqlite"),
		filepath.Join(dataDir, "misc", "keystore", "persistent.sqlite-shm"),
		filepath.Join(dataDir, "misc", "keystore", "persistent.sqlite-wal"),
		filepath.Join(dataDir, "misc", "keystore", "vpnprofilestore.sqlite"),
		filepath.Join(dataDir, "misc", "keystore", "vpnprofilestore.sqlite-shm"),
		filepath.Join(dataDir, "misc", "keystore", "vpnprofilestore.sqlite-wal"),
	}

	for _, target := range paths {
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (n *nodeAgent) persistSessionData(runtime runtimeAssignment) (*persistedBlob, error) {
	dataDir := filepath.Join(n.cfg.RuntimeRoot, runtime.ID, "data")
	if !directoryHasEntries(dataDir) {
		if err := os.RemoveAll(dataDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return &persistedBlob{ClearExisting: true}, nil
	}

	masterKey, err := n.runtimeBlobKey(runtime)
	if err != nil {
		return nil, err
	}

	if !runtime.BlobAutoSnapshot {
		if err := os.RemoveAll(dataDir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return &persistedBlob{ClearExisting: true}, nil
	}

	store, err := n.blobStore(n.cfg.BlobStoreKind)
	if err != nil {
		return nil, err
	}
	manifest, err := store.persistFromDir(context.Background(), runtime.ID, dataDir, masterKey)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &persistedBlob{
		Manifest:   manifest,
		SnapshotAt: &now,
	}, nil
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
	for kind, store := range stores {
		if kind == retainedStore.kind() {
			if err := store.pruneRuntime(context.Background(), runtime.ID, retained.SnapshotID); err != nil {
				return err
			}
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
	renterdStore, ok := store.(*renterdBlobStore)
	if !ok {
		return nil
	}
	return renterdStore.deleteManifest(context.Background(), manifest)
}

func (n *nodeAgent) runtimeBlobKey(runtime runtimeAssignment) ([]byte, error) {
	envelope, expiresAt, err := n.fetchActiveBlobKey(context.Background(), runtime.ID)
	if err != nil {
		return nil, err
	}
	return n.decryptBlobKeyEnvelope(envelope, expiresAt)
}

func (n *nodeAgent) fetchActiveBlobKey(ctx context.Context, runtimeID string) (blobKeyEnvelopePayload, time.Time, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		n.cfg.ControlPlaneURL+"/api/v1/internal/runtimes/"+url.PathEscape(runtimeID)+"/blob-key",
		nil,
	)
	if err != nil {
		return blobKeyEnvelopePayload{}, time.Time{}, err
	}
	if err := n.signControlPlaneRequest(req, nil, false); err != nil {
		return blobKeyEnvelopePayload{}, time.Time{}, err
	}

	resp, err := n.controlPlane.Do(req)
	if err != nil {
		return blobKeyEnvelopePayload{}, time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return blobKeyEnvelopePayload{}, time.Time{}, fmt.Errorf("fetch active blob key envelope: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	var payload struct {
		BlobKeyEnvelope blobKeyEnvelopePayload `json:"blob_key_envelope"`
		BlobKeyExpires  time.Time              `json:"blob_key_expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return blobKeyEnvelopePayload{}, time.Time{}, err
	}
	if strings.TrimSpace(payload.BlobKeyEnvelope.Ciphertext) == "" || payload.BlobKeyExpires.IsZero() {
		return blobKeyEnvelopePayload{}, time.Time{}, errors.New("blob key envelope handoff response is incomplete")
	}
	return payload.BlobKeyEnvelope, payload.BlobKeyExpires, nil
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

func (s *localBlobStore) persistFromDir(ctx context.Context, runtimeID, dataDir string, masterKey []byte) (*blobManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("virtroid-%s-%d.enc", runtimeID, time.Now().UnixNano()))
	defer os.Remove(tempPath)

	totalBytes, err := writeSnapshot(tempPath, dataDir, masterKey)
	if err != nil {
		return nil, err
	}

	snapshotID, err := newSnapshotID()
	if err != nil {
		return nil, err
	}
	runtimeDir := filepath.Join(s.root, runtimeID, snapshotID)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, err
	}

	file, err := os.Open(tempPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	manifest := &blobManifest{
		Version:     1,
		Store:       s.kind(),
		ObjectType:  "runtime-userdata",
		SnapshotID:  snapshotID,
		CreatedAt:   time.Now().UTC(),
		ChunkSize:   s.chunkSize,
		TotalBytes:  totalBytes,
		Compression: "gzip",
		Encryption:  "aes-ctr+hmac-sha256",
	}

	buffer := make([]byte, s.chunkSize)
	for index := 0; ; index++ {
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
		chunkPath := filepath.Join(runtimeDir, chunkName)
		if err := os.WriteFile(chunkPath, chunkPayload, 0o600); err != nil {
			return nil, err
		}
		manifest.Chunks = append(manifest.Chunks, blobChunk{
			Index:  index,
			Key:    filepath.ToSlash(filepath.Join(runtimeID, snapshotID, chunkName)),
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
		payload, err := os.ReadFile(chunkPath)
		if err != nil {
			tempFile.Close()
			return err
		}
		sum := sha256.Sum256(payload)
		if hex.EncodeToString(sum[:]) != chunk.SHA256 {
			tempFile.Close()
			return fmt.Errorf("blob chunk %d integrity mismatch", chunk.Index)
		}
		if _, err := tempFile.Write(payload); err != nil {
			tempFile.Close()
			return err
		}
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return restoreSnapshot(tempPath, dataDir, masterKey)
}

func validateBlobManifest(manifest *blobManifest) error {
	return validateBlobManifestForRuntime(manifest, "")
}

func validateBlobManifestForRuntime(manifest *blobManifest, runtimeID string) error {
	if manifest == nil {
		return errSnapshotMissing
	}
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported blob manifest version %d", manifest.Version)
	}
	if manifest.ObjectType != "runtime-userdata" {
		return fmt.Errorf("unsupported blob object type %q", manifest.ObjectType)
	}
	if manifest.Encryption != "aes-ctr+hmac-sha256" {
		return fmt.Errorf("unsupported blob encryption %q", manifest.Encryption)
	}
	if manifest.Compression != "gzip" {
		return fmt.Errorf("unsupported blob compression %q", manifest.Compression)
	}
	if len(manifest.Chunks) == 0 {
		return errors.New("blob manifest has no chunks")
	}
	if err := validateSnapshotID(manifest.SnapshotID); err != nil {
		return err
	}
	var totalBytes int64
	for index, chunk := range manifest.Chunks {
		if chunk.Index != index {
			return fmt.Errorf("blob manifest chunk index mismatch at %d", index)
		}
		if err := validateBlobChunkKey(runtimeID, manifest.SnapshotID, index, chunk.Key); err != nil {
			return err
		}
		if chunk.Size <= 0 {
			return fmt.Errorf("blob manifest chunk %d has invalid size", index)
		}
		if len(chunk.SHA256) != sha256.Size*2 {
			return fmt.Errorf("blob manifest chunk %d has invalid hash", index)
		}
		totalBytes += chunk.Size
	}
	if manifest.TotalBytes != totalBytes {
		return fmt.Errorf("blob manifest byte total mismatch")
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

func validateBlobChunkKey(runtimeID, snapshotID string, index int, key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("blob manifest chunk %d has empty key", index)
	}
	if strings.TrimSpace(key) != key || filepath.IsAbs(filepath.FromSlash(key)) {
		return fmt.Errorf("blob manifest chunk %d has invalid key %q", index, key)
	}
	if runtimeID != "" {
		expected := filepath.ToSlash(filepath.Join(runtimeID, snapshotID, fmt.Sprintf("chunk-%05d.bin", index)))
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
	if strings.TrimSpace(s.workerURL) == "" {
		report.addCheck("worker_url", "fail", "NODE_SIA_RENTERD_WORKER_URL is required")
		return
	}
	report.addCheck("worker_url", "pass", s.workerURL)
	if strings.TrimSpace(s.password) == "" {
		report.addCheck("api_password", "fail", "NODE_SIA_RENTERD_PASSWORD is required")
		return
	}
	report.addCheck("api_password", "pass", "configured")

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

	var wallet map[string]any
	if err := s.getRenterdJSON(ctx, "/api/bus/wallet", &wallet); err != nil {
		report.addCheck("wallet", "fail", err.Error())
	} else if nonZeroCurrencyValue(wallet, "siacoins", "balance", "spendable") {
		report.addCheck("wallet", "pass", "funded")
	} else {
		report.addCheck("wallet", "warn", "wallet endpoint reachable; non-zero balance was not detected")
	}

	var contracts []json.RawMessage
	if err := s.getRenterdJSON(ctx, "/api/bus/contracts/active", &contracts); err != nil {
		report.addCheck("active_contracts", "fail", err.Error())
	} else if len(contracts) == 0 {
		report.addCheck("active_contracts", "fail", "no active renterd contracts")
	} else {
		report.addCheck("active_contracts", "pass", fmt.Sprintf("%d active contracts", len(contracts)))
	}
}

func (s *renterdBlobStore) persistFromDir(ctx context.Context, runtimeID, dataDir string, masterKey []byte) (*blobManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.workerURL) == "" {
		return nil, errors.New("renterd worker url is required")
	}

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("virtroid-%s-%d.enc", runtimeID, time.Now().UnixNano()))
	defer os.Remove(tempPath)

	totalBytes, err := writeSnapshot(tempPath, dataDir, masterKey)
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

	manifest := &blobManifest{
		Version:     1,
		Store:       s.kind(),
		Bucket:      s.bucketName(),
		ObjectType:  "runtime-userdata",
		SnapshotID:  snapshotID,
		CreatedAt:   time.Now().UTC(),
		ChunkSize:   s.chunkSize,
		TotalBytes:  totalBytes,
		Compression: "gzip",
		Encryption:  "aes-ctr+hmac-sha256",
	}

	buffer := make([]byte, s.chunkSize)
	for index := 0; ; index++ {
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
		chunkKey := filepath.ToSlash(filepath.Join(runtimeID, snapshotID, chunkName))
		if err := s.putObject(ctx, chunkKey, chunkPayload); err != nil {
			return nil, err
		}
		manifest.Chunks = append(manifest.Chunks, blobChunk{
			Index:  index,
			Key:    chunkKey,
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

	tempPath := filepath.Join(os.TempDir(), fmt.Sprintf("virtroid-restore-%s-%d.enc", runtimeID, time.Now().UnixNano()))
	defer os.Remove(tempPath)

	tempFile, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	for _, chunk := range manifest.Chunks {
		payload, err := s.getObject(ctx, manifestBucket(manifest, s.bucketName()), chunk.Key)
		if err != nil {
			tempFile.Close()
			return err
		}
		sum := sha256.Sum256(payload)
		if hex.EncodeToString(sum[:]) != chunk.SHA256 {
			tempFile.Close()
			return fmt.Errorf("blob chunk %d integrity mismatch", chunk.Index)
		}
		if _, err := tempFile.Write(payload); err != nil {
			tempFile.Close()
			return err
		}
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return restoreSnapshot(tempPath, dataDir, masterKey)
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

func (s *renterdBlobStore) pruneRuntime(ctx context.Context, runtimeID, keepSnapshotID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtimeID == "" || keepSnapshotID == "" {
		return nil
	}
	// renterd's worker object API has no cheap prefix listing guarantee across
	// versions. We keep old Sia snapshots until manifest-tracked remote GC is
	// added, while local-disk remains aggressively pruned.
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

func (s *renterdBlobStore) getObject(ctx context.Context, bucket, key string) ([]byte, error) {
	requestURL, err := s.objectURL(bucket, key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	s.authorize(req)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("renterd download object %q failed: status=%d body=%s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

func (s *renterdBlobStore) deletePrefix(ctx context.Context, prefix string) error {
	// Without relying on a specific renterd object-listing response shape, the
	// provider can only delete manifest-known chunks. Full prefix GC is left for
	// a later provider version that uses renterd's bus object listing API.
	return nil
}

func (s *renterdBlobStore) deleteManifest(ctx context.Context, manifest *blobManifest) error {
	if manifest == nil {
		return nil
	}
	bucket := manifestBucket(manifest, s.bucketName())
	for _, chunk := range manifest.Chunks {
		if err := s.deleteObject(ctx, bucket, chunk.Key); err != nil {
			return err
		}
	}
	return nil
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

func (s *localBlobStore) pruneRuntime(ctx context.Context, runtimeID, keepSnapshotID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runtimeDir := filepath.Join(s.root, runtimeID)
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
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		return 0, err
	}

	tempPath := snapshotPath + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		return 0, err
	}

	closeWithError := func(sourceErr error) (int64, error) {
		_ = file.Close()
		_ = os.Remove(tempPath)
		return 0, sourceErr
	}

	header, writer, err := newEncryptedWriter(file, masterKey)
	if err != nil {
		return closeWithError(err)
	}
	if _, err := file.Write(header); err != nil {
		return closeWithError(err)
	}

	gzipWriter := gzip.NewWriter(writer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := addDirectoryToTar(tarWriter, dataDir); err != nil {
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
	if err := file.Close(); err != nil {
		return closeWithError(err)
	}
	if err := os.Rename(tempPath, snapshotPath); err != nil {
		return 0, err
	}

	info, err := os.Stat(snapshotPath)
	if err != nil {
		return 0, err
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
	file   *os.File
}

type hashState interface {
	Write(p []byte) (int, error)
	Sum(b []byte) []byte
}

func newEncryptedWriter(file *os.File, masterKey []byte) ([]byte, io.WriteCloser, error) {
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

func addDirectoryToTar(writer *tar.Writer, root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(relativePath)
		if relativePath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relativePath
		if err := populateTarOwnership(header, info); err != nil {
			return err
		}

		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			header.Linkname = target
		}

		if info.IsDir() {
			header.Name += "/"
			return writer.WriteHeader(header)
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err
	})
}

func extractTarToDir(reader *tar.Reader, root string) error {
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		targetPath, err := secureJoin(root, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureNoSymlinkInPath(root, targetPath); err != nil {
				return err
			}
			if err := os.MkdirAll(targetPath, fs.FileMode(header.Mode)); err != nil {
				return err
			}
			if err := applyExtractedMetadata(targetPath, header, false); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := validateSymlinkTarget(root, targetPath, header.Linkname); err != nil {
				return err
			}
			if err := ensureNoSymlinkInPath(root, filepath.Dir(targetPath)); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			_ = os.Remove(targetPath)
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return err
			}
			if err := applyExtractedMetadata(targetPath, header, true); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := ensureNoSymlinkInPath(root, targetPath); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, fs.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, reader); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			if err := applyExtractedMetadata(targetPath, header, false); err != nil {
				return err
			}
		}
	}
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

func applyExtractedMetadata(targetPath string, header *tar.Header, isSymlink bool) error {
	if header == nil {
		return nil
	}
	if isSymlink {
		if err := os.Lchown(targetPath, header.Uid, header.Gid); err != nil && !errors.Is(err, fs.ErrPermission) {
			return err
		}
		return nil
	}
	if err := os.Chown(targetPath, header.Uid, header.Gid); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if err := os.Chmod(targetPath, fs.FileMode(header.Mode)); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
	}
	modTime := header.ModTime
	if modTime.IsZero() {
		modTime = time.Now()
	}
	if err := os.Chtimes(targetPath, modTime, modTime); err != nil && !errors.Is(err, fs.ErrPermission) {
		return err
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

func validateSymlinkTarget(root, targetPath, linkName string) error {
	if strings.TrimSpace(linkName) == "" {
		return errors.New("archive symlink has empty target")
	}
	if filepath.IsAbs(filepath.FromSlash(linkName)) {
		return fmt.Errorf("archive symlink %q has absolute target %q", targetPath, linkName)
	}
	cleanRoot := filepath.Clean(root)
	resolved := filepath.Clean(filepath.Join(filepath.Dir(targetPath), filepath.FromSlash(linkName)))
	if resolved != cleanRoot && !strings.HasPrefix(resolved, cleanRoot+string(os.PathSeparator)) {
		return fmt.Errorf("archive symlink %q escapes restore root", targetPath)
	}
	return nil
}

func ensureNoSymlinkInPath(root, targetPath string) error {
	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(targetPath)
	if cleanTarget == cleanRoot {
		return nil
	}
	if !strings.HasPrefix(cleanTarget, cleanRoot+string(os.PathSeparator)) {
		return fmt.Errorf("archive path %q escapes restore root", targetPath)
	}
	relativePath, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return err
	}
	current := cleanRoot
	for _, part := range strings.Split(relativePath, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive path %q crosses symlink %q", targetPath, current)
		}
	}
	return nil
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
