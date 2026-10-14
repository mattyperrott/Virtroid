package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/indexd/api/app"
	"go.sia.tech/indexd/slabs"
	"go.sia.tech/siastorage"
	"go.uber.org/zap"
)

// The sia-indexd store keeps each runtime snapshot as one Sia object. The node
// encrypts the snapshot as for every other store, then uploads it straight to
// Sia hosts with the account's indexd app key; indexd only keeps the sealed
// object metadata. The manifest still lists the snapshot's 4 MiB chunks with
// their hashes, so a restore verifies every chunk against the authenticated
// manifest whatever Sia returns. The manifest names the object in SiaObject.

const (
	blobStoreSia          = "sia-indexd"
	siaObjectKind         = "virtroid-runtime-userdata"
	siaListPageSize       = 100
	siaCredentialsTTL     = 10 * time.Minute
	maxSiaCredentialsBody = 16 << 10
	// siaIndexerHTTPTimeout bounds the SDK's metadata calls to the indexer,
	// which go through http.DefaultClient. Data moves between the node and the
	// hosts on separate connections.
	siaIndexerHTTPTimeout = 2 * time.Minute
)

// errSiaNotConfigured means the control plane has no Sia indexer, so none of
// the account's data can be on Sia.
var errSiaNotConfigured = errors.New("sia storage is not configured")

// siaObjectMeta is sealed into each object with the account's app key, so the
// indexer cannot read it.
type siaObjectMeta struct {
	Kind       string `json:"kind"`
	RuntimeID  string `json:"runtime_id"`
	Namespace  string `json:"namespace"`
	SnapshotID string `json:"snapshot_id"`
}

type siaObjectInfo struct {
	ID   string
	Meta siaObjectMeta
}

// siaObjects is what the store needs from one account's Sia client.
type siaObjects interface {
	// remaining is how many more bytes the account's quota lets it store.
	remaining(ctx context.Context) (int64, error)
	put(ctx context.Context, meta siaObjectMeta, r io.Reader) (string, error)
	open(ctx context.Context, objectID string) (io.ReadCloser, int64, error)
	remove(ctx context.Context, objectID string) error
	list(ctx context.Context) ([]siaObjectInfo, error)
	prune(ctx context.Context) error
	close() error
}

// siaCredentials let a node act for one account on the indexer. The control
// plane releases them only to the node that holds the runtime.
type siaCredentials struct {
	IndexerURL  string
	AppKeySeed  []byte
	DataShards  int
	TotalShards int
}

func (c siaCredentials) validate() error {
	parsed, err := url.Parse(c.IndexerURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return errors.New("sia indexer url is invalid")
	}
	if len(c.AppKeySeed) != 32 {
		return errors.New("sia app key seed must be 32 bytes")
	}
	if c.DataShards < 1 || c.TotalShards <= c.DataShards || c.TotalShards > math.MaxUint8 {
		return fmt.Errorf("sia redundancy %d-of-%d is invalid", c.DataShards, c.TotalShards)
	}
	return nil
}

// account identifies the indexer account the credentials act for.
func (c siaCredentials) account() string {
	key := types.NewPrivateKeyFromSeed(c.AppKeySeed)
	return c.IndexerURL + "|" + key.PublicKey().String()
}

// siaAccounts caches credentials and clients across the stores blobStores
// builds, and keeps pruning away from the same account's uploads.
type siaAccounts struct {
	mu          sync.Mutex
	credentials map[string]cachedSiaCredentials
	clients     map[string]siaObjects
	locks       map[string]*sync.RWMutex
}

type cachedSiaCredentials struct {
	credentials siaCredentials
	expiresAt   time.Time
}

type siaBlobStore struct {
	tempDir     string
	chunkSize   int64
	accounts    *siaAccounts
	credentials func(ctx context.Context, runtimeID string) (siaCredentials, error)
	connect     func(ctx context.Context, credentials siaCredentials) (siaObjects, error)
}

