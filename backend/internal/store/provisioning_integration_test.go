package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
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
		TrialMaxVMs:  baseline + 3,
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
		vm, err := st.EnsureAccountVM(ctx, accountID, region, policy, VMRequest{})
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
		// Each start below makes a new runtime, more than a trial allows.
		if _, err := st.db.ExecContext(ctx,
			`UPDATE account_entitlements SET runtime_limit = 10 WHERE account_id = $1`, accountID,
		); err != nil {
			t.Fatalf("raise runtime limit: %v", err)
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

	// Deleting the VM revokes its node key and removes its host.
	if _, err := st.ApplyProvisionerReport(ctx, aliceVM.ID, ProvisionerReport{State: ProvisionedVMDeleted}); err != nil {
		t.Fatalf("delete alice VM: %v", err)
	}
	if keys, err := st.AuthorizedNodeKeys(ctx, aliceVM.ID); err == nil && len(keys) > 0 {
		t.Fatalf("deleted VM still has authorized keys: %v", keys)
	}
	var hostRows int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM hosts WHERE id = $1`, aliceVM.ID).Scan(&hostRows); err != nil || hostRows != 0 {
		t.Fatalf("deleted VM's host rows = %d err=%v, want none", hostRows, err)
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
		`UPDATE runtimes SET blob_host_id = $2, blob_store_kind = 'local-disk',
		        blob_manifest_json = '{"generation":4,"chunks":[],"migration_fallback":{"generation":3,"chunks":[]}}',
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
	var floor int64
	if err := st.db.QueryRowContext(ctx,
		`SELECT host_id, blob_host_id, blob_manifest_json, status, desired_state, blob_generation_floor
		   FROM runtimes WHERE id = $1`, orphan.ID,
	).Scan(&hostID, &blobHostID, &manifest, &status, &desired, &floor); err != nil {
		t.Fatal(err)
	}
	if hostID.Valid || blobHostID.Valid || manifest.Valid || status != "stopped" || desired != "stopped" {
		t.Fatalf("orphaned runtime host=%v blob_host=%v manifest=%v status=%s desired=%s, want a fresh stopped runtime",
			hostID, blobHostID, manifest, status, desired)
	}
	// Phones see floor + generation keep rising past the lost generation 4.
	if floor != 4 {
		t.Fatalf("orphaned runtime generation floor = %d, want 4", floor)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT blob_generation_floor FROM runtimes WHERE id = $1`, onSia.ID).Scan(&floor); err != nil || floor != 0 {
		t.Fatalf("sia runtime generation floor = %d err=%v, want 0", floor, err)
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

func TestPostgresRetiresReadyVMsWhoseNodeWentSilent(t *testing.T) {
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
	var otherReady int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM provisioned_vms WHERE desired = 'present' AND state = 'ready'`).Scan(&otherReady); err != nil {
		t.Fatal(err)
	}
	if otherReady != 0 {
		t.Skipf("%d ready VMs left by other tests would change the mass-silence guard", otherReady)
	}

	region := "ci-snp-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	var ids []string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, id := range ids {
			_, _ = st.db.ExecContext(cleanupCtx, `DELETE FROM hosts WHERE id = $1`, id)
			_, _ = st.db.ExecContext(cleanupCtx, `DELETE FROM provisioned_vms WHERE id = $1`, id)
		}
	})
	// readyVM is a ready spare whose node last reported quietFor ago, or
	// that never reported when quietFor is negative.
	readyVM := func(quietFor time.Duration) string {
		t.Helper()
		id := "pvm-ci-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
		ids = append(ids, id)
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO provisioned_vms (id, region, state, desired, ready_at, state_changed_at)
			 VALUES ($1, $2, 'ready', 'present', NOW() - INTERVAL '3 hours', NOW() - INTERVAL '3 hours')`, id, region); err != nil {
			t.Fatal(err)
		}
		if quietFor >= 0 {
			if _, err := st.db.ExecContext(ctx,
				`INSERT INTO hosts (id, name, advertise_addr, last_heartbeat_at)
				 VALUES ($1, 'CI VM', '2001:db8::10', NOW() - make_interval(secs => $2))`, id, quietFor.Seconds()); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	desired := func(id string) (string, string) {
		t.Helper()
		var d, reason string
		if err := st.db.QueryRowContext(ctx, `SELECT desired, retire_reason FROM provisioned_vms WHERE id = $1`, id).Scan(&d, &reason); err != nil {
			t.Fatal(err)
		}
		return d, reason
	}
	policy := ProvisioningPolicy{
		Regions:         []string{region},
		SetupTimeout:    30 * time.Minute,
		NodeSilentAfter: time.Hour,
		UpSince:         time.Now().Add(-2 * time.Hour),
	}

	wedged := readyVM(2 * time.Hour)
	neverReported := readyVM(-1)
	healthy := readyVM(10 * time.Second)
	alsoHealthy := readyVM(20 * time.Second)
	alsoHealthy2 := readyVM(5 * time.Second)

	// Not while the control plane itself has been up for less than the limit.
	fresh := policy
	fresh.UpSince = time.Now()
	if summary, err := st.ReconcileProvisionedVMs(ctx, fresh); err != nil || summary.NodeSilent != 0 {
		t.Fatalf("reconcile just after start = %+v err=%v, want nothing retired", summary, err)
	}

	summary, err := st.ReconcileProvisionedVMs(ctx, policy)
	if err != nil || summary.NodeSilent != 2 || summary.NodeSilentHeld != 0 {
		t.Fatalf("reconcile = %+v err=%v, want the two silent VMs retired", summary, err)
	}
	for _, id := range []string{wedged, neverReported} {
		if d, reason := desired(id); d != "absent" || reason != "node-silent" {
			t.Fatalf("silent VM %s desired=%s reason=%s, want retired as node-silent", id, d, reason)
		}
	}
	for _, id := range []string{healthy, alsoHealthy, alsoHealthy2} {
		if d, _ := desired(id); d != "present" {
			t.Fatalf("reporting VM %s was retired", id)
		}
	}

	// When most ready VMs go quiet at once, none is retired.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE hosts SET last_heartbeat_at = NOW() - INTERVAL '2 hours' WHERE id = ANY($1)`,
		[]string{healthy, alsoHealthy}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE provisioned_vms SET desired = 'present', retire_reason = '' WHERE id = $1`, wedged); err != nil {
		t.Fatal(err)
	}
	summary, err = st.ReconcileProvisionedVMs(ctx, policy)
	if err != nil || summary.NodeSilent != 0 || summary.NodeSilentHeld != 3 {
		t.Fatalf("mass-silence reconcile = %+v err=%v, want 3 held and none retired", summary, err)
	}

	// A silent ready spare is not handed to an account; a reporting one is.
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
	claimPolicy := policy
	claimPolicy.MaxVMs = 100
	claimPolicy.TrialMaxVMs = 100
	vm, err := st.EnsureAccountVM(ctx, accountID, region, claimPolicy, VMRequest{})
	if err != nil || vm.ID != alsoHealthy2 {
		t.Fatalf("claimed VM %q err=%v, want the reporting spare %q", vm.ID, err, alsoHealthy2)
	}
}

func TestPostgresCheckRuntimeStartEntitlementChangesNothing(t *testing.T) {
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
	if _, err := st.db.ExecContext(ctx,
		`UPDATE account_entitlements SET runtime_limit = 10 WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	runtime, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "CI runtime", FileMode: "upload-only"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := st.CheckRuntimeStartEntitlement(ctx, accountID, runtime.ID); err != nil {
		t.Fatalf("entitled start check = %v, want nil", err)
	}
	if err := st.CheckRuntimeStartEntitlement(ctx, accountID, uuid.NewString()); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("unknown runtime check = %v, want ErrRuntimeNotFound", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE account_entitlements SET runtime_starts_per_day = 0 WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckRuntimeStartEntitlement(ctx, accountID, runtime.ID); !errors.Is(err, ErrRuntimeStartQuota) {
		t.Fatalf("no-starts check = %v, want ErrRuntimeStartQuota", err)
	}
	// A runtime already meant to run is startRuntime's to answer.
	if _, err := st.db.ExecContext(ctx, `UPDATE runtimes SET desired_state = 'running' WHERE id = $1`, runtime.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckRuntimeStartEntitlement(ctx, accountID, runtime.ID); err != nil {
		t.Fatalf("running runtime check = %v, want nil", err)
	}
	var starts int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_start_events WHERE account_id = $1`, accountID).Scan(&starts); err != nil || starts != 0 {
		t.Fatalf("start events = %d err=%v, want none recorded by a check", starts, err)
	}
}

func TestPostgresHostHeartbeatKeepsTheSiaIndexdStoreKind(t *testing.T) {
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
	hostID := "ci-host-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.db.ExecContext(cleanupCtx, `DELETE FROM hosts WHERE id = $1`, hostID)
	})
	for kind, want := range map[string]string{"sia-indexd": "sia-indexd", "SIA-INDEXD ": "sia-indexd", "nonsense": "local-disk"} {
		host, err := st.UpsertHostHeartbeat(ctx, HostHeartbeat{
			ID:            hostID,
			Name:          "CI host",
			AdvertiseAddr: "2001:db8::20",
			RelayPort:     8090,
			BlobStoreKind: kind,
		})
		if err != nil || host.BlobStoreKind != want {
			t.Fatalf("heartbeat with %q recorded %q err=%v, want %q", kind, host.BlobStoreKind, err, want)
		}
	}
}

// trialVMFixture is a region of its own, with accounts of any entitlement.
type trialVMFixture struct {
	t      *testing.T
	ctx    context.Context
	st     *Store
	region string
	policy ProvisioningPolicy
}

func newTrialVMFixture(t *testing.T, ctx context.Context) (*trialVMFixture, int) {
	t.Helper()
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}
	st, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	region := "ci-trial-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.db.ExecContext(cleanupCtx, `DELETE FROM provisioned_vms WHERE region = $1`, region)
	})
	var trialsBefore int
	if err := st.db.QueryRowContext(ctx, trialVMsSQL).Scan(&trialsBefore); err != nil {
		t.Fatalf("count trial VMs: %v", err)
	}
	return &trialVMFixture{t: t, ctx: ctx, st: st, region: region, policy: ProvisioningPolicy{
		Regions:      []string{region},
		MaxVMs:       1000,
		SetupTimeout: 30 * time.Minute,
		RetireAfter:  time.Hour,
	}}, trialsBefore
}

func (f *trialVMFixture) account(source string) string {
	f.t.Helper()
	accountID := uuid.NewString()
	f.t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = f.st.db.ExecContext(cleanupCtx, `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	if _, err := f.st.BootstrapAccountWithIdentity(
		f.ctx, accountID, uuid.NewString(), "CI device", integrationNodePublicKey(f.t), CreateRuntimeInput{},
	); err != nil {
		f.t.Fatalf("bootstrap account: %v", err)
	}
	if err := f.st.SetAccountRuntimeRegion(f.ctx, accountID, f.region); err != nil {
		f.t.Fatalf("set account region: %v", err)
	}
	if _, err := f.st.db.ExecContext(f.ctx,
		`UPDATE account_entitlements SET source = $2 WHERE account_id = $1`, accountID, source,
	); err != nil {
		f.t.Fatalf("set entitlement: %v", err)
	}
	return accountID
}

// runtime makes a runtime for accountID, holding a stored snapshot if withData.
func (f *trialVMFixture) runtime(accountID string, withData bool) string {
	f.t.Helper()
	runtime, err := f.st.CreateRuntime(f.ctx, accountID, CreateRuntimeInput{Name: "CI runtime", FileMode: "upload-only"})
	if err != nil {
		f.t.Fatalf("create runtime: %v", err)
	}
	if withData {
		if _, err := f.st.db.ExecContext(f.ctx,
			`UPDATE runtimes SET blob_store_kind = 'sia-indexd', blob_manifest_json = '{"version":3,"generation":1,"total_bytes":1}' WHERE id = $1`,
			runtime.ID,
		); err != nil {
			f.t.Fatalf("store a snapshot: %v", err)
		}
	}
	return runtime.ID
}

func (f *trialVMFixture) ensure(accountID string, request VMRequest) (ProvisionedVM, error) {
	f.t.Helper()
	return f.st.EnsureAccountVM(f.ctx, accountID, f.region, f.policy, request)
}

func (f *trialVMFixture) mustEnsure(accountID string, request VMRequest) ProvisionedVM {
	f.t.Helper()
	vm, err := f.ensure(accountID, request)
	if err != nil {
		f.t.Fatalf("ensure VM: %v", err)
	}
	return vm
}

func (f *trialVMFixture) refused(accountID string, request VMRequest, want error) {
	f.t.Helper()
	if vm, err := f.ensure(accountID, request); !errors.Is(err, want) {
		f.t.Fatalf("ensure VM = %q, %v, want %v", vm.ID, err, want)
	}
	var held int
	if err := f.st.db.QueryRowContext(f.ctx,
		`SELECT COUNT(*) FROM provisioned_vms WHERE account_id::text = $1 AND state <> 'deleted'`, accountID,
	).Scan(&held); err != nil {
		f.t.Fatalf("count the account's VMs: %v", err)
	}
	if held != 0 {
		f.t.Fatalf("a refused account holds %d VMs", held)
	}
}

func (f *trialVMFixture) deleteVM(id string) {
	f.t.Helper()
	if _, err := f.st.db.ExecContext(f.ctx,
		`UPDATE provisioned_vms SET state = 'deleted', desired = 'absent', deleted_at = NOW() WHERE id = $1`, id,
	); err != nil {
		f.t.Fatalf("delete VM: %v", err)
	}
}

func TestPostgresTrialVMsLeaveTheRestForPayingAccounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, trialsBefore := newTrialVMFixture(t, ctx)
	f.policy.TrialMaxVMs = trialsBefore + 2

	asked := 0
	counted := VMRequest{AllowTrialVM: func() bool { asked++; return true }}
	mustNotAsk := VMRequest{AllowTrialVM: func() bool {
		t.Error("asked about a trial VM for a request that isn't one")
		return false
	}}

	// The source's limit can refuse a trial with room in the pool.
	f.refused(f.account("trial"), VMRequest{AllowTrialVM: func() bool { return false }}, ErrTrialVMRate)

	first, second := f.account("trial"), f.account("trial")
	firstVM := f.mustEnsure(first, counted)
	secondVM := f.mustEnsure(second, counted)
	if asked != 2 {
		t.Fatalf("asked %d times for two new VMs", asked)
	}
	// Coming back for the VM it has isn't a new VM, even with the pool full.
	if again := f.mustEnsure(first, mustNotAsk); again.ID != firstVM.ID {
		t.Fatalf("second ensure = %q, want %q", again.ID, firstVM.ID)
	}

	third := f.account("trial")
	f.refused(third, mustNotAsk, ErrTrialVMCapacity)

	// Paying and operator accounts aren't held back, and don't use the pool.
	f.mustEnsure(f.account("paid"), mustNotAsk)
	f.mustEnsure(f.account("operator"), mustNotAsk)
	f.refused(third, mustNotAsk, ErrTrialVMCapacity)

	// A first deposit moves the account and its VM out of the trials at once.
	wallet, err := f.st.AccountWallet(ctx, first)
	if err != nil {
		t.Fatalf("open wallet: %v", err)
	}
	if credited, err := f.st.CreditDeposit(ctx, DepositCredit{
		TxID: strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""), Address: "T-test-" + first, Index: wallet.DepositIndex,
		AmountMicro: 1_000_000, BlockTime: time.Now(),
	}); err != nil || !credited {
		t.Fatalf("credit deposit = %v, %v", credited, err)
	}
	thirdVM := f.mustEnsure(third, counted)

	// Deleting an account doesn't free the slot before its VM is gone.
	if err := f.st.DeleteAccount(ctx, second); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	fourth := f.account("trial")
	f.refused(fourth, mustNotAsk, ErrTrialVMCapacity)
	if _, err := f.st.ReconcileProvisionedVMs(ctx, f.policy); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	f.refused(fourth, mustNotAsk, ErrTrialVMCapacity)
	f.deleteVM(secondVM.ID)
	f.mustEnsure(fourth, counted)

	// A full pool stays full for a spare, which a paying account still gets.
	spareID, err := newProvisionedVMID()
	if err != nil {
		t.Fatal(err)
	}
	insertSpare := func(id string) {
		t.Helper()
		if _, err := f.st.db.ExecContext(ctx, `INSERT INTO provisioned_vms (id, region) VALUES ($1, $2)`, id, f.region); err != nil {
			t.Fatalf("insert spare: %v", err)
		}
	}
	insertSpare(spareID)
	fifth := f.account("trial")
	f.refused(fifth, mustNotAsk, ErrTrialVMCapacity)
	if vm := f.mustEnsure(f.account("paid"), mustNotAsk); vm.ID != spareID {
		t.Fatalf("paying account got %q, want the spare %q", vm.ID, spareID)
	}

	// A spare a trial claims counts against the pool like any other.
	f.deleteVM(thirdVM.ID)
	spareID, err = newProvisionedVMID()
	if err != nil {
		t.Fatal(err)
	}
	insertSpare(spareID)
	if vm := f.mustEnsure(fifth, counted); vm.ID != spareID {
		t.Fatalf("trial got %q, want the spare %q", vm.ID, spareID)
	}
	f.refused(f.account("trial"), mustNotAsk, ErrTrialVMCapacity)
}

