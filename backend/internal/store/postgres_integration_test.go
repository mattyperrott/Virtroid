package store

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"virtroid/backend/internal/nodeauth"
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
	nodeID := "ci-node-" + uuid.NewString()
	operatorID := "ci-operator-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_request_nonces WHERE node_id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM hosts WHERE id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_registry_audit WHERE node_id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM approved_nodes WHERE node_id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM operator_registry_audit WHERE operator_id = $1`, operatorID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_operators WHERE id = $1`, operatorID)
	})
	devicePublicKey := integrationNodePublicKey(t)
	if _, err := st.BootstrapAccountWithIdentity(
		ctx, accountID, deviceID, "CI device", devicePublicKey, CreateRuntimeInput{},
	); err != nil {
		t.Fatalf("bootstrap public account: %v", err)
	}
	runtime, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "CI runtime"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if runtime.OperationGeneration != 1 {
		t.Fatalf("initial operation generation = %d, want 1", runtime.OperationGeneration)
	}
	approvedNode, err := st.ApproveNode(ctx, ApproveNodeInput{
		NodeID:       nodeID,
		OperatorID:   operatorID,
		OperatorName: "CI operator",
		PublicKey:    integrationNodePublicKey(t),
		Actor:        "postgres-integration-test",
		Reason:       "lifecycle integration fixture",
	})
	if err != nil {
		t.Fatalf("approve test node: %v", err)
	}
	if _, err := st.UpsertHostHeartbeat(ctx, HostHeartbeat{
		ID:            nodeID,
		Name:          "CI node",
		AdvertiseAddr: "virtnoded",
		RelayPort:     8090,
		DockerSocket:  true,
		Binder:        true,
		PublicKey:     approvedNode.Keys[0].PublicKey,
		BlobStoreKind: "local-disk",
	}); err != nil {
		t.Fatalf("register test node: %v", err)
	}
	runtime, err = st.StartRuntimeOnHost(ctx, accountID, runtime.ID, nodeID)
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	if runtime.OperationGeneration != 2 {
		t.Fatalf("start operation generation = %d, want 2", runtime.OperationGeneration)
	}
	if err := st.UpdateRuntimeObservation(ctx, runtime.ID, RuntimeObservation{
		HostID:              nodeID,
		Status:              "running",
		ConnectionStatus:    "online",
		OperationGeneration: 1,
	}); !errors.Is(err, ErrRuntimeObservationStale) {
		t.Fatalf("stale runtime observation error = %v, want %v", err, ErrRuntimeObservationStale)
	}
	if err := st.UpdateRuntimeObservation(ctx, runtime.ID, RuntimeObservation{
		HostID:              nodeID,
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
		HostID:              nodeID,
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
		HostID:    nodeID,
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
	if _, err := st.GetRuntimeBlobKeyHandoff(ctx, runtime.ID, nodeID); err != nil {
		t.Fatalf("restore blob-key handoff: %v", err)
	}

	if err := st.DeleteAccount(ctx, accountID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if _, err := st.GetRuntimeBlobKeyHandoff(ctx, runtime.ID, nodeID); !errors.Is(err, ErrRuntimeBlobKeyHandoff) {
		t.Fatalf("handoff after account deletion error = %v, want %v", err, ErrRuntimeBlobKeyHandoff)
	}
}

func TestPostgresApprovedNodeRegistryIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	nodeID := "registry-node-" + uuid.NewString()
	operatorID := "registry-operator-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_request_nonces WHERE node_id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM hosts WHERE id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_registry_audit WHERE node_id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM approved_nodes WHERE node_id = $1`, nodeID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM operator_registry_audit WHERE operator_id = $1`, operatorID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_operators WHERE id = $1`, operatorID)
	})

	keyV1 := integrationNodePublicKey(t)
	keyV2 := integrationNodePublicKey(t)
	keyV3 := integrationNodePublicKey(t)
	forgedKey := integrationNodePublicKey(t)
	approved, err := st.ApproveNode(ctx, ApproveNodeInput{
		NodeID:       nodeID,
		OperatorID:   operatorID,
		OperatorName: "Registry integration operator",
		PublicKey:    keyV1,
		Actor:        "postgres-integration-test",
		Reason:       "initial approval",
	})
	if err != nil {
		t.Fatalf("ApproveNode: %v", err)
	}
	if approved.ActiveKeyVersion != 1 || len(approved.Keys) != 1 {
		t.Fatalf("approved node = %+v, want one version-1 key", approved)
	}
	canonicalV1 := approved.Keys[0].PublicKey

	if _, err := st.UpsertHostHeartbeat(ctx, HostHeartbeat{
		ID:            nodeID,
		Name:          "Registry node",
		AdvertiseAddr: "virtnoded",
		RelayPort:     8090,
		DockerSocket:  true,
		Binder:        true,
		PublicKey:     canonicalV1,
		BlobStoreKind: "local-disk",
	}); err != nil {
		t.Fatalf("approved heartbeat: %v", err)
	}
	assertReadyHost(t, ctx, st, nodeID, true)

	// Even a direct forged heartbeat can only alter observation state. It cannot
	// add a trust key, and readiness fails while the observed key is unapproved.
	if _, err := st.UpsertHostHeartbeat(ctx, HostHeartbeat{
		ID:            nodeID,
		Name:          "Forged observation",
		AdvertiseAddr: "attacker",
		RelayPort:     8090,
		DockerSocket:  true,
		Binder:        true,
		PublicKey:     forgedKey,
		BlobStoreKind: "local-disk",
	}); err != nil {
		t.Fatalf("forged observation fixture: %v", err)
	}
	authorized, err := st.AuthorizedNodeKeys(ctx, nodeID)
	if err != nil {
		t.Fatalf("AuthorizedNodeKeys after forged heartbeat: %v", err)
	}
	if len(authorized) != 1 || authorized[0].FingerprintSHA256 != approved.Keys[0].FingerprintSHA256 {
		t.Fatalf("authorized keys changed after forged heartbeat: %+v", authorized)
	}
	assertReadyHost(t, ctx, st, nodeID, false)

	if _, err := st.UpsertHostHeartbeat(ctx, HostHeartbeat{
		ID:            nodeID,
		Name:          "Registry node",
		AdvertiseAddr: "virtnoded",
		RelayPort:     8090,
		DockerSocket:  true,
		Binder:        true,
		PublicKey:     canonicalV1,
		BlobStoreKind: "local-disk",
	}); err != nil {
		t.Fatalf("restore approved heartbeat: %v", err)
	}

	rotated, err := st.RotateNodeKey(ctx, RotateNodeKeyInput{
		NodeID:    nodeID,
		PublicKey: keyV2,
		Overlap:   10 * time.Minute,
		Actor:     "postgres-integration-test",
		Reason:    "first rotation",
	})
	if err != nil {
		t.Fatalf("first RotateNodeKey: %v", err)
	}
	if rotated.ActiveKeyVersion != 2 {
		t.Fatalf("active key version = %d, want 2", rotated.ActiveKeyVersion)
	}
	authorized, err = st.AuthorizedNodeKeys(ctx, nodeID)
	if err != nil || len(authorized) != 2 {
		t.Fatalf("authorized keys during first overlap = %+v, %v; want two", authorized, err)
	}

	rotated, err = st.RotateNodeKey(ctx, RotateNodeKeyInput{
		NodeID:    nodeID,
		PublicKey: keyV3,
		Overlap:   10 * time.Minute,
		Actor:     "postgres-integration-test",
		Reason:    "second rotation",
	})
	if err != nil {
		t.Fatalf("second RotateNodeKey: %v", err)
	}
	authorized, err = st.AuthorizedNodeKeys(ctx, nodeID)
	if err != nil {
		t.Fatalf("AuthorizedNodeKeys after second rotation: %v", err)
	}
	if len(authorized) != 2 || authorized[0].KeyVersion != 3 || authorized[1].KeyVersion != 2 {
		t.Fatalf("authorized key versions = %+v, want only active v3 and overlap v2", authorized)
	}

	if _, err := st.RevokeNode(ctx, RevokeNodeInput{
		NodeID: nodeID,
		Actor:  "postgres-integration-test",
		Reason: "compromise exercise",
	}); err != nil {
		t.Fatalf("RevokeNode: %v", err)
	}
	if _, err := st.AuthorizedNodeKeys(ctx, nodeID); !errors.Is(err, ErrApprovedNodeNotFound) {
		t.Fatalf("authorized keys after revocation error = %v, want %v", err, ErrApprovedNodeNotFound)
	}
	assertReadyHost(t, ctx, st, nodeID, false)

	var auditCount int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_registry_audit WHERE node_id = $1`,
		nodeID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("read registry audit count: %v", err)
	}
	if auditCount != 4 {
		t.Fatalf("registry audit count = %d, want approve + two rotations + revoke", auditCount)
	}
}

func TestPostgresOperatorLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("VIRTROID_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VIRTROID_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer st.Close()
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	operatorID := "lifecycle-operator-" + uuid.NewString()
	nodeIDs := []string{
		"lifecycle-node-" + uuid.NewString(),
		"lifecycle-node-" + uuid.NewString(),
	}
	t.Cleanup(func() {
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_registry_audit WHERE node_id IN ($1, $2)`, nodeIDs[0], nodeIDs[1])
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM approved_nodes WHERE node_id IN ($1, $2)`, nodeIDs[0], nodeIDs[1])
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM operator_registry_audit WHERE operator_id = $1`, operatorID)
		_, _ = st.db.ExecContext(context.Background(), `DELETE FROM node_operators WHERE id = $1`, operatorID)
	})

	operator, err := st.ApproveOperator(ctx, ApproveOperatorInput{
		OperatorID: operatorID,
		Name:       "Lifecycle operator",
		Actor:      "security-admin",
		Reason:     "out-of-band identity verification",
	})
	if err != nil {
		t.Fatalf("ApproveOperator: %v", err)
	}
	if operator.Status != "approved" {
		t.Fatalf("approved operator status = %q", operator.Status)
	}

	publicKeys := []string{integrationNodePublicKey(t), integrationNodePublicKey(t)}
	for index, nodeID := range nodeIDs {
		operatorName := "Lifecycle operator"
		if index == 1 {
			operatorName = "Unaudited rename attempt"
		}
		if _, err := st.ApproveNode(ctx, ApproveNodeInput{
			NodeID:       nodeID,
			OperatorID:   operatorID,
			OperatorName: operatorName,
			PublicKey:    publicKeys[index],
			Actor:        "security-admin",
			Reason:       "operator lifecycle fixture",
		}); err != nil {
			t.Fatalf("ApproveNode(%s): %v", nodeID, err)
		}
		if _, err := st.AuthorizedNodeKeys(ctx, nodeID); err != nil {
			t.Fatalf("AuthorizedNodeKeys(%s) before revocation: %v", nodeID, err)
		}
	}
	operator, err = st.NodeOperator(ctx, operatorID)
	if err != nil {
		t.Fatalf("NodeOperator after node approvals: %v", err)
	}
	if operator.Name != "Lifecycle operator" {
		t.Fatalf("node approval changed operator name to %q", operator.Name)
	}

	operator, err = st.RevokeOperator(ctx, RevokeOperatorInput{
		OperatorID: operatorID,
		Actor:      "incident-commander",
		Reason:     "operator credential compromise",
	})
	if err != nil {
		t.Fatalf("RevokeOperator: %v", err)
	}
	if operator.Status != "revoked" || operator.RevokedAt == nil {
		t.Fatalf("revoked operator = %+v", operator)
	}
	for _, nodeID := range nodeIDs {
		if _, err := st.AuthorizedNodeKeys(ctx, nodeID); !errors.Is(err, ErrApprovedNodeNotFound) {
			t.Fatalf("AuthorizedNodeKeys(%s) after operator revocation error = %v, want %v", nodeID, err, ErrApprovedNodeNotFound)
		}
	}

	if _, err := st.ApproveNode(ctx, ApproveNodeInput{
		NodeID:       nodeIDs[0],
		OperatorID:   operatorID,
		OperatorName: "Lifecycle operator",
		PublicKey:    publicKeys[0],
		Actor:        "deploy-bootstrap",
		Reason:       "must not reactivate operator",
	}); !errors.Is(err, ErrNodeOperatorRevoked) {
		t.Fatalf("ApproveNode for revoked operator error = %v, want %v", err, ErrNodeOperatorRevoked)
	}

	operator, err = st.ApproveOperator(ctx, ApproveOperatorInput{
		OperatorID: operatorID,
		Name:       "Lifecycle operator restored",
		Actor:      "security-admin",
		Reason:     "new credentials independently verified",
	})
	if err != nil {
		t.Fatalf("ApproveOperator reactivate: %v", err)
	}
	if operator.Status != "approved" || operator.RevokedAt != nil {
		t.Fatalf("reactivated operator = %+v", operator)
	}
	for _, nodeID := range nodeIDs {
		if _, err := st.AuthorizedNodeKeys(ctx, nodeID); err != nil {
			t.Fatalf("AuthorizedNodeKeys(%s) after reactivation: %v", nodeID, err)
		}
	}

	rows, err := st.db.QueryContext(ctx,
		`SELECT action, actor, reason
		 FROM operator_registry_audit
		 WHERE operator_id = $1
		 ORDER BY id`,
		operatorID,
	)
	if err != nil {
		t.Fatalf("read operator audit: %v", err)
	}
	defer rows.Close()
	type auditEntry struct {
		action string
		actor  string
		reason string
	}
	var audit []auditEntry
	for rows.Next() {
		var entry auditEntry
		if err := rows.Scan(&entry.action, &entry.actor, &entry.reason); err != nil {
			t.Fatalf("scan operator audit: %v", err)
		}
		audit = append(audit, entry)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate operator audit: %v", err)
	}
	wantAudit := []auditEntry{
		{action: "approve", actor: "security-admin", reason: "out-of-band identity verification"},
		{action: "revoke", actor: "incident-commander", reason: "operator credential compromise"},
		{action: "reactivate", actor: "security-admin", reason: "new credentials independently verified"},
	}
	if len(audit) != len(wantAudit) {
		t.Fatalf("operator audit = %+v, want %+v", audit, wantAudit)
	}
	for index := range wantAudit {
		if audit[index] != wantAudit[index] {
			t.Fatalf("operator audit[%d] = %+v, want %+v", index, audit[index], wantAudit[index])
		}
	}
}

func integrationNodePublicKey(t *testing.T) string {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	publicKey, err := nodeauth.PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("encode node public key: %v", err)
	}
	return publicKey
}

func assertReadyHost(t *testing.T, ctx context.Context, st *Store, nodeID string, wantReady bool) {
	t.Helper()
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin readiness transaction: %v", err)
	}
	defer tx.Rollback()
	_, err = requireReadyHostTX(ctx, tx, nodeID)
	if wantReady && err != nil {
		t.Fatalf("approved node is not ready: %v", err)
	}
	if !wantReady && !errors.Is(err, ErrNoReadyHost) {
		t.Fatalf("unapproved node readiness error = %v, want %v", err, ErrNoReadyHost)
	}
}