func (n *nodeAgent) siaStore() *siaBlobStore {
	return &siaBlobStore{
		tempDir:     filepath.Join(n.cfg.RuntimeRoot, "_blobstore", "sia-tmp"),
		chunkSize:   blobChunkSize,
		accounts:    &n.siaAccounts,
		credentials: n.fetchSiaCredentials,
		connect:     connectSiaIndexer,
	}
}

func (s *siaBlobStore) kind() string {
	return blobStoreSia
}

func (s *siaBlobStore) preflight(ctx context.Context, report *blobPreflightReport) {
	if err := ctx.Err(); err != nil {
		report.addCheck("context", "fail", err.Error())
		return
	}
	if err := os.MkdirAll(s.tempDir, 0o700); err != nil {
		report.addCheck("temp_dir", "fail", err.Error())
		return
	}
	report.addCheck("temp_dir", "ok", s.tempDir)
	report.addCheck("indexer", "ok", "credentials are issued per account by the control plane")
}

// account returns the client for the runtime's account and the lock that
// keeps pruning out of that account's uploads.
func (s *siaBlobStore) account(ctx context.Context, runtimeID string) (siaObjects, *sync.RWMutex, error) {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return nil, nil, errors.New("runtime id is required")
	}
	a := s.accounts
	now := time.Now()
	a.mu.Lock()
	cached, ok := a.credentials[runtimeID]
	a.mu.Unlock()
	credentials := cached.credentials
	if !ok || !now.Before(cached.expiresAt) {
		fetched, err := s.credentials(ctx, runtimeID)
		if err != nil {
			return nil, nil, err
		}
		if err := fetched.validate(); err != nil {
			return nil, nil, err
		}
		credentials = fetched
		a.mu.Lock()
		if a.credentials == nil {
			a.credentials = make(map[string]cachedSiaCredentials)
		}
		a.credentials[runtimeID] = cachedSiaCredentials{credentials: fetched, expiresAt: now.Add(siaCredentialsTTL)}
		a.mu.Unlock()
	}

	account := credentials.account()
	a.mu.Lock()
	if a.locks == nil {
		a.locks = make(map[string]*sync.RWMutex)
		a.clients = make(map[string]siaObjects)
	}
	lock := a.locks[account]
	if lock == nil {
		lock = &sync.RWMutex{}
		a.locks[account] = lock
	}
	client := a.clients[account]
	a.mu.Unlock()
	if client != nil {
		return client, lock, nil
	}

	// Connecting takes a few round trips, so it happens outside the lock.
	client, err := s.connect(ctx, credentials)
	if err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	if existing := a.clients[account]; existing != nil {
		a.mu.Unlock()
		_ = client.close()
		return existing, lock, nil
	}
	a.clients[account] = client
	a.mu.Unlock()
	return client, lock, nil
}