func TestPostgresRemovingDataGoesPastAFullTrialPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, trialsBefore := newTrialVMFixture(t, ctx)
	room := 2
	f.policy.TrialMaxVMs = trialsBefore + room
	for range room {
		f.mustEnsure(f.account("trial"), VMRequest{})
	}

	mustNotAsk := func() bool {
		t.Error("the source's limit was asked about removing data")
		return false
	}
	refuseSource := func() bool { return false }

	// A runtime with nothing stored has no claim on the extra room: a new
	// account can always have one.
	empty := f.account("trial")
	emptyRuntime := f.runtime(empty, false)
	f.refused(empty, VMRequest{CleanupRuntimeID: emptyRuntime, AllowTrialVM: mustNotAsk}, ErrTrialVMCapacity)

	// Nor does naming another account's runtime that has data.
	owner := f.account("trial")
	ownerRuntime := f.runtime(owner, true)
	f.refused(empty, VMRequest{CleanupRuntimeID: ownerRuntime, AllowTrialVM: mustNotAsk}, ErrTrialVMCapacity)

	// Starting a runtime with data has no claim either.
	f.refused(owner, VMRequest{AllowTrialVM: mustNotAsk}, ErrTrialVMCapacity)

	// Removing data goes past the limit, by as much again, whatever the source.
	f.mustEnsure(owner, VMRequest{CleanupRuntimeID: ownerRuntime, AllowTrialVM: refuseSource})
	for range f.policy.TrialMaxVMs - 1 {
		account := f.account("trial")
		f.mustEnsure(account, VMRequest{CleanupRuntimeID: f.runtime(account, true), AllowTrialVM: refuseSource})
	}
	last := f.account("trial")
	f.refused(last, VMRequest{CleanupRuntimeID: f.runtime(last, true), AllowTrialVM: mustNotAsk}, ErrTrialVMCapacity)
}

