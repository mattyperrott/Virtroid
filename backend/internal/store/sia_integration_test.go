package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresSiaAccounts(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", strings.Fields(query)[0], err)
		}
	}
	newAccount := func() string {
		t.Helper()
		accountID := uuid.NewString()
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			_, _ = st.db.ExecContext(cleanupCtx, `DELETE FROM accounts WHERE id = $1`, accountID)
		})
		if _, err := st.BootstrapAccountWithIdentity(
			ctx, accountID, uuid.NewString(), "CI device", integrationNodePublicKey(t), CreateRuntimeInput{},
		); err != nil {
			t.Fatalf("bootstrap account: %v", err)
		}
		// Some checks below make several runtimes, more than a trial allows.
		exec(`UPDATE account_entitlements SET runtime_limit = 10 WHERE account_id = $1`, accountID)
		return accountID
	}
	newRuntime := func(accountID string) Runtime {
		t.Helper()
		runtime, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "CI sia runtime", FileMode: "upload-only"})
		if err != nil {
			t.Fatalf("create runtime: %v", err)
		}
		return runtime
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	alice, bob := newAccount(), newAccount()
	aliceVM, bobVM, shared := "ci-sia-alice-"+suffix, "ci-sia-bob-"+suffix, "ci-sia-shared-"+suffix
	for vm, accountID := range map[string]string{aliceVM: alice, bobVM: bob} {
		exec(`INSERT INTO provisioned_vms (id, account_id, region, state) VALUES ($1, $2, 'ci-sia', 'ready')`, vm, accountID)
		t.Cleanup(func() {
			_, _ = st.db.ExecContext(context.Background(), `DELETE FROM provisioned_vms WHERE id = $1`, vm)
		})
	}

	// Credentials go only to the runtime's current host, and never to another
	// account's VM.
	runtime := newRuntime(alice)
	if _, _, err := st.SiaCredentialAccount(ctx, runtime.ID, aliceVM); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("credentials for an unassigned runtime: err=%v, want ErrRuntimeNotFound", err)
	}
	for host, want := range map[string]string{aliceVM: alice, shared: alice, bobVM: ""} {
		exec(`UPDATE runtimes SET host_id = $2 WHERE id = $1`, runtime.ID, host)
		accountID, deleted, err := st.SiaCredentialAccount(ctx, runtime.ID, host)
		if want == "" {
			if !errors.Is(err, ErrRuntimeNotFound) {
				t.Fatalf("credentials on another account's VM: account=%s err=%v", accountID, err)
			}
			continue
		}
		if err != nil || accountID != want || deleted {
			t.Fatalf("credentials on %s: account=%s deleted=%v err=%v, want %s", host, accountID, deleted, err, want)
		}
	}

	// A Sia manifest leaves the runtime free to move; a local one pins it.
	var operation int64
	if err := st.db.QueryRowContext(ctx, `UPDATE runtimes SET host_id = $2 WHERE id = $1 RETURNING operation_generation`, runtime.ID, shared).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	for generation, report := range []struct {
		kind       string
		wantPinned bool
	}{{"local-disk", true}, {"sia-indexd", false}} {
		kind, wantPinned := report.kind, report.wantPinned
		manifest := testSiaManifest(kind, int64(generation)+1)
		if err := st.UpdateRuntimeObservation(ctx, runtime.ID, RuntimeObservation{
			HostID:              shared,
			Status:              "stopped",
			ConnectionStatus:    "offline",
			BlobStoreKind:       &kind,
			BlobManifestJSON:    &manifest,
			OperationGeneration: operation,
		}); err != nil {
			t.Fatalf("report %s manifest: %v", kind, err)
		}
		var blobHost sql.NullString
		if err := st.db.QueryRowContext(ctx, `SELECT blob_host_id FROM runtimes WHERE id = $1`, runtime.ID).Scan(&blobHost); err != nil {
			t.Fatal(err)
		}
		if blobHost.Valid != wantPinned || (wantPinned && blobHost.String != shared) {
			t.Fatalf("%s manifest: blob_host_id=%v, pinned want %v", kind, blobHost, wantPinned)
		}
	}

	// A deleted runtime's Sia snapshot is cleaned up by the account's own VM
	// or a shared node, never another account's VM.
	deleting := newRuntime(alice)
	exec(`UPDATE runtimes SET host_id = NULL, blob_host_id = NULL, desired_state = 'deleted', status = 'deleting',
	             blob_store_kind = 'sia-indexd', blob_manifest_json = $2 WHERE id = $1`, deleting.ID, testSiaManifest("sia-indexd", 1))
	if assigned, err := st.AssignPendingRemoteRuntimeCleanup(ctx, bobVM, "sia-indexd"); err != nil || assigned != 0 {
		t.Fatalf("cleanup assigned to another account's VM: %d err=%v", assigned, err)
	}
	if assigned, err := st.AssignPendingRemoteRuntimeCleanup(ctx, aliceVM, "sia-indexd"); err != nil || assigned != 1 {
		t.Fatalf("cleanup assigned to the account's VM: %d err=%v, want 1", assigned, err)
	}

	// Deleting the account: its credentials stop, and the release finishes the
	// runtimes that only waited for their Sia snapshots.
	if err := st.MarkAccountSiaRegistered(ctx, alice); err != nil {
		t.Fatalf("MarkAccountSiaRegistered: %v", err)
	}
	exec(`UPDATE runtimes SET host_id = NULL WHERE id = ANY($1)`, []string{runtime.ID, deleting.ID})
	if err := st.DeleteAccount(ctx, alice); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	exec(`UPDATE runtimes SET host_id = $2 WHERE id = $1`, deleting.ID, aliceVM)
	if _, deleted, err := st.SiaCredentialAccount(ctx, deleting.ID, aliceVM); err != nil || !deleted {
		t.Fatalf("credentials for a deleted account: deleted=%v err=%v, want deleted", deleted, err)
	}
	exec(`UPDATE runtimes SET host_id = NULL WHERE id = $1`, deleting.ID)
	pending, err := st.DeletedAccountsOnSia(ctx, 1000)
	if err != nil || !containsString(pending, alice) {
		t.Fatalf("deleted accounts on sia = %v err=%v, want %s", pending, err, alice)
	}
	finished, err := st.ReleaseAccountSia(ctx, alice, "sia-indexd")
	if err != nil || finished != 2 {
		t.Fatalf("ReleaseAccountSia finished %d err=%v, want 2", finished, err)
	}
	var remaining int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtimes WHERE account_id = $1 AND deleted_at IS NULL`, alice).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("runtimes left after release = %d err=%v", remaining, err)
	}
	if pending, err := st.DeletedAccountsOnSia(ctx, 1000); err != nil || containsString(pending, alice) {
		t.Fatalf("released account still pending: %v err=%v", pending, err)
	}
	if again, err := st.ReleaseAccountSia(ctx, alice, "sia-indexd"); err != nil || again != 0 {
		t.Fatalf("second release = %d err=%v, want 0", again, err)
	}
}

func testSiaManifest(kind string, generation int64) string {
	return fmt.Sprintf(`{"version":3,"store":%q,"snapshot_id":"snapshot-%d","generation":%d,"total_bytes":10,"chunks":[{"size":10}]}`,
		kind, generation, generation)
}