func (s *siaBlobStore) operationDir(prefix string) (string, error) {
	if err := os.MkdirAll(s.tempDir, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(s.tempDir, prefix+"-*")
}

func (s *siaBlobStore) persistFromDir(ctx context.Context, runtimeID, dataDir string, masterKey []byte) (manifest *blobManifest, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.chunkSize <= 0 || s.chunkSize > maxBlobChunkSize {
		return nil, fmt.Errorf("invalid sia blob chunk size %d", s.chunkSize)
	}
	objects, lock, err := s.account(ctx, runtimeID)
	if err != nil {
		return nil, err
	}

	workDir, err := s.operationDir("persist")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(workDir)
	snapshotPath := filepath.Join(workDir, "snapshot.enc")
	snapshotKey := deriveRuntimeSnapshotKey(masterKey, runtimeID)
	defer clearBytes(snapshotKey)
	totalBytes, err := writeSnapshotWithContext(ctx, snapshotPath, dataDir, snapshotKey)
	if err != nil {
		return nil, err
	}
	// The previous snapshot stays until this one is committed, so both must fit.
	remaining, err := objects.remaining(ctx)
	if err != nil {
		return nil, err
	}
	if remaining < totalBytes {
		return nil, fmt.Errorf("%w: the snapshot needs %d bytes, the account's sia quota has %d left", errRuntimeStorageQuotaExceeded, totalBytes, remaining)
	}
	snapshotID, err := newSnapshotID()
	if err != nil {
		return nil, err
	}
	namespace := deriveRuntimeBlobNamespace(masterKey, runtimeID)
	file, err := os.Open(snapshotPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	hasher := &chunkHasher{
		reader:    file,
		chunkSize: s.chunkSize,
		keyPrefix: path.Join(namespace, snapshotID),
	}
	lock.RLock()
	objectID, err := objects.put(ctx, siaObjectMeta{
		Kind:       siaObjectKind,
		RuntimeID:  runtimeID,
		Namespace:  namespace,
		SnapshotID: snapshotID,
	}, hasher)
	lock.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("upload snapshot to sia: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if removeErr := objects.remove(context.WithoutCancel(ctx), objectID); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove incomplete sia snapshot: %w", removeErr))
			}
		}
	}()
	chunks := hasher.finish()
	if hasher.total != totalBytes {
		return nil, fmt.Errorf("sia upload read %d of %d snapshot bytes", hasher.total, totalBytes)
	}

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
		Chunks:      chunks,
		SiaObject:   objectID,
	}
	if err := authenticateBlobManifest(manifest, masterKey, runtimeID); err != nil {
		return nil, fmt.Errorf("authenticate sia blob manifest: %w", err)
	}
	if err := validateBlobManifestForRuntime(manifest, runtimeID); err != nil {
		return nil, fmt.Errorf("validate completed sia blob manifest: %w", err)
	}
	committed = true
	return manifest, nil
}

func (s *siaBlobStore) restoreToDir(ctx context.Context, runtimeID string, manifest *blobManifest, dataDir string, masterKey []byte) error {
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
	objects, _, err := s.account(ctx, runtimeID)
	if err != nil {
		return err
	}
	reader, size, err := objects.open(ctx, manifest.SiaObject)
	if err != nil {
		return fmt.Errorf("open sia snapshot: %w", err)
	}
	defer reader.Close()
	if size != manifest.TotalBytes {
		return fmt.Errorf("sia snapshot is %d bytes, manifest says %d", size, manifest.TotalBytes)
	}

	workDir, err := s.operationDir("restore")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	snapshotPath := filepath.Join(workDir, "snapshot.enc")
	file, err := os.OpenFile(snapshotPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, chunk := range manifest.Chunks {
		if err := copyStreamedBlobChunk(file, reader, chunk); err != nil {
			_ = file.Close()
			return err
		}
	}
	if extra, _ := reader.Read(make([]byte, 1)); extra > 0 {
		_ = file.Close()
		return errors.New("sia snapshot is longer than its manifest")
	}
	if err := file.Close(); err != nil {
		return err
	}
	snapshotKey := snapshotKeyForManifest(masterKey, runtimeID, manifest)
	defer clearBytes(snapshotKey)
	return restoreSnapshot(snapshotPath, dataDir, snapshotKey)
}

// copyStreamedBlobChunk copies exactly one chunk from a stream that holds the
// whole snapshot and checks it against the manifest.
func copyStreamedBlobChunk(dst io.Writer, src io.Reader, chunk blobChunk) error {
	if chunk.Size <= 0 || chunk.Size > maxBlobChunkSize {
		return fmt.Errorf("blob chunk %d has invalid expected size %d", chunk.Index, chunk.Size)
	}
	expectedHash, err := hex.DecodeString(chunk.SHA256)
	if err != nil || len(expectedHash) != sha256.Size {
		return fmt.Errorf("blob chunk %d has invalid expected hash", chunk.Index)
	}
	hasher := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(dst, hasher), src, chunk.Size); err != nil {
		return fmt.Errorf("read blob chunk %d: %w", chunk.Index, err)
	}
	if !hmac.Equal(hasher.Sum(nil), expectedHash) {
		return fmt.Errorf("blob chunk %d integrity mismatch", chunk.Index)
	}
	return nil
}

