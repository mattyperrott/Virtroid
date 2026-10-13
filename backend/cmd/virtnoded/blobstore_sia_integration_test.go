//go:build siaintegration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/siaindexd"
)

// TestSiaIndexerIntegration runs the sia-indexd store against a real indexd,
// with a fresh account each run:
//
//	SIA_TEST_APP_URL=http://127.0.0.1:9982 \
//	SIA_TEST_ADMIN_URL=http://127.0.0.1:9980/api \
//	SIA_TEST_ADMIN_PASSWORD_FILE=/path/to/admin-password \
//	SIA_TEST_DATA_SHARDS=10 SIA_TEST_TOTAL_SHARDS=30 SIA_TEST_SNAPSHOT_MIB=64 \
//	./virtnoded.test -test.run SiaIndexerIntegration -test.v -test.timeout 30m
//
// indexd only accepts redundancy between 1.5x and 4x that recovers a slab with
// at least 99.99% probability, which takes 12 or more hosts (3-of-12). The Zen
// testnet has fewer usable hosts, so uploads need mainnet.
func TestSiaIndexerIntegration(t *testing.T) {
	appURL := os.Getenv("SIA_TEST_APP_URL")
	if appURL == "" {
		t.Skip("SIA_TEST_APP_URL is not set")
	}
	password, err := os.ReadFile(os.Getenv("SIA_TEST_ADMIN_PASSWORD_FILE"))
	if err != nil {
		t.Fatalf("read admin password: %v", err)
	}
	dataShards := envInt(t, "SIA_TEST_DATA_SHARDS", 10)
	totalShards := envInt(t, "SIA_TEST_TOTAL_SHARDS", 30)
	snapshotMiB := envInt(t, "SIA_TEST_SNAPSHOT_MIB", 64)

	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	registry, err := siaindexd.New(siaindexd.Config{
		AppURL:          appURL,
		AdminURL:        os.Getenv("SIA_TEST_ADMIN_URL"),
		AdminPassword:   strings.TrimSpace(string(password)),
		KeySecret:       secret,
		Quota:           "virtroid-test",
		MaxPinnedBytes:  4 << 30,
		FundTargetBytes: 1 << 30,
		DataShards:      dataShards,
		TotalShards:     totalShards,
	}, nil)
	if err != nil {
		t.Fatalf("siaindexd.New: %v", err)
	}
	ctx := context.Background()
	accountID := fmt.Sprintf("integration-%d", time.Now().Unix())
	credentials, err := registry.Credentials(ctx, accountID)
	if err != nil {
		t.Fatalf("register account: %v", err)
	}
	t.Cleanup(func() {
		if err := registry.DeleteAccount(context.Background(), accountID); err != nil {
			t.Errorf("delete account: %v", err)
		}
	})

	store := &siaBlobStore{
		tempDir:   filepath.Join(t.TempDir(), "sia-tmp"),
		chunkSize: blobChunkSize,
		accounts:  &siaAccounts{},
		credentials: func(context.Context, string) (siaCredentials, error) {
			return siaCredentials{
				IndexerURL:  credentials.IndexerURL,
				AppKeySeed:  credentials.AppKeySeed,
				DataShards:  credentials.DataShards,
				TotalShards: credentials.TotalShards,
			}, nil
		},
		connect: connectSiaIndexer,
	}
	runtimeID := "runtime-integration"
	objects, _, err := store.account(ctx, runtimeID)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	sourceDir := filepath.Join(t.TempDir(), "source")
	payload := make([]byte, snapshotMiB<<20)
	_, _ = rand.Read(payload)
	writeFile(t, filepath.Join(sourceDir, "app", "blob.bin"), string(payload))
	writeFile(t, filepath.Join(sourceDir, "app", "state.db"), "first generation")
	key := testBlobKey(42)

	// A new account is funded on hosts within a few minutes.
	registered := time.Now()
	var first *blobManifest
	var started time.Time
	for {
		started = time.Now()
		first, err = store.persistFromDir(ctx, runtimeID, sourceDir, key)
		if err == nil {
			break
		}
		if time.Since(registered) > 10*time.Minute {
			t.Fatalf("persist: %v", err)
		}
		t.Logf("persist not possible yet: %v", err)
		time.Sleep(30 * time.Second)
	}
	elapsed := time.Since(started)
	t.Logf("persisted %d bytes in %s (%.1f MiB/s), object %s", first.TotalBytes, elapsed.Round(time.Millisecond),
		float64(first.TotalBytes)/(1<<20)/elapsed.Seconds(), first.SiaObject)

	restoreDir := filepath.Join(t.TempDir(), "restore")
	started = time.Now()
	if err := store.restoreToDir(ctx, runtimeID, first, restoreDir, key); err != nil {
		t.Fatalf("restore: %v", err)
	}
	elapsed = time.Since(started)
	t.Logf("restored in %s (%.1f MiB/s)", elapsed.Round(time.Millisecond), float64(first.TotalBytes)/(1<<20)/elapsed.Seconds())
	restored, err := os.ReadFile(filepath.Join(restoreDir, "app", "blob.bin"))
	if err != nil || !bytes.Equal(restored, payload) {
		t.Fatalf("restored payload differs (err=%v)", err)
	}
	assertFileContent(t, filepath.Join(restoreDir, "app", "state.db"), "first generation")

	writeFile(t, filepath.Join(sourceDir, "app", "state.db"), "second generation")
	second, err := store.persistFromDir(ctx, runtimeID, sourceDir, key)
	if err != nil {
		t.Fatalf("persist second generation: %v", err)
	}
	if err := store.pruneRuntime(ctx, runtimeID, second.Namespace, second.SnapshotID); err != nil {
		t.Fatalf("pruneRuntime: %v", err)
	}
	listed, err := objects.list(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != second.SiaObject {
		t.Fatalf("objects after prune = %+v, want only %s", listed, second.SiaObject)
	}

	if err := store.clearRuntime(ctx, runtimeID); err != nil {
		t.Fatalf("clearRuntime: %v", err)
	}
	if listed, err := objects.list(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("objects after clear = %+v (err=%v)", listed, err)
	}
}

func envInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}
