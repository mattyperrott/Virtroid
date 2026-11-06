package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

// Sealed imports wait on disk between the phone and the VM. A phone's uplink can
// take minutes over one, so holding them in memory would let a few slow uploads
// exhaust this server. They are sealed for the VM, so the disk only ever holds
// ciphertext this server can't read.
const (
	spooledImportPattern = "import-*"
	// Two imports at the largest size at once.
	runtimeImportAccountBudgetBytes = 2 * (maxRuntimeFileImportBytes + sealedImportOverhead)
)

var (
	errImportSpoolBusy   = errors.New("import spool has no room")
	errSpooledImportSize = errors.New("import is empty or too large")
	errSpooledImportHash = errors.New("import does not match its signed hash")
)

type importSpool struct {
	dir    string
	budget *byteBudget
}

// newImportSpool prepares dir, removing what an earlier process left there:
// nothing still needs it, since a phone whose upload was cut off sends it again.
func newImportSpool(dir string, maxBytes int64) (*importSpool, error) {
	if dir == "" {
		return nil, errors.New("no import spool directory is set")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	stale, err := filepath.Glob(filepath.Join(dir, spooledImportPattern))
	if err != nil {
		return nil, err
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	probe, err := os.CreateTemp(dir, spooledImportPattern)
	if err != nil {
		return nil, err
	}
	_ = probe.Close()
	if err := os.Remove(probe.Name()); err != nil {
		return nil, err
	}
	return &importSpool{dir: dir, budget: newByteBudget(maxBytes, runtimeImportAccountBudgetBytes)}, nil
}

// spooledImport is an import on disk whose hash has been checked.
type spooledImport struct {
	path     string
	size     int64
	bodyHash string
	release  func()
}

func (s *spooledImport) open() (io.ReadCloser, error) {
	return os.Open(s.path)
}

func (s *spooledImport) discard() {
	_ = os.Remove(s.path)
	s.release()
}

// receive writes an import to disk within the account's share of the budget.
// declaredSize is the request's Content-Length, negative when it has none.
func (s *importSpool) receive(accountID string, body io.Reader, declaredSize, maxSize int64, bodyHash string) (*spooledImport, error) {
	if declaredSize > maxSize {
		return nil, errSpooledImportSize
	}
	charge := declaredSize
	if charge < 0 {
		charge = maxSize
	}
	release, ok := s.budget.reserve(accountID, charge)
	if !ok {
		return nil, errImportSpoolBusy
	}
	file, err := os.CreateTemp(s.dir, spooledImportPattern)
	if err != nil {
		release()
		return nil, fmt.Errorf("create spooled import: %w", err)
	}
	spooled := &spooledImport{path: file.Name(), bodyHash: bodyHash, release: release}
	digest := sha256.New()
	// Never more than maxSize+1 reaches the disk, however the body is framed.
	spooled.size, err = io.Copy(io.MultiWriter(file, digest), io.LimitReader(body, maxSize+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	switch {
	case err != nil:
	case spooled.size == 0 || spooled.size > maxSize:
		err = errSpooledImportSize
	case base64.RawURLEncoding.EncodeToString(digest.Sum(nil)) != bodyHash:
		err = errSpooledImportHash
	}
	if err != nil {
		spooled.discard()
		return nil, err
	}
	return spooled, nil
}

// spoolSealedImport takes an import's body from the phone, answering the
// request itself when that fails.
func (a *API) spoolSealedImport(w http.ResponseWriter, r *http.Request, signed signedCapabilityRequest, kind string, maxBytes int64) (*spooledImport, bool) {
	if a.importSpool == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_import_unavailable", "imports are unavailable right now")
		return nil, false
	}
	spooled, err := a.importSpool.receive(signed.actor.accountID, r.Body, r.ContentLength, maxBytes+sealedImportOverhead, signed.bodyHash)
	if err == nil {
		return spooled, true
	}
	var maxBytesErr *http.MaxBytesError
	var diskErr *fs.PathError
	switch {
	case errors.Is(err, errImportSpoolBusy):
		w.Header().Set("Retry-After", requestBudgetRetryAfter)
		writeAPIError(w, http.StatusTooManyRequests, "runtime_import_busy", "too many imports are in progress; try again shortly")
	case errors.Is(err, errSpooledImportSize):
		writeAPIError(w, http.StatusRequestEntityTooLarge, kind+"_too_large", "sealed "+kind+" is empty or too large")
	case errors.As(err, &maxBytesErr):
		writeAPIError(w, http.StatusRequestEntityTooLarge, kind+"_too_large", "sealed "+kind+" is too large")
	case errors.Is(err, errSpooledImportHash):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "runtime capability signature body hash mismatch"})
	case errors.As(err, &diskErr):
		writeServerAPIError(w, http.StatusServiceUnavailable, "runtime_import_unavailable", "imports are unavailable right now", err)
	default:
		writeAPIError(w, http.StatusBadRequest, kind+"_read_failed", "could not read sealed "+kind)
	}
	return nil, false
}