func (s *siaBlobStore) deleteManifest(ctx context.Context, manifest *blobManifest) error {
	if manifest == nil {
		return nil
	}
	if manifest.Store != s.kind() {
		return fmt.Errorf("cannot delete %q manifest from sia", manifest.Store)
	}
	if err := validateBlobManifestForRuntime(manifest, manifest.RuntimeID); err != nil {
		return err
	}
	objects, lock, err := s.account(ctx, manifest.RuntimeID)
	if err != nil {
		return err
	}
	if err := objects.remove(ctx, manifest.SiaObject); err != nil {
		return fmt.Errorf("delete sia snapshot: %w", err)
	}
	return pruneSiaAccount(ctx, objects, lock)
}

func (s *siaBlobStore) clearRuntime(ctx context.Context, runtimeID string) error {
	return s.removeRuntimeObjects(ctx, runtimeID, func(siaObjectMeta) bool { return true })
}

func (s *siaBlobStore) pruneRuntime(ctx context.Context, runtimeID, namespace, keepSnapshotID string) error {
	if strings.TrimSpace(keepSnapshotID) == "" {
		return nil
	}
	return s.removeRuntimeObjects(ctx, runtimeID, func(meta siaObjectMeta) bool {
		return meta.SnapshotID != keepSnapshotID || meta.Namespace != namespace
	})
}

// removeRuntimeObjects deletes the runtime's objects that match, which also
// retries any earlier delete that failed.
func (s *siaBlobStore) removeRuntimeObjects(ctx context.Context, runtimeID string, match func(siaObjectMeta) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(runtimeID) == "" {
		return nil
	}
	objects, lock, err := s.account(ctx, runtimeID)
	if errors.Is(err, errSiaNotConfigured) {
		return nil
	}
	if err != nil {
		return err
	}
	listed, err := objects.list(ctx)
	if err != nil {
		return fmt.Errorf("list sia snapshots: %w", err)
	}
	removed := 0
	var removeErrors []error
	for _, object := range listed {
		if object.Meta.RuntimeID != runtimeID || !match(object.Meta) {
			continue
		}
		if err := objects.remove(ctx, object.ID); err != nil {
			removeErrors = append(removeErrors, err)
			continue
		}
		removed++
	}
	if removed > 0 {
		removeErrors = append(removeErrors, pruneSiaAccount(ctx, objects, lock))
	}
	return errors.Join(removeErrors...)
}

// pruneSiaAccount releases the slabs of deleted objects, which count against
// the account until then. Slabs of an upload in flight are not referenced by
// an object yet, so pruning waits for a moment with no uploads; a skipped
// prune happens with the next delete.
func pruneSiaAccount(ctx context.Context, objects siaObjects, lock *sync.RWMutex) error {
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	if err := objects.prune(ctx); err != nil {
		return fmt.Errorf("prune sia slabs: %w", err)
	}
	return nil
}

// chunkHasher hashes the bytes read through it in manifest-sized chunks.
type chunkHasher struct {
	reader    io.Reader
	chunkSize int64
	keyPrefix string
	chunks    []blobChunk
	current   hash.Hash
	inChunk   int64
	total     int64
}

func (c *chunkHasher) Read(payload []byte) (int, error) {
	read, err := c.reader.Read(payload)
	remaining := payload[:read]
	for len(remaining) > 0 {
		if c.current == nil {
			c.current = sha256.New()
		}
		take := min(int64(len(remaining)), c.chunkSize-c.inChunk)
		_, _ = c.current.Write(remaining[:take])
		c.inChunk += take
		c.total += take
		remaining = remaining[take:]
		if c.inChunk == c.chunkSize {
			c.closeChunk()
		}
	}
	return read, err
}