func TestPostgresClosedTrialsStillLetDataBeRemoved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, trialsBefore := newTrialVMFixture(t, ctx)
	if trialsBefore != 0 {
		t.Skipf("%d trial VMs already exist", trialsBefore)
	}
	f.policy.TrialMaxVMs = 0

	account := f.account("trial")
	runtimeID := f.runtime(account, true)
	f.refused(account, VMRequest{}, ErrTrialVMCapacity)
	f.mustEnsure(account, VMRequest{CleanupRuntimeID: runtimeID})
	// One at a time.
	other := f.account("trial")
	f.refused(other, VMRequest{CleanupRuntimeID: f.runtime(other, true)}, ErrTrialVMCapacity)
}

func TestPostgresTrialVMsStayWithinTheLimitWhenRacing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, trialsBefore := newTrialVMFixture(t, ctx)
	room := 3
	f.policy.TrialMaxVMs = trialsBefore + room

	accounts := make([]string, 12)
	for i := range accounts {
		accounts[i] = f.account("trial")
	}
	var wg sync.WaitGroup
	results := make([]error, len(accounts))
	for i, accountID := range accounts {
		wg.Go(func() {
			_, results[i] = f.st.EnsureAccountVM(ctx, accountID, f.region, f.policy, VMRequest{})
		})
	}
	wg.Wait()
	granted := 0
	for _, err := range results {
		switch {
		case err == nil:
			granted++
		case !errors.Is(err, ErrTrialVMCapacity):
			t.Fatalf("ensure VM: %v", err)
		}
	}
	if granted != room {
		t.Fatalf("%d VMs granted, want %d", granted, room)
	}
}
