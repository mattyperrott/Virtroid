package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	pendingBlobDeletionVersion = 1
	maxPendingBlobDeletions    = 128
	maxPendingDeletionBytes    = 64 << 20
)

type pendingBlobDeletions struct {
	Version   int             `json:"version"`
	Manifests []*blobManifest `json:"manifests"`
}

func (s *renterdBlobStore) deleteManifestObjects(ctx context.Context, manifest *blobManifest) error {
	if manifest == nil {
		return nil
	}
	if manifest.Store != blobStoreRenterd {
		return fmt.Errorf("cannot delete %q manifest from renterd", manifest.Store)
	}
	if err := validateBlobManifestForRuntime(manifest, manifest.RuntimeID); err != nil {
		return err
	}
	bucket := manifestBucket(manifest, s.bucketName())
	if bucket != s.bucketName() {
		return fmt.Errorf("manifest bucket %q does not match configured renterd bucket %q", bucket, s.bucketName())
	}
	var deleteErrors []error
	for _, chunk := range manifest.Chunks {
		if err := s.deleteObject(ctx, bucket, chunk.Key); err != nil {
			deleteErrors = append(deleteErrors, err)
		}
	}
	return errors.Join(deleteErrors...)
}

func (s *renterdBlobStore) enqueuePendingDeletion(manifest *blobManifest) error {
	if manifest == nil || strings.TrimSpace(s.cleanupPath) == "" {
		return nil
	}
	if s.cleanupMu != nil {
		s.cleanupMu.Lock()
		defer s.cleanupMu.Unlock()
	}
	pending, err := loadPendingBlobDeletions(s.cleanupPath)
	if err != nil {
		return err
	}
	identity := pendingBlobDeletionIdentity(manifest)
	for _, existing := range pending.Manifests {
		if pendingBlobDeletionIdentity(existing) == identity {
			return nil
		}
	}
	if len(pending.Manifests) >= maxPendingBlobDeletions {
		return fmt.Errorf("pending renterd deletion journal reached limit %d", maxPendingBlobDeletions)
	}
	copyManifest := *manifest
	copyManifest.Chunks = append([]blobChunk(nil), manifest.Chunks...)
	pending.Manifests = append(pending.Manifests, &copyManifest)
	return writePendingBlobDeletions(s.cleanupPath, pending)
}

func (s *renterdBlobStore) drainPendingDeletions(ctx context.Context) (int, error) {
	if strings.TrimSpace(s.cleanupPath) == "" {
		return 0, nil
	}
	if s.cleanupMu != nil {
		s.cleanupMu.Lock()
		defer s.cleanupMu.Unlock()
	}
	pending, err := loadPendingBlobDeletions(s.cleanupPath)
	if err != nil {
		return 0, err
	}
	if len(pending.Manifests) == 0 {
		return 0, nil
	}
	remaining := pending.Manifests[:0]
	var deleteErrors []error
	for _, manifest := range pending.Manifests {
		if err := s.deleteManifestObjects(ctx, manifest); err != nil {
			remaining = append(remaining, manifest)
			deleteErrors = append(deleteErrors, err)
		}
	}
	pending.Manifests = remaining
	if err := writePendingBlobDeletions(s.cleanupPath, pending); err != nil {
		return len(remaining), errors.Join(errors.Join(deleteErrors...), err)
	}
	return len(remaining), errors.Join(deleteErrors...)
}

func pendingBlobDeletionIdentity(manifest *blobManifest) string {
	if manifest == nil {
		return ""
	}
	return strings.Join([]string{manifest.Store, manifest.Bucket, manifest.RuntimeID, manifest.Namespace, manifest.SnapshotID}, "\x00")
}

func loadPendingBlobDeletions(path string) (pendingBlobDeletions, error) {
	pending := pendingBlobDeletions{Version: pendingBlobDeletionVersion}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return pending, nil
	}
	if err != nil {
		return pending, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return pending, errors.New("pending renterd deletion journal is not a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return pending, fmt.Errorf("pending renterd deletion journal mode is %04o, want 0600", info.Mode().Perm())
	}
	if info.Size() > maxPendingDeletionBytes {
		return pending, fmt.Errorf("pending renterd deletion journal exceeds %d bytes", maxPendingDeletionBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return pending, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxPendingDeletionBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pending); err != nil {
		return pendingBlobDeletions{}, fmt.Errorf("decode pending renterd deletion journal: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pendingBlobDeletions{}, errors.New("pending renterd deletion journal contains trailing data")
	}
	if pending.Version != pendingBlobDeletionVersion {
		return pendingBlobDeletions{}, fmt.Errorf("unsupported pending renterd deletion journal version %d", pending.Version)
	}
	if len(pending.Manifests) > maxPendingBlobDeletions {
		return pendingBlobDeletions{}, fmt.Errorf("pending renterd deletion journal exceeds entry limit %d", maxPendingBlobDeletions)
	}
	for _, manifest := range pending.Manifests {
		if manifest == nil || manifest.Store != blobStoreRenterd {
			return pendingBlobDeletions{}, errors.New("pending renterd deletion journal contains an invalid store")
		}
		if err := validateBlobManifestForRuntime(manifest, manifest.RuntimeID); err != nil {
			return pendingBlobDeletions{}, fmt.Errorf("validate pending renterd deletion: %w", err)
		}
	}
	return pending, nil
}

func writePendingBlobDeletions(path string, pending pendingBlobDeletions) error {
	payload, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if len(payload) > maxPendingDeletionBytes {
		return fmt.Errorf("pending renterd deletion journal exceeds %d bytes", maxPendingDeletionBytes)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".pending-renterd-deletes-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}
