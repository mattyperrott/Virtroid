package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresSchemaAndLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()

	// Multiple replicas may start together during a deploy. The advisory lock
	// must serialize the idempotent schema batch without partial DDL failures.
	const replicas = 4
	var wg sync.WaitGroup
	errs := make(chan error, replicas)
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- st.EnsureSchema(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent EnsureSchema: %v", err)
		}
	}

	var schemaVersion int64
	if err := st.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		t.Fatalf("read schema migration ledger: %v", err)
	}
	if schemaVersion != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", schemaVersion, currentSchemaVersion)
	}

	accountID := uuid.NewString()
	deviceID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	if _, err := st.BootstrapAccountWithIdentity(ctx, accountID, deviceID, "CI device", "ci-public-key", CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap account: %v", err)
	}
	runtime, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "CI runtime"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if runtime.OperationGeneration != 1 {
		t.Fatalf("initial operation generation = %d, want 1", runtime.OperationGeneration)
	}
	if _, err := st.UpsertHostHeartbeat(ctx, HostHeartbeat{
		ID:            "ci-node",
		Name:          "CI node",
		AdvertiseAddr: "virtnoded",
		RelayPort:     8090,
		DockerSocket:  true,
		Binder:        true,
		PublicKey:     "ci-node-public-key",
		BlobStoreKind: "local-disk",
	}); err != nil {
		t.Fatalf("register test node: %v", err)
	}
	runtime, err = st.StartRuntimeOnHost(ctx, accountID, runtime.ID, "ci-node")
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	if runtime.OperationGeneration != 2 {
		t.Fatalf("start operation generation = %d, want 2", runtime.OperationGeneration)
	}
	if err := st.UpdateRuntimeObservation(ctx, runtime.ID, RuntimeObservation{
		HostID:              "ci-node",
		Status:              "running",
		ConnectionStatus:    "online",
		OperationGeneration: 1,
	}); !errors.Is(err, ErrRuntimeObservationStale) {
		t.Fatalf("stale runtime observation error = %v, want %v", err, ErrRuntimeObservationStale)
	}
	if err := st.UpdateRuntimeObservation(ctx, runtime.ID, RuntimeObservation{
		HostID:              "ci-node",
		Status:              "running",
		ConnectionStatus:    "online",
		OperationGeneration: runtime.OperationGeneration,
	}); err != nil {
		t.Fatalf("current runtime observation: %v", err)
	}
	runtime, err = st.StopRuntime(ctx, accountID, runtime.ID)
	if err != nil {
		t.Fatalf("stop runtime: %v", err)
	}
	if runtime.OperationGeneration != 3 {
		t.Fatalf("stop operation generation = %d, want 3", runtime.OperationGeneration)
	}
	if err := st.UpdateRuntimeObservation(ctx, runtime.ID, RuntimeObservation{
		HostID:              "ci-node",
		Status:              "stopped",
		ConnectionStatus:    "offline",
		CleanupComplete:     true,
		OperationGeneration: runtime.OperationGeneration,
	}); err != nil {
		t.Fatalf("stopped cleanup observation: %v", err)
	}

	expiresAt := time.Now().UTC().Add(time.Minute)
	lease := RuntimeBlobKeyHandoff{
		AccountID: accountID,
		RuntimeID: runtime.ID,
		HostID:    "ci-node",
		Operation: "start",
		LeaseID:   "ci-lease",
		ExpiresAt: expiresAt,
	}
	if err := st.PutRuntimeBlobKeyLease(ctx, lease); err != nil {
		t.Fatalf("persist blob-key lease: %v", err)
	}
	lease.EnvelopeJSON = `{"version":1,"ciphertext":"encrypted-for-node"}`
	lease.BlobKeyVerifier = "ci-verifier"
	if _, err := st.ActivateRuntimeBlobKeyHandoff(ctx, lease); err != nil {
		t.Fatalf("activate blob-key handoff: %v", err)
	}
	if _, err := st.GetRuntimeBlobKeyHandoff(ctx, runtime.ID, "ci-node"); err != nil {
		t.Fatalf("restore blob-key handoff: %v", err)
	}

	if err := st.DeleteAccount(ctx, accountID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if _, err := st.GetRuntimeBlobKeyHandoff(ctx, runtime.ID, "ci-node"); !errors.Is(err, ErrRuntimeBlobKeyHandoff) {
		t.Fatalf("handoff after account deletion error = %v, want %v", err, ErrRuntimeBlobKeyHandoff)
	}
}