func (c *chunkHasher) closeChunk() {
	index := len(c.chunks)
	c.chunks = append(c.chunks, blobChunk{
		Index:  index,
		Key:    path.Join(c.keyPrefix, fmt.Sprintf("chunk-%05d.bin", index)),
		Size:   c.inChunk,
		SHA256: hex.EncodeToString(c.current.Sum(nil)),
	})
	c.current = nil
	c.inChunk = 0
}

func (c *chunkHasher) finish() []blobChunk {
	if c.inChunk > 0 {
		c.closeChunk()
	}
	return c.chunks
}

// fetchSiaCredentials asks the control plane for the indexer credentials of
// the runtime's account.
func (n *nodeAgent) fetchSiaCredentials(ctx context.Context, runtimeID string) (siaCredentials, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		n.cfg.ControlPlaneURL+"/api/v1/internal/runtimes/"+url.PathEscape(runtimeID)+"/sia-credentials",
		nil,
	)
	if err != nil {
		return siaCredentials{}, err
	}
	if err := n.signControlPlaneRequest(req, nil, false); err != nil {
		return siaCredentials{}, err
	}
	resp, err := n.controlPlane.Do(req)
	if err != nil {
		return siaCredentials{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSiaCredentialsBody))
	if err != nil {
		return siaCredentials{}, err
	}
	if resp.StatusCode == http.StatusNotFound && strings.Contains(string(body), "sia_not_configured") {
		return siaCredentials{}, errSiaNotConfigured
	}
	if resp.StatusCode >= 300 {
		return siaCredentials{}, fmt.Errorf("fetch sia credentials: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		IndexerURL  string `json:"indexer_url"`
		AppKeySeed  string `json:"app_key_seed"`
		DataShards  int    `json:"data_shards"`
		TotalShards int    `json:"total_shards"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return siaCredentials{}, fmt.Errorf("decode sia credentials: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(payload.AppKeySeed)
	if err != nil {
		return siaCredentials{}, errors.New("sia app key seed is not base64")
	}
	return siaCredentials{
		IndexerURL:  strings.TrimRight(strings.TrimSpace(payload.IndexerURL), "/"),
		AppKeySeed:  seed,
		DataShards:  payload.DataShards,
		TotalShards: payload.TotalShards,
	}, nil
}

// sdkSiaObjects is siaObjects on the Sia SDK.
type sdkSiaObjects struct {
	sdk          *siastorage.SDK
	dataShards   uint8
	parityShards uint8
}

func connectSiaIndexer(ctx context.Context, credentials siaCredentials) (siaObjects, error) {
	appKey := types.NewPrivateKeyFromSeed(credentials.AppKeySeed)
	// The SDK checks the key without a deadline; checking first with ours
	// fails fast while the indexer is unreachable.
	authorized, err := app.NewClient(credentials.IndexerURL).CheckAppAuth(ctx, appKey)
	if err != nil {
		return nil, fmt.Errorf("reach sia indexer: %w", err)
	}
	if !authorized {
		return nil, errors.New("the sia indexer does not know this account's app key")
	}
	sdk, err := siastorage.NewBuilder(credentials.IndexerURL, siastorage.AppMetadata{Name: "Virtroid"}).
		SDK(appKey, siastorage.WithLogger(zap.NewNop()))
	if err != nil {
		return nil, fmt.Errorf("connect to sia indexer: %w", err)
	}
	return &sdkSiaObjects{
		sdk:          sdk,
		dataShards:   uint8(credentials.DataShards),
		parityShards: uint8(credentials.TotalShards - credentials.DataShards),
	}, nil
}

func (o *sdkSiaObjects) remaining(ctx context.Context) (int64, error) {
	account, err := o.sdk.Account(ctx)
	if err != nil {
		return 0, fmt.Errorf("read sia account: %w", err)
	}
	// account.Ready means funded on 30 hosts; an upload needs as many hosts as
	// it has shards, and reports it when there are too few.
	return int64(min(account.RemainingStorage, math.MaxInt64)), nil
}

func (o *sdkSiaObjects) put(ctx context.Context, meta siaObjectMeta, r io.Reader) (string, error) {
	encodedMeta, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	object := siastorage.NewEmptyObject()
	object.UpdateMetadata(encodedMeta)
	if err := o.sdk.Upload(ctx, &object, r, siastorage.WithRedundancy(o.dataShards, o.parityShards)); err != nil {
		return "", err
	}
	// Until the object is pinned its slabs belong to no object, and a prune
	// would release them.
	if err := o.sdk.PinObject(ctx, object); err != nil {
		return "", fmt.Errorf("pin object: %w", err)
	}
	id := object.ID()
	return hex.EncodeToString(id[:]), nil
}

func (o *sdkSiaObjects) open(ctx context.Context, objectID string) (io.ReadCloser, int64, error) {
	key, err := parseSiaObjectID(objectID)
	if err != nil {
		return nil, 0, err
	}
	object, err := o.sdk.Object(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	size := object.Size()
	if size > math.MaxInt64 {
		return nil, 0, errors.New("sia object is too large")
	}
	reader, err := o.sdk.Download(object)
	if err != nil {
		return nil, 0, err
	}
	// Download is not bound to a context; closing the reader cancels it.
	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	return &siaDownload{ReadCloser: reader, stop: stop}, int64(size), nil
}

type siaDownload struct {
	io.ReadCloser
	stop func() bool
}

func (d *siaDownload) Close() error {
	d.stop()
	return d.ReadCloser.Close()
}

func (o *sdkSiaObjects) remove(ctx context.Context, objectID string) error {
	key, err := parseSiaObjectID(objectID)
	if err != nil {
		return err
	}
	err = o.sdk.DeleteObject(ctx, key)
	var httpErr *app.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

// list returns the account's objects. indexd publishes a change to this
// listing about a second after it, so an object saved or deleted just now may
// not show yet; pruning keeps the newest snapshot by id, so it never depends on
// seeing it here.
func (o *sdkSiaObjects) list(ctx context.Context) ([]siaObjectInfo, error) {
	var listed []siaObjectInfo
	seen := make(map[types.Hash256]bool)
	var cursor slabs.Cursor
	for {
		events, err := o.sdk.ObjectEvents(ctx, cursor, siaListPageSize)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if event.Deleted || event.Object == nil || seen[event.Key] {
				continue
			}
			seen[event.Key] = true
			var meta siaObjectMeta
			if err := json.Unmarshal(event.Object.Metadata(), &meta); err != nil || meta.Kind != siaObjectKind {
				continue
			}
			listed = append(listed, siaObjectInfo{ID: hex.EncodeToString(event.Key[:]), Meta: meta})
		}
		if len(events) < siaListPageSize {
			return listed, nil
		}
		last := events[len(events)-1]
		cursor = slabs.Cursor{After: last.UpdatedAt, Key: last.Key}
	}
}

func (o *sdkSiaObjects) prune(ctx context.Context) error {
	// The indexer's default only prunes slabs pinned three days ago.
	before := time.Now().UTC().Format(time.RFC3339Nano)
	return o.sdk.PruneSlabs(ctx, func(values url.Values) { values.Set("before", before) })
}

func (o *sdkSiaObjects) close() error {
	return o.sdk.Close()
}

func parseSiaObjectID(objectID string) (types.Hash256, error) {
	var key types.Hash256
	decoded, err := hex.DecodeString(objectID)
	if err != nil || len(decoded) != len(key) {
		return key, fmt.Errorf("invalid sia object id %q", objectID)
	}
	copy(key[:], decoded)
	return key, nil
}
