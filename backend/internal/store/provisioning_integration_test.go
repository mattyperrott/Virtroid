package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresProvisionedVMLifecycle(t *testing.T) {
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

	var baseline int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provisioned_vms WHERE state <> 'deleted'`).Scan(&baseline); err != nil {
		t.Fatalf("count live VMs: %v", err)
	}
	region := "ci-snp-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	policy := ProvisioningPolicy{
		Regions:      []string{region},
		MaxVMs:       baseline + 2,
		SetupTimeout: 30 * time.Minute,
		RetireAfter:  time.Hour,
	}
	var vmIDs []string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, id := range vmIDs {
			_, _ = st.db.ExecContext(cleanupCtx, `DELETE FROM provisioned_vms WHERE id = $1`, id)
		}
	})
	ensure := func(accountID string) (ProvisionedVM, error) {
		t.Helper()
		vm, err := st.EnsureAccountVM(ctx, accountID, region, policy)
		if err == nil && !containsString(vmIDs, vm.ID) {
			vmIDs = append(vmIDs, vm.ID)
		}
		return vm, err
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
		if err := st.SetAccountRuntimeRegion(ctx, accountID, region); err != nil {
			t.Fatalf("set account region: %v", err)
		}
		return accountID
	}
	startRuntime := func(accountID string) (Runtime, error) {
		t.Helper()
		runtime, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "CI runtime", FileMode: "upload-only"})
		if err != nil {
			t.Fatalf("create runtime: %v", err)
		}
		return st.StartRuntime(ctx, accountID, runtime.ID)
	}

	alice, bob := newAccount(), newAccount()
	if region, err := st.AccountProvisionedRegion(ctx, alice, policy); err != nil || region != policy.Regions[0] {
		t.Fatalf("provisioned region = %q err=%v", region, err)
	}
	if _, err := startRuntime(alice); !errors.Is(err, ErrNoReadyHost) {
		t.Fatalf("start before any VM error = %v, want ErrNoReadyHost", err)
	}

	aliceVM, err := ensure(alice)
	if err != nil || aliceVM.State != ProvisionedVMRequested || aliceVM.AccountID == nil || *aliceVM.AccountID != alice {
		t.Fatalf("alice VM = %+v err=%v", aliceVM, err)
	}
	if again, err := ensure(alice); err != nil || again.ID != aliceVM.ID {
		t.Fatalf("second ensure = %q err=%v, want the same VM %q", again.ID, err, aliceVM.ID)
	}
	bobVM, err := ensure(bob)
	if err != nil || bobVM.ID == aliceVM.ID {
		t.Fatalf("bob VM = %+v err=%v", bobVM, err)
	}
	if _, err := ensure(newAccount()); !errors.Is(err, ErrProvisioningCapacity) {
		t.Fatalf("third VM error = %v, want ErrProvisioningCapacity", err)
	}

	// Alice's VM goes through every provisioner step and comes up.
	if _, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{State: ProvisionedVMBooting}); !errors.Is(err, ErrProvisionedVMTransition) {
		t.Fatalf("skipping to booting error = %v, want ErrProvisionedVMTransition", err)
	}
	itemHash := strings.Repeat("ab", 32)
	if vm, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{State: ProvisionedVMCreating, ItemHash: itemHash}); err != nil || vm.ItemHash != itemHash {
		t.Fatalf("creating report = %+v err=%v", vm, err)
	}
	if vm, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{Progress: "downloading the VM image"}); err != nil ||
		vm.Progress != "downloading the VM image" || vm.State != ProvisionedVMCreating {
		t.Fatalf("progress report = %+v err=%v", vm, err)
	}
	nodeKey := integrationNodePublicKey(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, query := range []string{
			`DELETE FROM node_request_nonces WHERE node_id = $1`,
			`DELETE FROM hosts WHERE id = $1`,
			`DELETE FROM node_registry_audit WHERE node_id = $1`,
			`DELETE FROM approved_nodes WHERE node_id = $1`,
		} {
			_, _ = st.db.ExecContext(cleanupCtx, query, aliceVM.ID)
		}
	})
	if _, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{
		State:         ProvisionedVMUnlocking,
		PublicIP:      "fe80::1",
		NodePublicKey: nodeKey,
	}); err == nil {
		t.Fatal("a link-local VM address was accepted")
	}
	vm, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{
		State:         ProvisionedVMUnlocking,
		PublicIP:      "2001:db8:3100:1781::10",
		NodePublicKey: nodeKey,
	})
	if err != nil || vm.State != ProvisionedVMUnlocking || vm.PublicIP != "2001:db8:3100:1781::10" || vm.Progress != "" {
		t.Fatalf("unlocking report = %+v err=%v (a state change clears the old progress note)", vm, err)
	}
	if keys, err := st.AuthorizedNodeKeys(ctx, aliceVM.ID); err != nil || len(keys) != 1 {
		t.Fatalf("approved keys = %v err=%v, want the VM's node key", keys, err)
	}
	if _, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{
		State:         ProvisionedVMUnlocking,
		PublicIP:      "2001:db8:3100:1781::10",
		NodePublicKey: integrationNodePublicKey(t),
	}); err == nil {
		t.Fatal("a second node key for the same VM was accepted")
	}
	if _, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{State: ProvisionedVMBooting}); err != nil {
		t.Fatalf("booting report: %v", err)
	}
	if _, err := st.UpsertHostHeartbeat(ctx, HostHeartbeat{
		ID:             aliceVM.ID,
		Name:           "CI provisioned VM",
		AdvertiseAddr:  "2001:db8:3100:1781::10",
		RelayPort:      8090,
		DockerSocket:   true,
		Binder:         true,
		AudioStreaming: true,
		FileImport:     true,
		PublicKey:      vm.NodePublicKey,
		BlobStoreKind:  "local-disk",
		Region:         region,
	}); err != nil {
		t.Fatalf("VM heartbeat: %v", err)
	}
	if err := st.MarkProvisionedVMReady(ctx, aliceVM.ID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if vm, err := st.ProvisionedVM(ctx, aliceVM.ID); err != nil || vm.State != ProvisionedVMReady || vm.ReadyAt == nil {
		t.Fatalf("after heartbeat = %+v err=%v, want ready", vm, err)
	}

	// The VM is Alice's alone, even though it is in Bob's region too.
	if _, err := startRuntime(bob); !errors.Is(err, ErrNoReadyHost) {
		t.Fatalf("bob start on alice's VM error = %v, want ErrNoReadyHost", err)
	}
	aliceRuntime, err := startRuntime(alice)
	if err != nil || valueOrEmpty(aliceRuntime.HostID) != aliceVM.ID {
		t.Fatalf("alice runtime host = %q err=%v, want %q", valueOrEmpty(aliceRuntime.HostID), err, aliceVM.ID)
	}

	// A failed VM is retired but still counts until the provisioner deletes it.
	if vm, err := st.ApplyProvisionerReport(ctx, bobVM.ID, ProvisionerReport{Failure: "attestation failed"}); err != nil ||
		vm.Desired != "absent" || vm.RetireReason != "failed" {
		t.Fatalf("failure report = %+v err=%v", vm, err)
	}
	if _, err := ensure(bob); !errors.Is(err, ErrProvisioningCapacity) {
		t.Fatalf("ensure while the failed VM is undeleted error = %v, want ErrProvisioningCapacity", err)
	}
	if vm, err := st.ApplyProvisionerReport(ctx, bobVM.ID, ProvisionerReport{State: ProvisionedVMDeleted}); err != nil ||
		vm.State != ProvisionedVMDeleted || vm.DeletedAt == nil {
		t.Fatalf("deleted report = %+v err=%v", vm, err)
	}
	newBobVM, err := ensure(bob)
	if err != nil || newBobVM.ID == bobVM.ID {
		t.Fatalf("bob's replacement VM = %+v err=%v", newBobVM, err)
	}

	// A VM with a runtime on it is kept; once the runtime is gone and the VM
	// has been unused for RetireAfter, it is retired.
	if _, err := st.db.ExecContext(ctx, `UPDATE provisioned_vms SET last_used_at = NOW() - INTERVAL '2 hours' WHERE id = $1`, aliceVM.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReconcileProvisionedVMs(ctx, policy); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if vm, _ := st.ProvisionedVM(ctx, aliceVM.ID); vm.Desired != "present" {
		t.Fatalf("VM with a runtime was retired: %+v", vm)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE runtimes SET deleted_at = NOW() WHERE id = $1`, aliceRuntime.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE provisioned_vms SET last_used_at = NOW() - INTERVAL '2 hours' WHERE id = $1`, aliceVM.ID); err != nil {
		t.Fatal(err)
	}
	summary, err := st.ReconcileProvisionedVMs(ctx, policy)
	if err != nil || summary.Retired != 1 {
		t.Fatalf("reconcile summary = %+v err=%v, want one retired VM", summary, err)
	}
	if vm, _ := st.ProvisionedVM(ctx, aliceVM.ID); vm.Desired != "absent" || vm.RetireReason != "unused" {
		t.Fatalf("unused VM = %+v, want retired as unused", vm)
	}

	// Deleting the VM revokes its node key.
	if _, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{State: ProvisionedVMDeleted}); err != nil {
		t.Fatalf("delete alice VM: %v", err)
	}
	if keys, err := st.AuthorizedNodeKeys(ctx, aliceVM.ID); err == nil && len(keys) > 0 {
		t.Fatalf("deleted VM still has authorized keys: %v", keys)
	}

	// Runtimes whose data lived on a deleted VM start fresh instead of waiting
	// for it, and their log says why; a deletion waiting on it completes.
	newRuntime := func(name string) Runtime {
		t.Helper()
		runtime, err := st.CreateRuntime(ctx, newAccount(), CreateRuntimeInput{Name: name, FileMode: "upload-only"})
		if err != nil {
			t.Fatalf("create runtime: %v", err)
		}
		return runtime
	}
	orphan, deleting := newRuntime("CI orphan"), newRuntime("CI deleting")
	if _, err := st.db.ExecContext(ctx,
		`UPDATE runtimes SET blob_host_id = $2, blob_store_kind = 'local-disk', blob_manifest_json = '{"chunks":[]}',
		        blob_last_snapshot_at = NOW() WHERE id = $1`, orphan.ID, aliceVM.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE runtimes SET host_id = $2, desired_state = 'deleted', status = 'deleting' WHERE id = $1`,
		deleting.ID, aliceVM.ID); err != nil {
		t.Fatal(err)
	}
	// Snapshots on Sia outlive the VM.
	onSia, deletingOnSia := newRuntime("CI on sia"), newRuntime("CI deleting on sia")
	if _, err := st.db.ExecContext(ctx,
		`UPDATE runtimes SET host_id = $2, blob_store_kind = 'sia-indexd', blob_manifest_json = '{"store":"sia-indexd"}',
		        blob_last_snapshot_at = NOW() WHERE id = $1`, onSia.ID, aliceVM.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE runtimes SET host_id = $2, desired_state = 'deleted', status = 'deleting', blob_store_kind = 'sia-indexd',
		        blob_manifest_json = '{"store":"sia-indexd"}' WHERE id = $1`, deletingOnSia.ID, aliceVM.ID); err != nil {
		t.Fatal(err)
	}
	summary, err = st.ReconcileProvisionedVMs(ctx, policy)
	if err != nil || summary.RuntimesReset != 4 {
		t.Fatalf("reconcile summary = %+v err=%v, want four runtimes reset", summary, err)
	}
	for _, check := range []struct {
		id, status string
	}{{onSia.ID, "stopped"}, {deletingOnSia.ID, "deleting"}} {
		var host, manifest sql.NullString
		var status string
		var deletedAt sql.NullTime
		var logs int
		if err := st.db.QueryRowContext(ctx,
			`SELECT host_id, blob_manifest_json, status, deleted_at,
			        (SELECT COUNT(*) FROM runtime_logs WHERE runtime_id = runtimes.id AND message = $2)
			   FROM runtimes WHERE id = $1`, check.id, lostRuntimeDataMessage,
		).Scan(&host, &manifest, &status, &deletedAt, &logs); err != nil {
			t.Fatal(err)
		}
		if host.Valid || !manifest.Valid || status != check.status || deletedAt.Valid || logs != 0 {
			t.Fatalf("sia runtime host=%v manifest=%v status=%s deleted_at=%v lost-data logs=%d, want its snapshot kept and status %s",
				host, manifest, status, deletedAt, logs, check.status)
		}
	}
	var hostID, blobHostID, manifest sql.NullString
	var status, desired string
	if err := st.db.QueryRowContext(ctx,
		`SELECT host_id, blob_host_id, blob_manifest_json, status, desired_state FROM runtimes WHERE id = $1`, orphan.ID,
	).Scan(&hostID, &blobHostID, &manifest, &status, &desired); err != nil {
		t.Fatal(err)
	}
	if hostID.Valid || blobHostID.Valid || manifest.Valid || status != "stopped" || desired != "stopped" {
		t.Fatalf("orphaned runtime host=%v blob_host=%v manifest=%v status=%s desired=%s, want a fresh stopped runtime",
			hostID, blobHostID, manifest, status, desired)
	}
	var logged int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtime_logs WHERE runtime_id = $1 AND message = $2`, orphan.ID, lostRuntimeDataMessage,
	).Scan(&logged); err != nil || logged != 1 {
		t.Fatalf("lost-data log entries = %d err=%v, want 1", logged, err)
	}
	var deletedAt sql.NullTime
	if err := st.db.QueryRowContext(ctx, `SELECT deleted_at FROM runtimes WHERE id = $1`, deleting.ID).Scan(&deletedAt); err != nil || !deletedAt.Valid {
		t.Fatalf("deletion waiting on the deleted VM: deleted_at=%v err=%v, want it completed", deletedAt, err)
	}
	if summary, err := st.ReconcileProvisionedVMs(ctx, policy); err != nil || summary.RuntimesReset != 0 {
		t.Fatalf("second reconcile = %+v err=%v, want nothing left to reset", summary, err)
	}

	// Warm spares fill free capacity and are handed to the next account.
	sparePolicy := policy
	sparePolicy.WarmSpares = 1
	summary, err = st.ReconcileProvisionedVMs(ctx, sparePolicy)
	if err != nil || summary.SparesCreated != 1 {
		t.Fatalf("spare reconcile = %+v err=%v, want one spare", summary, err)
	}
	var spareID string
	if err := st.db.QueryRowContext(ctx,
		`SELECT id FROM provisioned_vms WHERE region = $1 AND account_id IS NULL AND state <> 'deleted'`, region,
	).Scan(&spareID); err != nil {
		t.Fatalf("find spare: %v", err)
	}
	vmIDs = append(vmIDs, spareID)
	carol := newAccount()
	carolVM, err := ensure(carol)
	if err != nil || carolVM.ID != spareID {
		t.Fatalf("carol VM = %q err=%v, want the spare %q", carolVM.ID, err, spareID)
	}
}
