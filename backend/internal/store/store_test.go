package store

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"virtroid/backend/internal/nodeauth"
)

const (
	bootstrapInvitationReservationSQL = `SELECT id\s+FROM bootstrap_invitations\s+WHERE token_sha256 = \$1\s+AND consumed_at IS NULL\s+AND expires_at > NOW\(\)\s+FOR UPDATE`
	bootstrapInvitationConsumptionSQL = `UPDATE bootstrap_invitations\s+SET consumed_at = NOW\(\),\s+consumed_account_id = \$1\s+WHERE id = \$2\s+AND consumed_at IS NULL\s+AND expires_at > NOW\(\)`
)

func TestEnsureSchemaSerializesAndRecordsVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(schemaMigrationLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS schema_migrations`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(0))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS accounts`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO schema_migrations`).
		WithArgs(currentSchemaVersion, schemaVersionLabel).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := st.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnsureSchemaRejectsNewerDatabaseVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs(schemaMigrationLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`CREATE TABLE IF NOT EXISTS schema_migrations`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(version\), 0\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(currentSchemaVersion + 1))
	mock.ExpectRollback()

	err = st.EnsureSchema(context.Background())
	if err == nil || !strings.Contains(err.Error(), "newer than binary") {
		t.Fatalf("EnsureSchema error = %v, want newer-schema rejection", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestNodeRegistrySchemaIsSeparateFromHeartbeatHosts(t *testing.T) {
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS node_operators",
		"CREATE TABLE IF NOT EXISTS approved_nodes",
		"CREATE TABLE IF NOT EXISTS approved_node_keys",
		"CREATE TABLE IF NOT EXISTS node_registry_audit",
		"CREATE TABLE IF NOT EXISTS operator_registry_audit",
		"idx_approved_node_keys_one_active",
		"idx_approved_node_keys_one_overlap",
		"idx_operator_registry_audit_operator_created_at",
	} {
		if !strings.Contains(schemaSQL, required) {
			t.Fatalf("schema is missing node-registry control %q", required)
		}
	}
	if strings.Contains(schemaSQL, "INSERT INTO approved_nodes") || strings.Contains(schemaSQL, "SELECT id, public_key FROM hosts") {
		t.Fatal("schema auto-imports heartbeat-observed hosts into the trust registry")
	}
}

func TestBootstrapSchemaUsesHashedSingleUseInvitations(t *testing.T) {
	if !strings.Contains(schemaSQL, "DROP TABLE IF EXISTS enrollment_invitations") {
		t.Fatal("schema does not remove the obsolete operator invitation table")
	}
	if strings.Contains(schemaSQL, "CREATE TABLE IF NOT EXISTS enrollment_invitations") {
		t.Fatal("schema recreates the obsolete operator invitation table")
	}
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS bootstrap_invitations",
		"token_sha256 BYTEA NOT NULL UNIQUE",
		"consumed_at TIMESTAMPTZ",
		"consumed_account_id UUID",
		"consumed_at IS NOT NULL AND consumed_account_id IS NOT NULL",
		"WHERE consumed_at IS NULL",
	} {
		if !strings.Contains(schemaSQL, required) {
			t.Fatalf("schema is missing bootstrap-invitation control %q", required)
		}
	}
	if strings.Contains(schemaSQL, "invite_token") {
		t.Fatal("schema appears to persist plaintext bootstrap invitation tokens")
	}
	if strings.Contains(schemaSQL, "consumed_account_id UUID REFERENCES") {
		t.Fatal("bootstrap invitation audit history would block or be rewritten by later account deletion")
	}
}

func TestAccountRecoverySchemaUsesPasswordEnvelopeAndSingleUseChallenge(t *testing.T) {
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS account_recovery_credentials",
		"PBKDF2_HMAC_SHA256_V2",
		"AES_GCM_256_V1",
		"recovery_public_key TEXT NOT NULL",
		"blob_key_verifier TEXT NOT NULL",
		"CREATE TABLE IF NOT EXISTS account_recovery_challenges",
		"consumed_at TIMESTAMPTZ",
		"WHERE consumed_at IS NULL",
	} {
		if !strings.Contains(schemaSQL, required) {
			t.Fatalf("schema is missing account-recovery control %q", required)
		}
	}
	if strings.Contains(schemaSQL, "recovery_private_key TEXT") || strings.Contains(schemaSQL, "account_master_key TEXT") {
		t.Fatal("schema stores plaintext account recovery secrets")
	}
}

func TestNodeRegistryRejectsInvalidInputsBeforeDatabaseMutation(t *testing.T) {
	st := &Store{}
	if _, err := st.ApproveOperator(context.Background(), ApproveOperatorInput{
		OperatorID: "operator-1",
		Actor:      "tester",
	}); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("ApproveOperator missing-reason error = %v", err)
	}
	if _, err := st.RevokeOperator(context.Background(), RevokeOperatorInput{
		OperatorID: "bad operator id",
		Actor:      "tester",
		Reason:     "compromised",
	}); err == nil || !strings.Contains(err.Error(), "unsupported character") {
		t.Fatalf("RevokeOperator invalid-ID error = %v", err)
	}
	if _, err := st.ApproveNode(context.Background(), ApproveNodeInput{
		NodeID:       "bad node id",
		OperatorID:   "operator-1",
		OperatorName: "Operator",
		PublicKey:    "not-a-key",
		Actor:        "tester",
	}); err == nil {
		t.Fatal("ApproveNode accepted an invalid node id")
	}
	if _, err := st.RotateNodeKey(context.Background(), RotateNodeKeyInput{
		NodeID:    "node-1",
		PublicKey: "not-a-key",
		Overlap:   MaxNodeKeyRotationOverlap + time.Second,
		Actor:     "tester",
	}); !errors.Is(err, ErrNodeKeyRotationOverlap) {
		t.Fatalf("RotateNodeKey overlap error = %v, want %v", err, ErrNodeKeyRotationOverlap)
	}
	if _, err := st.RevokeNode(context.Background(), RevokeNodeInput{
		NodeID: "node-1",
		Actor:  "tester",
	}); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("RevokeNode missing-reason error = %v", err)
	}
}

func TestApproveNodeWritesExplicitRegistryAndAuditRecords(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	nodeID := "node-1"
	operatorID := "operator-1"
	publicKey, fingerprint, err := nodeauth.NormalizePublicKey(integrationNodePublicKey(t))
	if err != nil {
		t.Fatalf("NormalizePublicKey: %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO node_operators`).
		WithArgs(operatorID, "Test operator").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT status FROM node_operators`).
		WithArgs(operatorID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("approved"))
	mock.ExpectExec(`INSERT INTO operator_registry_audit`).
		WithArgs(operatorID, "approve", "test-actor", "out-of-band verification").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT operator_id, status, active_key_version`).
		WithArgs(nodeID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO approved_nodes`).
		WithArgs(nodeID, operatorID, int64(1)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO approved_node_keys`).
		WithArgs(nodeID, int64(1), publicKey, fingerprint).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO node_registry_audit`).
		WithArgs(nodeID, operatorID, "approve", "test-actor", "out-of-band verification", int64(1), fingerprint).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT n.node_id, n.operator_id`).
		WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{
			"node_id", "operator_id", "operator_name", "operator_status", "status",
			"active_key_version", "approved_at", "revoked_at", "created_at", "updated_at",
		}).AddRow(nodeID, operatorID, "Test operator", "approved", "approved", int64(1), now, nil, now, now))
	mock.ExpectQuery(`SELECT node_id, key_version, public_key`).
		WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{
			"node_id", "key_version", "public_key", "fingerprint_sha256", "state",
			"valid_from", "valid_until", "retired_at", "created_at",
		}).AddRow(nodeID, int64(1), publicKey, fingerprint, "active", now, nil, nil, now))

	node, err := st.ApproveNode(context.Background(), ApproveNodeInput{
		NodeID:       nodeID,
		OperatorID:   operatorID,
		OperatorName: "Test operator",
		PublicKey:    publicKey,
		Actor:        "test-actor",
		Reason:       "out-of-band verification",
	})
	if err != nil {
		t.Fatalf("ApproveNode: %v", err)
	}
	if node.ActiveKeyVersion != 1 || len(node.Keys) != 1 || node.Keys[0].FingerprintSHA256 != fingerprint {
		t.Fatalf("approved node = %+v, want version 1 and expected fingerprint", node)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestApproveOperatorCreatesAuditedOperator(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	operatorID := "operator-1"
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO node_operators`).
		WithArgs(operatorID, "Test operator").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT status FROM node_operators`).
		WithArgs(operatorID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("approved"))
	mock.ExpectExec(`UPDATE node_operators SET name`).
		WithArgs(operatorID, "Test operator", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO operator_registry_audit`).
		WithArgs(operatorID, "approve", "security-admin", "verified identity").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id, name, status, approved_at`).
		WithArgs(operatorID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "status", "approved_at", "revoked_at", "created_at", "updated_at",
		}).AddRow(operatorID, "Test operator", "approved", now, nil, now, now))

	operator, err := st.ApproveOperator(context.Background(), ApproveOperatorInput{
		OperatorID: operatorID,
		Name:       "Test operator",
		Actor:      "security-admin",
		Reason:     "verified identity",
	})
	if err != nil {
		t.Fatalf("ApproveOperator: %v", err)
	}
	if operator.ID != operatorID || operator.Status != "approved" || operator.RevokedAt != nil {
		t.Fatalf("approved operator = %+v", operator)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestApproveOperatorReactivatesWithAudit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	operatorID := "operator-1"
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO node_operators`).
		WithArgs(operatorID, "Restored operator").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT status FROM node_operators`).
		WithArgs(operatorID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("revoked"))
	mock.ExpectExec(`UPDATE node_operators[\s\S]*status = 'approved'`).
		WithArgs(operatorID, "Restored operator", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO operator_registry_audit`).
		WithArgs(operatorID, "reactivate", "security-admin", "incident resolved").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id, name, status, approved_at`).
		WithArgs(operatorID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "status", "approved_at", "revoked_at", "created_at", "updated_at",
		}).AddRow(operatorID, "Restored operator", "approved", now, nil, now, now))

	operator, err := st.ApproveOperator(context.Background(), ApproveOperatorInput{
		OperatorID: operatorID,
		Name:       "Restored operator",
		Actor:      "security-admin",
		Reason:     "incident resolved",
	})
	if err != nil {
		t.Fatalf("ApproveOperator reactivate: %v", err)
	}
	if operator.Status != "approved" || operator.RevokedAt != nil {
		t.Fatalf("reactivated operator = %+v", operator)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRevokeOperatorWritesAudit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	operatorID := "operator-1"
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status FROM node_operators`).
		WithArgs(operatorID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("approved"))
	mock.ExpectExec(`UPDATE node_operators[\s\S]*status = 'revoked'`).
		WithArgs(operatorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO operator_registry_audit`).
		WithArgs(operatorID, "revoke", "security-admin", "operator credential compromise").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id, name, status, approved_at`).
		WithArgs(operatorID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "status", "approved_at", "revoked_at", "created_at", "updated_at",
		}).AddRow(operatorID, "Test operator", "revoked", now, now, now, now))

	operator, err := st.RevokeOperator(context.Background(), RevokeOperatorInput{
		OperatorID: operatorID,
		Actor:      "security-admin",
		Reason:     "operator credential compromise",
	})
	if err != nil {
		t.Fatalf("RevokeOperator: %v", err)
	}
	if operator.Status != "revoked" || operator.RevokedAt == nil {
		t.Fatalf("revoked operator = %+v", operator)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAuthorizedNodeKeysReturnsOnlyRegistryQueryResults(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	nodeID := "node-1"
	activeKey, activeFingerprint, err := nodeauth.NormalizePublicKey(integrationNodePublicKey(t))
	if err != nil {
		t.Fatalf("NormalizePublicKey(active): %v", err)
	}
	overlapKey, overlapFingerprint, err := nodeauth.NormalizePublicKey(integrationNodePublicKey(t))
	if err != nil {
		t.Fatalf("NormalizePublicKey(overlap): %v", err)
	}
	mock.ExpectQuery(`FROM approved_nodes n[\s\S]*JOIN node_operators[\s\S]*JOIN approved_node_keys`).
		WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{
			"node_id", "key_version", "public_key", "fingerprint_sha256", "state",
			"valid_from", "valid_until", "retired_at", "created_at",
		}).
			AddRow(nodeID, int64(2), activeKey, activeFingerprint, "active", now, nil, nil, now).
			AddRow(nodeID, int64(1), overlapKey, overlapFingerprint, "overlap", now.Add(-time.Hour), now.Add(time.Hour), nil, now.Add(-time.Hour)))

	keys, err := st.AuthorizedNodeKeys(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("AuthorizedNodeKeys: %v", err)
	}
	if len(keys) != 2 || keys[0].KeyVersion != 2 || keys[1].KeyVersion != 1 {
		t.Fatalf("authorized keys = %+v, want active v2 and overlap v1", keys)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAppendRuntimeLogBoundsFieldsAndPrunesOldRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "11111111-1111-1111-1111-111111111111"
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO runtime_logs`).
		WithArgs(
			runtimeID,
			strings.Repeat("s", runtimeLogSourceRunes),
			strings.Repeat("l", runtimeLogLevelRunes),
			strings.Repeat("m", runtimeLogMessageRunes),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`DELETE FROM runtime_logs`).
		WithArgs(runtimeID, runtimeLogRetentionRows).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()

	if err := st.AppendRuntimeLog(
		context.Background(),
		runtimeID,
		strings.Repeat("s", runtimeLogSourceRunes+10),
		strings.Repeat("l", runtimeLogLevelRunes+10),
		strings.Repeat("m", runtimeLogMessageRunes+10),
	); err != nil {
		t.Fatalf("AppendRuntimeLog: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestPruneRuntimeLogsUsesConfiguredRetention(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	mock.ExpectExec(`DELETE FROM runtime_logs WHERE created_at < \$1`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 7))

	count, err := st.PruneRuntimeLogs(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("PruneRuntimeLogs: %v", err)
	}
	if count != 7 {
		t.Fatalf("PruneRuntimeLogs count = %d, want 7", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestBootstrapAccountDoesNotCreateRuntime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO accounts").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).
			AddRow("11111111-1111-1111-1111-111111111111", now))
	mock.ExpectExec("INSERT INTO account_entitlements").
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("INSERT INTO devices").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "Pixel", "public-key").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "name", "public_key", "blob_key_verifier", "created_at", "last_seen_at",
		}).AddRow(
			"22222222-2222-2222-2222-222222222222",
			"11111111-1111-1111-1111-111111111111",
			"Pixel",
			"public-key",
			nil,
			now,
			nil,
		))
	mock.ExpectCommit()

	result, err := st.BootstrapAccount(context.Background(), "Pixel", "public-key", CreateRuntimeInput{})
	if err != nil {
		t.Fatalf("BootstrapAccount returned error: %v", err)
	}
	if result.Runtime != nil {
		t.Fatalf("bootstrap runtime = %#v; want nil until the user creates one", result.Runtime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetAccountStorageAppliesLatestRenterdPreflight(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectQuery("SELECT account_id, provider, funding_model").
		WithArgs(accountID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT created_at FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(now))
	mock.ExpectQuery("SELECT storage_preflight_kind, storage_preflight_status").
		WillReturnRows(sqlmock.NewRows([]string{
			"storage_preflight_kind",
			"storage_preflight_status",
			"storage_preflight_json",
			"storage_preflight_at",
		}).AddRow("sia-renterd", "contracts_required", `{"store":"sia-renterd","wallet_address":"addr:fund","nested":{"fundingAddress":"addr:nested"}}`, now))

	storage, err := st.GetAccountStorage(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccountStorage returned error: %v", err)
	}
	if storage.Provider != "sia-renterd" || storage.FundingModel != "operator-pooled" {
		t.Fatalf("storage provider/model = %s/%s, want sia-renterd/operator-pooled", storage.Provider, storage.FundingModel)
	}
	if storage.Status != "contracts_required" {
		t.Fatalf("storage.Status = %q, want contracts_required", storage.Status)
	}
	if storage.FundingAddress != nil || storage.WalletAddress != nil {
		t.Fatalf("node preflight populated account payment fields: funding=%v wallet=%v", storage.FundingAddress, storage.WalletAddress)
	}
	if storage.LastPreflightJSON == nil || !strings.Contains(*storage.LastPreflightJSON, `"store":"sia-renterd"`) {
		t.Fatalf("storage.LastPreflightJSON = %v, want sanitized operational preflight data", storage.LastPreflightJSON)
	}
	if strings.Contains(*storage.LastPreflightJSON, "addr:fund") || strings.Contains(*storage.LastPreflightJSON, "addr:nested") {
		t.Fatalf("storage.LastPreflightJSON exposed a node-supplied payment address: %s", *storage.LastPreflightJSON)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapAccountWithIdentityUsesSuppliedIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).
			AddRow(accountID, now))
	mock.ExpectExec("INSERT INTO account_entitlements").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("INSERT INTO devices").
		WithArgs(deviceID, accountID, "Pixel", "public-key").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "name", "public_key", "blob_key_verifier", "created_at", "last_seen_at",
		}).AddRow(
			deviceID,
			accountID,
			"Pixel",
			"public-key",
			nil,
			now,
			nil,
		))
	mock.ExpectCommit()

	result, err := st.BootstrapAccountWithIdentity(
		context.Background(),
		accountID,
		deviceID,
		"Pixel",
		"public-key",
		CreateRuntimeInput{},
	)
	if err != nil {
		t.Fatalf("BootstrapAccountWithIdentity returned error: %v", err)
	}
	if result.Account.ID != accountID {
		t.Fatalf("account id = %q, want %q", result.Account.ID, accountID)
	}
	if result.Device.ID != deviceID {
		t.Fatalf("device id = %q, want %q", result.Device.ID, deviceID)
	}
	if result.Runtime != nil {
		t.Fatalf("bootstrap runtime = %#v; want nil until the user creates one", result.Runtime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapAccountWithInvitationConsumesItInAccountTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	invitationID := "33333333-3333-3333-3333-333333333333"
	tokenDigest := sha256.Sum256([]byte("single-use-bootstrap-token"))

	mock.ExpectBegin()
	mock.ExpectQuery(bootstrapInvitationReservationSQL).
		WithArgs(tokenDigest[:]).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(invitationID))
	mock.ExpectQuery("INSERT INTO accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(accountID, now))
	mock.ExpectExec("INSERT INTO account_entitlements").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("INSERT INTO devices").
		WithArgs(deviceID, accountID, "Pixel", "public-key").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "name", "public_key", "blob_key_verifier", "created_at", "last_seen_at",
		}).AddRow(deviceID, accountID, "Pixel", "public-key", nil, now, nil))
	mock.ExpectExec(bootstrapInvitationConsumptionSQL).
		WithArgs(accountID, invitationID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := st.BootstrapAccountWithInvitation(
		context.Background(),
		accountID,
		deviceID,
		"Pixel",
		"public-key",
		tokenDigest[:],
		CreateRuntimeInput{},
	)
	if err != nil {
		t.Fatalf("BootstrapAccountWithInvitation returned error: %v", err)
	}
	if result.Account.ID != accountID || result.Device.ID != deviceID {
		t.Fatalf("unexpected bootstrap result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapAccountWithAutomaticInvitationIssuesAndConsumesInAccountTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO bootstrap_invitations").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "device:"+deviceID, automaticBootstrapActor, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("INSERT INTO accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(accountID, now))
	mock.ExpectExec("INSERT INTO account_entitlements").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("INSERT INTO devices").
		WithArgs(deviceID, accountID, "Pixel", "public-key").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "name", "public_key", "blob_key_verifier", "created_at", "last_seen_at",
		}).AddRow(deviceID, accountID, "Pixel", "public-key", nil, now, nil))
	mock.ExpectExec(bootstrapInvitationConsumptionSQL).
		WithArgs(accountID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := st.BootstrapAccountWithAutomaticInvitation(
		context.Background(),
		accountID,
		deviceID,
		"Pixel",
		"public-key",
		CreateRuntimeInput{},
	)
	if err != nil {
		t.Fatalf("BootstrapAccountWithAutomaticInvitation returned error: %v", err)
	}
	if result.Account.ID != accountID || result.Device.ID != deviceID {
		t.Fatalf("unexpected bootstrap result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapAccountWithInvitationRejectsUnavailableTokenAndRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	tokenDigest := sha256.Sum256([]byte("expired-or-consumed-token"))
	mock.ExpectBegin()
	mock.ExpectQuery(bootstrapInvitationReservationSQL).
		WithArgs(tokenDigest[:]).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = st.BootstrapAccountWithInvitation(
		context.Background(),
		accountID,
		deviceID,
		"Pixel",
		"public-key",
		tokenDigest[:],
		CreateRuntimeInput{},
	)
	if !errors.Is(err, ErrBootstrapInviteInvalid) {
		t.Fatalf("BootstrapAccountWithInvitation error = %v, want %v", err, ErrBootstrapInviteInvalid)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapInvitationConsumptionRollsBackWhenDeviceCreationFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	invitationID := "33333333-3333-3333-3333-333333333333"
	tokenDigest := sha256.Sum256([]byte("rollback-safe-token"))

	mock.ExpectBegin()
	mock.ExpectQuery(bootstrapInvitationReservationSQL).
		WithArgs(tokenDigest[:]).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(invitationID))
	mock.ExpectQuery("INSERT INTO accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(accountID, now))
	mock.ExpectExec("INSERT INTO account_entitlements").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("INSERT INTO devices").
		WithArgs(deviceID, accountID, "Pixel", "public-key").
		WillReturnError(errors.New("device insert failed"))
	mock.ExpectRollback()

	_, err = st.BootstrapAccountWithInvitation(
		context.Background(),
		accountID,
		deviceID,
		"Pixel",
		"public-key",
		tokenDigest[:],
		CreateRuntimeInput{},
	)
	if err == nil || !strings.Contains(err.Error(), "device insert failed") {
		t.Fatalf("BootstrapAccountWithInvitation error = %v, want device failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateBootstrapInvitationStoresDigestAndAuditMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	tokenDigest := sha256.Sum256([]byte("plaintext-is-never-stored"))
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	createdAt := time.Now().UTC()
	mock.ExpectQuery("INSERT INTO bootstrap_invitations").
		WithArgs(sqlmock.AnyArg(), tokenDigest[:], "Pixel 9", "security-admin", expiresAt).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(createdAt))

	invitation, err := st.CreateBootstrapInvitation(
		context.Background(),
		tokenDigest[:],
		" Pixel 9 ",
		" security-admin ",
		expiresAt,
	)
	if err != nil {
		t.Fatalf("CreateBootstrapInvitation returned error: %v", err)
	}
	if invitation.ID == "" || invitation.Label != "Pixel 9" || invitation.CreatedBy != "security-admin" {
		t.Fatalf("unexpected invitation: %+v", invitation)
	}
	if !invitation.CreatedAt.Equal(createdAt) || !invitation.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected invitation timestamps: %+v", invitation)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDeleteAccountQueuesAssignedRuntimeCleanup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accountID))
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "host_id"}).AddRow(runtimeID, "host-1"))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "system", "warn", "Account deletion requested; runtime cleanup queued.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM device_request_nonces").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM devices").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM account_storage").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM account_entitlements").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE accounts").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT deleted_at FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"deleted_at"}).AddRow(now))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectCommit()

	if err := st.DeleteAccount(context.Background(), accountID); err != nil {
		t.Fatalf("DeleteAccount returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDeleteAccountQueuesStoppedSnapshotCleanupToBlobOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	blobHostID := "snapshot-owner-1"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accountID))
	mock.ExpectQuery("host_id = COALESCE\\(host_id, blob_host_id\\)").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "host_id"}).AddRow(runtimeID, blobHostID))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "system", "warn", "Account deletion requested; runtime cleanup queued.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("DELETE FROM device_request_nonces").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM devices").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM account_storage").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM account_entitlements").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE accounts").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT deleted_at FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"deleted_at"}).AddRow(now))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectCommit()

	if err := st.DeleteAccount(context.Background(), accountID); err != nil {
		t.Fatalf("DeleteAccount returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDeletedObservationAcknowledgesCleanupBeforeHardDeletingAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	cleanupHostID := "snapshot-owner-1"

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(runtimeID, cleanupHostID, nil, int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(accountID))
	mock.ExpectQuery("SELECT deleted_at FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"deleted_at"}).AddRow(now))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("DELETE FROM accounts").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = st.UpdateRuntimeObservation(context.Background(), runtimeID, RuntimeObservation{
		HostID:              cleanupHostID,
		OperationGeneration: 1,
		Deleted:             true,
	})
	if err != nil {
		t.Fatalf("UpdateRuntimeObservation returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDeleteAccountHardDeletesAfterUnassignedRuntimeCleanup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accountID))
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "host_id"}))
	mock.ExpectExec("DELETE FROM device_request_nonces").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM devices").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM account_storage").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM account_entitlements").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE accounts").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT deleted_at FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"deleted_at"}).AddRow(now))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("DELETE FROM accounts").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := st.DeleteAccount(context.Background(), accountID); err != nil {
		t.Fatalf("DeleteAccount returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRevokeDeviceMarksDeviceAndStopsItsLiveRuntime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accountID))
	mock.ExpectQuery("SELECT id").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(deviceID))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("UPDATE devices d").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "name", "public_key", "blob_key_verifier", "created_at", "last_seen_at", "revoked_at",
		}).AddRow(
			deviceID,
			accountID,
			"Pixel",
			"public-key",
			nil,
			now,
			now,
			now,
		))
	mock.ExpectExec("DELETE FROM device_request_nonces").
		WithArgs(accountID, deviceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("revoked_sessions AS").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(runtimeID))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "system", "warn", "Runtime stop queued because a linked device was revoked.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	device, stoppedRuntimeIDs, err := st.RevokeDevice(context.Background(), accountID, deviceID)
	if err != nil {
		t.Fatalf("RevokeDevice returned error: %v", err)
	}
	if device.ID != deviceID || device.RevokedAt == nil {
		t.Fatalf("revoked device = %+v, want id %s with revoked_at", device, deviceID)
	}
	if len(stoppedRuntimeIDs) != 1 || stoppedRuntimeIDs[0] != runtimeID {
		t.Fatalf("stopped runtime ids = %#v, want [%s]", stoppedRuntimeIDs, runtimeID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRevokeDeviceRejectsLastActiveDevice(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accountID))
	mock.ExpectQuery("SELECT id").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(deviceID))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	_, _, err = st.RevokeDevice(context.Background(), accountID, deviceID)
	if !errors.Is(err, ErrLastActiveDevice) {
		t.Fatalf("RevokeDevice error = %v, want %v", err, ErrLastActiveDevice)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRegisterDeviceBlobKeyVerifierAllowsInitialSetupWithoutCurrentKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	verifier := blobVerifierForTest(0x11)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(nil))
	mock.ExpectExec("UPDATE devices").
		WithArgs(accountID, deviceID, verifier).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := st.RegisterDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, verifier); err != nil {
		t.Fatalf("RegisterDeviceBlobKeyVerifier initial setup returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRegisterDeviceBlobKeyVerifierRejectsOverwrite(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	oldVerifier := blobVerifierForTest(0x22)
	newVerifier := blobVerifierForTest(0x33)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(oldVerifier))
	mock.ExpectRollback()

	err = st.RegisterDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, newVerifier)
	if !errors.Is(err, ErrIdentityAlreadySet) {
		t.Fatalf("RegisterDeviceBlobKeyVerifier error = %v, want %v", err, ErrIdentityAlreadySet)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestValidateAccountRecoveryCredential(t *testing.T) {
	credential := accountRecoveryCredentialForTest(t)
	validated, err := ValidateAccountRecoveryCredential(credential)
	if err != nil {
		t.Fatalf("ValidateAccountRecoveryCredential returned error: %v", err)
	}
	if validated.AccountID != credential.AccountID || validated.BlobKeyVerifier != credential.BlobKeyVerifier {
		t.Fatalf("validated credential changed identity fields: %#v", validated)
	}

	for name, mutate := range map[string]func(*AccountRecoveryCredential){
		"weak kdf": func(value *AccountRecoveryCredential) { value.KDFIterations = AccountRecoveryMinIterations - 1 },
		"bad iv":   func(value *AccountRecoveryCredential) { value.EnvelopeIV = "invalid" },
		"bad curve": func(value *AccountRecoveryCredential) {
			value.RecoveryPublicKey = base64.StdEncoding.EncodeToString([]byte("not a public key"))
		},
		"bad verifier": func(value *AccountRecoveryCredential) { value.BlobKeyVerifier = "invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := credential
			mutate(&invalid)
			if _, err := ValidateAccountRecoveryCredential(invalid); err == nil {
				t.Fatal("invalid account recovery credential was accepted")
			}
		})
	}
}

func TestRegisterAccountRecoveryCredentialAtomicallyInitializesBlobIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	credential := accountRecoveryCredentialForTest(t)
	deviceID := "22222222-2222-2222-2222-222222222222"
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(credential.AccountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(nil))
	mock.ExpectExec("UPDATE devices").
		WithArgs(credential.AccountID, deviceID, credential.BlobKeyVerifier).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO account_recovery_credentials").
		WithArgs(
			credential.AccountID,
			credential.Version,
			credential.KDFAlgorithm,
			credential.KDFSalt,
			credential.KDFIterations,
			credential.EnvelopeAlgorithm,
			credential.EnvelopeIV,
			credential.EnvelopeCiphertext,
			credential.RecoveryPublicKey,
			credential.BlobKeyVerifier,
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"account_id", "version", "kdf_algorithm", "kdf_salt", "kdf_iterations",
			"envelope_algorithm", "envelope_iv", "envelope_ciphertext",
			"recovery_public_key", "blob_key_verifier", "created_at", "updated_at",
		}).AddRow(
			credential.AccountID, credential.Version, credential.KDFAlgorithm, credential.KDFSalt,
			credential.KDFIterations, credential.EnvelopeAlgorithm, credential.EnvelopeIV,
			credential.EnvelopeCiphertext, credential.RecoveryPublicKey, credential.BlobKeyVerifier,
			now, now,
		))
	mock.ExpectCommit()

	registered, err := st.RegisterAccountRecoveryCredential(
		context.Background(), credential.AccountID, deviceID, credential,
	)
	if err != nil {
		t.Fatalf("RegisterAccountRecoveryCredential returned error: %v", err)
	}
	if registered.BlobKeyVerifier != credential.BlobKeyVerifier {
		t.Fatalf("registered verifier = %q, want %q", registered.BlobKeyVerifier, credential.BlobKeyVerifier)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestChangeDeviceBlobKeyVerifierRequiresCurrentVerifier(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	oldVerifier := blobVerifierForTest(0x22)
	newVerifier := blobVerifierForTest(0x33)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(oldVerifier))
	mock.ExpectRollback()

	err = st.ChangeDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, newVerifier, "")
	if !errors.Is(err, ErrIdentityKeyRequired) {
		t.Fatalf("ChangeDeviceBlobKeyVerifier error = %v, want %v", err, ErrIdentityKeyRequired)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestChangeDeviceBlobKeyVerifierRejectsUnconfiguredIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	newVerifier := blobVerifierForTest(0x33)
	currentVerifier := blobVerifierForTest(0x22)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(nil))
	mock.ExpectRollback()

	err = st.ChangeDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, newVerifier, currentVerifier)
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("ChangeDeviceBlobKeyVerifier error = %v, want %v", err, ErrIdentityNotFound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestChangeDeviceBlobKeyVerifierRejectsWrongCurrentVerifier(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	oldVerifier := blobVerifierForTest(0x44)
	newVerifier := blobVerifierForTest(0x55)
	wrongVerifier := blobVerifierForTest(0x99)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(oldVerifier))
	mock.ExpectRollback()

	err = st.ChangeDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, newVerifier, wrongVerifier)
	if !errors.Is(err, ErrIdentityAuthFailed) {
		t.Fatalf("ChangeDeviceBlobKeyVerifier error = %v, want %v", err, ErrIdentityAuthFailed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestChangeDeviceBlobKeyVerifierOverwritesWithCurrentVerifier(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	oldVerifier := blobVerifierForTest(0x44)
	newVerifier := blobVerifierForTest(0x55)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(oldVerifier))
	mock.ExpectExec("UPDATE devices").
		WithArgs(accountID, deviceID, newVerifier).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := st.ChangeDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, newVerifier, oldVerifier); err != nil {
		t.Fatalf("ChangeDeviceBlobKeyVerifier overwrite returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestVerifyDeviceBlobKeyVerifierAcceptsStoredVerifierWithoutRawKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	verifier := blobVerifierForTest(0x66)

	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(verifier))

	if err := st.VerifyDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, verifier); err != nil {
		t.Fatalf("VerifyDeviceBlobKeyVerifier returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestVerifyDeviceBlobKeyVerifierRejectsWrongVerifier(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"

	mock.ExpectQuery("SELECT blob_key_verifier").
		WithArgs(accountID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_key_verifier"}).AddRow(blobVerifierForTest(0x77)))

	err = st.VerifyDeviceBlobKeyVerifier(context.Background(), accountID, deviceID, blobVerifierForTest(0x88))
	if !errors.Is(err, ErrIdentityAuthFailed) {
		t.Fatalf("VerifyDeviceBlobKeyVerifier error = %v, want %v", err, ErrIdentityAuthFailed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func blobAccessKeyForTest(fill byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func blobVerifierForTest(fill byte) string {
	return blobVerifierFromAccessKeyForTest(nil, blobAccessKeyForTest(fill))
}

func accountRecoveryCredentialForTest(t *testing.T) AccountRecoveryCredential {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate recovery key: %v", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal recovery public key: %v", err)
	}
	return AccountRecoveryCredential{
		AccountID:          "11111111-1111-1111-1111-111111111111",
		Version:            AccountRecoveryVersion,
		KDFAlgorithm:       AccountRecoveryKDFAlgorithm,
		KDFSalt:            base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		KDFIterations:      AccountRecoveryMinIterations,
		EnvelopeAlgorithm:  AccountRecoveryEnvelopeAlgorithm,
		EnvelopeIV:         base64.RawURLEncoding.EncodeToString(make([]byte, 12)),
		EnvelopeCiphertext: base64.RawURLEncoding.EncodeToString(make([]byte, 96)),
		RecoveryPublicKey:  base64.StdEncoding.EncodeToString(publicKeyDER),
		BlobKeyVerifier:    blobVerifierForTest(0x44),
	}
}

func blobVerifierFromAccessKeyForTest(t *testing.T, accessKey string) string {
	raw, err := base64.RawURLEncoding.DecodeString(accessKey)
	if err != nil {
		if t == nil {
			panic(err)
		}
		t.Fatalf("decode blob access key: %v", err)
	}
	if len(raw) != 32 {
		if t == nil {
			panic("blob access key has invalid length")
		}
		t.Fatalf("blob access key length = %d, want 32", len(raw))
	}
	sum := sha256.Sum256(append([]byte("virtroid-blob-verifier-v1:"), raw...))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestDeleteAccountReportsMissingAccount(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM accounts").
		WithArgs(accountID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	if err := st.DeleteAccount(context.Background(), accountID); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("DeleteAccount error = %v, want %v", err, ErrAccountNotFound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRuntimeDeletedObservationFinalizesPendingAccountDeletion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	lastError := "removed"

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(runtimeID, "host-1", sql.NullString{String: lastError, Valid: true}, int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(accountID))
	mock.ExpectQuery("SELECT deleted_at FROM accounts").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"deleted_at"}).AddRow(now))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("DELETE FROM accounts").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := st.UpdateRuntimeObservation(context.Background(), runtimeID, RuntimeObservation{
		HostID:              "host-1",
		OperationGeneration: 1,
		Deleted:             true,
		LastError:           &lastError,
	}); err != nil {
		t.Fatalf("UpdateRuntimeObservation returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAssignPendingRemoteRuntimeCleanupNeverClaimsLocalSnapshots(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	assigned, err := st.AssignPendingRemoteRuntimeCleanup(context.Background(), "host-1", "local-disk")
	if err != nil {
		t.Fatalf("AssignPendingRemoteRuntimeCleanup: %v", err)
	}
	if assigned != 0 {
		t.Fatalf("assigned = %d, want zero for local snapshots", assigned)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected SQL: %v", err)
	}
}

func TestAssignPendingRemoteRuntimeCleanupClaimsMatchingStore(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	mock.ExpectExec("UPDATE runtimes").
		WithArgs("host-1", "sia-renterd").
		WillReturnResult(sqlmock.NewResult(0, 2))

	assigned, err := st.AssignPendingRemoteRuntimeCleanup(context.Background(), "host-1", "SIA-RENTERD")
	if err != nil {
		t.Fatalf("AssignPendingRemoteRuntimeCleanup: %v", err)
	}
	if assigned != 2 {
		t.Fatalf("assigned = %d, want 2", assigned)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRuntimeBlobKeyHandoffPersistsActivatesAndRestoresEncryptedEnvelope(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	expiresAt := time.Now().UTC().Add(time.Minute)
	lease := RuntimeBlobKeyHandoff{
		AccountID: "11111111-1111-1111-1111-111111111111",
		RuntimeID: "33333333-3333-3333-3333-333333333333",
		HostID:    "host-1",
		Operation: "stop",
		LeaseID:   "lease-1",
		ExpiresAt: expiresAt,
	}
	mock.ExpectExec("INSERT INTO runtime_blob_key_handoffs").
		WithArgs(lease.AccountID, lease.RuntimeID, lease.HostID, lease.Operation, lease.LeaseID, expiresAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := st.PutRuntimeBlobKeyLease(context.Background(), lease); err != nil {
		t.Fatalf("PutRuntimeBlobKeyLease: %v", err)
	}

	lease.EnvelopeJSON = `{"version":1,"ciphertext":"encrypted"}`
	lease.BlobKeyVerifier = "verifier"
	mock.ExpectQuery("UPDATE runtime_blob_key_handoffs").
		WithArgs(
			lease.AccountID,
			lease.RuntimeID,
			lease.HostID,
			lease.Operation,
			lease.LeaseID,
			lease.EnvelopeJSON,
			lease.BlobKeyVerifier,
		).
		WillReturnRows(sqlmock.NewRows([]string{"expires_at"}).AddRow(expiresAt))
	activated, err := st.ActivateRuntimeBlobKeyHandoff(context.Background(), lease)
	if err != nil {
		t.Fatalf("ActivateRuntimeBlobKeyHandoff: %v", err)
	}
	if !activated.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("activated expiry = %v, want %v", activated.ExpiresAt, expiresAt)
	}

	mock.ExpectQuery("SELECT account_id, runtime_id").
		WithArgs(lease.RuntimeID, lease.HostID).
		WillReturnRows(sqlmock.NewRows([]string{
			"account_id", "runtime_id", "host_id", "operation", "lease_id",
			"envelope_json", "blob_key_verifier", "expires_at",
		}).AddRow(
			lease.AccountID,
			lease.RuntimeID,
			lease.HostID,
			lease.Operation,
			lease.LeaseID,
			lease.EnvelopeJSON,
			lease.BlobKeyVerifier,
			expiresAt,
		))
	restored, err := st.GetRuntimeBlobKeyHandoff(context.Background(), lease.RuntimeID, lease.HostID)
	if err != nil {
		t.Fatalf("GetRuntimeBlobKeyHandoff: %v", err)
	}
	if restored.EnvelopeJSON != lease.EnvelopeJSON || restored.BlobKeyVerifier != lease.BlobKeyVerifier {
		t.Fatalf("restored handoff = %+v, want encrypted envelope and verifier", restored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRuntimeBlobKeyHandoffActivationRejectsExpiredOrMismatchedLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	handoff := RuntimeBlobKeyHandoff{
		AccountID:       "11111111-1111-1111-1111-111111111111",
		RuntimeID:       "33333333-3333-3333-3333-333333333333",
		HostID:          "host-1",
		Operation:       "stop",
		LeaseID:         "wrong-lease",
		EnvelopeJSON:    `{"version":1}`,
		BlobKeyVerifier: "verifier",
	}
	mock.ExpectQuery("UPDATE runtime_blob_key_handoffs").
		WithArgs(
			handoff.AccountID,
			handoff.RuntimeID,
			handoff.HostID,
			handoff.Operation,
			handoff.LeaseID,
			handoff.EnvelopeJSON,
			handoff.BlobKeyVerifier,
		).
		WillReturnError(sql.ErrNoRows)

	_, err = st.ActivateRuntimeBlobKeyHandoff(context.Background(), handoff)
	if !errors.Is(err, ErrRuntimeBlobKeyHandoff) {
		t.Fatalf("ActivateRuntimeBlobKeyHandoff error = %v, want %v", err, ErrRuntimeBlobKeyHandoff)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestStoppedOrErrorObservationRetainsAssignmentWithoutCleanupAck(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "33333333-3333-3333-3333-333333333333"

	for _, status := range []string{"stopped", "error"} {
		mock.ExpectExec(`OR \(\$16 AND desired_state <> 'running' AND \$3 IN \('stopped', 'error'\)\)`).
			WithArgs(
				runtimeID,
				"host-1",
				status,
				"offline",
				nil,
				nil,
				nil,
				nil,
				false,
				false,
				nil,
				false,
				nil,
				nil,
				nil,
				false,
				int64(1),
			).
			WillReturnResult(sqlmock.NewResult(0, 1))

		if err := st.UpdateRuntimeObservation(context.Background(), runtimeID, RuntimeObservation{
			HostID:              "host-1",
			Status:              status,
			ConnectionStatus:    "offline",
			OperationGeneration: 1,
		}); err != nil {
			t.Fatalf("UpdateRuntimeObservation(%s) returned error: %v", status, err)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestStoppedObservationCleanupAckReleasesAssignment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectExec(`OR \(\$16 AND desired_state <> 'running' AND \$3 IN \('stopped', 'error'\)\)`).
		WithArgs(
			runtimeID,
			"host-1",
			"stopped",
			"offline",
			nil,
			nil,
			nil,
			nil,
			false,
			false,
			nil,
			false,
			nil,
			nil,
			nil,
			true,
			int64(1),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := st.UpdateRuntimeObservation(context.Background(), runtimeID, RuntimeObservation{
		HostID:              "host-1",
		Status:              "stopped",
		ConnectionStatus:    "offline",
		CleanupComplete:     true,
		OperationGeneration: 1,
	}); err != nil {
		t.Fatalf("UpdateRuntimeObservation returned error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRuntimeObservationRejectsStaleOperationGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "33333333-3333-3333-3333-333333333333"
	mock.ExpectExec(`operation_generation = \$17`).
		WithArgs(
			runtimeID,
			"host-1",
			"running",
			"online",
			nil,
			nil,
			nil,
			nil,
			false,
			false,
			nil,
			false,
			nil,
			nil,
			nil,
			false,
			int64(7),
		).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = st.UpdateRuntimeObservation(context.Background(), runtimeID, RuntimeObservation{
		HostID:              "host-1",
		Status:              "running",
		ConnectionStatus:    "online",
		OperationGeneration: 7,
	})
	if !errors.Is(err, ErrRuntimeObservationStale) {
		t.Fatalf("UpdateRuntimeObservation error = %v, want %v", err, ErrRuntimeObservationStale)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestSchemaCarriesMonotonicRuntimeOperationGeneration(t *testing.T) {
	if !strings.Contains(schemaSQL, "operation_generation BIGINT NOT NULL DEFAULT 1") {
		t.Fatal("runtime operation generation column is missing from the schema")
	}
}

func TestCreateRuntimeRequiresEntitlement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectBegin()
	mock.ExpectExec("SELECT 1 FROM accounts").
		WithArgs(accountID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT account_id, source, status").
		WithArgs(accountID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = st.CreateRuntime(context.Background(), accountID, CreateRuntimeInput{})
	if !errors.Is(err, ErrRuntimeEntitlement) {
		t.Fatalf("CreateRuntime error = %v, want %v", err, ErrRuntimeEntitlement)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetAccountEntitlementSummaryReportsRemainingTrialUse(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectQuery("SELECT account_id, source, status").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{
			"account_id",
			"source",
			"status",
			"runtime_limit",
			"active_runtime_limit",
			"runtime_starts_per_day",
			"storage_bytes_limit",
			"trial_runtime_seconds",
			"expires_at",
			"created_at",
			"updated_at",
		}).AddRow(
			accountID,
			"trial",
			"active",
			3,
			1,
			10,
			int64(1073741824),
			3600,
			nil,
			now,
			now,
		))
	mock.ExpectQuery("SELECT").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"runtime_count", "active_runtime_count"}).
			AddRow(2, 0))
	mock.ExpectQuery("runtime_start_events").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectQuery("FROM sessions s").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(int64(900)))
	mock.ExpectQuery("SELECT blob_manifest_json").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"blob_manifest_json"}).
			AddRow(`{"version":3,"snapshot_id":"snapshot-a","generation":1,"total_bytes":8388608,"chunks":[{"size":8388608}]}`))

	summary, err := st.GetAccountEntitlementSummary(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccountEntitlementSummary returned error: %v", err)
	}
	if summary.RuntimeLimit != 3 || summary.RuntimeCount != 2 || summary.RuntimeRemaining != 1 {
		t.Fatalf("runtime quota summary = limit %d count %d remaining %d; want 3, 2, 1", summary.RuntimeLimit, summary.RuntimeCount, summary.RuntimeRemaining)
	}
	if summary.RuntimeStartsPerDay != 10 || summary.RuntimeStartsUsedToday != 4 || summary.RuntimeStartsRemainingToday != 6 {
		t.Fatalf("start quota summary = limit %d used %d remaining %d; want 10, 4, 6", summary.RuntimeStartsPerDay, summary.RuntimeStartsUsedToday, summary.RuntimeStartsRemainingToday)
	}
	if summary.TrialRuntimeSecondsUsed != 900 || summary.TrialRuntimeSecondsRemaining != 2700 {
		t.Fatalf("trial time summary = used %d remaining %d; want 900, 2700", summary.TrialRuntimeSecondsUsed, summary.TrialRuntimeSecondsRemaining)
	}
	if summary.StorageBytesUsed != 8388608 || summary.StorageBytesRemaining != 1065353216 {
		t.Fatalf("storage summary = used %d remaining %d; want 8388608, 1065353216", summary.StorageBytesUsed, summary.StorageBytesRemaining)
	}
	if !summary.CanCreateRuntime || !summary.CanStartRuntime {
		t.Fatalf("trial summary blocked create=%v start=%v; want both allowed", summary.CanCreateRuntime, summary.CanStartRuntime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestStoredBlobManifestBytesCountsFallbackAndValidatesChunks(t *testing.T) {
	raw := `{
		"version":3,"snapshot_id":"snapshot-new","generation":2,
		"total_bytes":700,
		"chunks":[{"size":400},{"size":300}],
		"migration_fallback":{"version":3,"snapshot_id":"snapshot-old","generation":1,"total_bytes":200,"chunks":[{"size":200}]}
	}`
	used, err := storedBlobManifestBytes(raw)
	if err != nil {
		t.Fatalf("storedBlobManifestBytes returned error: %v", err)
	}
	if used != 900 {
		t.Fatalf("stored bytes = %d, want 900", used)
	}

	if _, err := storedBlobManifestBytes(`{"version":3,"snapshot_id":"snapshot-a","generation":1,"total_bytes":700,"chunks":[{"size":699}]}`); err == nil {
		t.Fatal("storedBlobManifestBytes accepted a mismatched chunk total")
	}
	if _, err := storedBlobManifestBytes(`{
		"version":3,"snapshot_id":"snapshot-c","generation":3,"total_bytes":1,"chunks":[{"size":1}],
		"migration_fallback":{"version":3,"snapshot_id":"snapshot-b","generation":2,"total_bytes":1,"chunks":[{"size":1}],
		"migration_fallback":{"version":3,"snapshot_id":"snapshot-a","generation":1,"total_bytes":1,"chunks":[{"size":1}]}}
	}`); err == nil {
		t.Fatal("storedBlobManifestBytes accepted a nested migration fallback")
	}
}

func TestValidateStoredBlobManifestAdvanceRejectsRollbackAndFork(t *testing.T) {
	currentRaw := `{"version":3,"snapshot_id":"snapshot-2","generation":2,"total_bytes":1,"chunks":[{"size":1}]}`
	cases := map[string]*storedBlobManifestUsage{
		"rollback": {Version: 3, SnapshotID: "snapshot-1", Generation: 1, TotalBytes: 1, Chunks: []storedBlobChunkUsage{{Size: 1}}},
		"fork":     {Version: 3, SnapshotID: "snapshot-fork", Generation: 2, TotalBytes: 1, Chunks: []storedBlobChunkUsage{{Size: 1}}},
		"skip":     {Version: 3, SnapshotID: "snapshot-4", Generation: 4, TotalBytes: 1, Chunks: []storedBlobChunkUsage{{Size: 1}}},
	}
	for name, incoming := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateStoredBlobManifestAdvance(currentRaw, incoming); !errors.Is(err, ErrRuntimeSnapshotRollback) {
				t.Fatalf("validateStoredBlobManifestAdvance error = %v, want %v", err, ErrRuntimeSnapshotRollback)
			}
		})
	}
	if err := validateStoredBlobManifestAdvance(currentRaw, &storedBlobManifestUsage{
		Version: 3, SnapshotID: "snapshot-3", Generation: 3, TotalBytes: 1, Chunks: []storedBlobChunkUsage{{Size: 1}},
	}); err != nil {
		t.Fatalf("validateStoredBlobManifestAdvance rejected next generation: %v", err)
	}
	if err := validateStoredBlobManifestAdvance(currentRaw, &storedBlobManifestUsage{
		Version: 3, SnapshotID: "snapshot-2", Generation: 2, TotalBytes: 1, Chunks: []storedBlobChunkUsage{{Size: 1}},
	}); err != nil {
		t.Fatalf("validateStoredBlobManifestAdvance rejected idempotent commit: %v", err)
	}
}

func TestUpdateRuntimeObservationRejectsStorageQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "33333333-3333-3333-3333-333333333333"
	accountID := "11111111-1111-1111-1111-111111111111"
	manifest := `{"version":3,"snapshot_id":"snapshot-new","generation":1,"total_bytes":700,"chunks":[{"size":700}]}`

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_id").
		WithArgs(runtimeID, "host-1", int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "blob_manifest_json"}).AddRow(accountID, nil))
	mock.ExpectQuery("SELECT storage_bytes_limit").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"storage_bytes_limit"}).AddRow(int64(1000)))
	mock.ExpectQuery("SELECT id, blob_manifest_json").
		WithArgs(accountID, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "blob_manifest_json"}).
			AddRow("44444444-4444-4444-4444-444444444444", `{"version":3,"snapshot_id":"snapshot-other","generation":1,"total_bytes":400,"chunks":[{"size":400}]}`))
	mock.ExpectRollback()

	err = st.UpdateRuntimeObservation(context.Background(), runtimeID, RuntimeObservation{
		HostID:              "host-1",
		Status:              "stopped",
		ConnectionStatus:    "offline",
		BlobManifestJSON:    &manifest,
		OperationGeneration: 7,
	})
	if !errors.Is(err, ErrRuntimeStorageQuota) {
		t.Fatalf("UpdateRuntimeObservation error = %v, want %v", err, ErrRuntimeStorageQuota)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestUpdateRuntimeObservationCommitsWithinStorageQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "33333333-3333-3333-3333-333333333333"
	accountID := "11111111-1111-1111-1111-111111111111"
	manifest := `{"version":3,"snapshot_id":"snapshot-new","generation":1,"total_bytes":500,"chunks":[{"size":500}]}`

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_id").
		WithArgs(runtimeID, "host-1", int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "blob_manifest_json"}).AddRow(accountID, nil))
	mock.ExpectQuery("SELECT storage_bytes_limit").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"storage_bytes_limit"}).AddRow(int64(1000)))
	mock.ExpectQuery("SELECT id, blob_manifest_json").
		WithArgs(accountID, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "blob_manifest_json"}).
			AddRow("44444444-4444-4444-4444-444444444444", `{"version":3,"snapshot_id":"snapshot-other","generation":1,"total_bytes":400,"chunks":[{"size":400}]}`))
	mock.ExpectExec("UPDATE runtimes").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = st.UpdateRuntimeObservation(context.Background(), runtimeID, RuntimeObservation{
		HostID:              "host-1",
		Status:              "stopped",
		ConnectionStatus:    "offline",
		BlobManifestJSON:    &manifest,
		OperationGeneration: 7,
	})
	if err != nil {
		t.Fatalf("UpdateRuntimeObservation returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestListAppCatalogMarksSelectedApps(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectQuery("SELECT c.package_name").
		WithArgs(accountID, "%", catalogResponseLimit).
		WillReturnRows(sqlmock.NewRows([]string{
			"package_name", "source", "display_name", "summary", "icon_url",
			"version_name", "version_code", "apk_url", "apk_sha256", "apk_size_bytes",
			"min_sdk", "native_code", "license", "categories_json", "anti_features_json",
			"recommended", "catalog_updated_at", "selected",
		}).AddRow(
			"org.videolan.vlc",
			"fdroid",
			"VLC",
			"Video player",
			"",
			"3.7.1",
			int64(13070108),
			"https://f-droid.org/repo/org.videolan.vlc_13070108.apk",
			"sha",
			int64(49444910),
			17,
			"x86_64",
			"",
			"[]",
			"[]",
			true,
			now,
			true,
		))

	items, err := st.ListAppCatalog(context.Background(), accountID, "")
	if err != nil {
		t.Fatalf("ListAppCatalog returned error: %v", err)
	}
	if len(items) != 1 || !items[0].Selected || items[0].PackageName != "org.videolan.vlc" {
		t.Fatalf("items = %+v, want selected VLC", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestUpsertAppCatalogEntriesRejectsDowngradeAndDisablesMissingGenerationEntries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	entry := AppCatalogEntry{
		PackageName:      "org.example.app",
		Source:           "fdroid",
		DisplayName:      "Example",
		VersionName:      "1.0",
		VersionCode:      10,
		APKURL:           "https://f-droid.org/repo/org.example.app_10.apk",
		APKSHA256:        strings.Repeat("a", 64),
		APKSizeBytes:     1024,
		MinSDK:           28,
		CategoriesJSON:   "[]",
		AntiFeaturesJSON: "[]",
		CatalogUpdatedAt: now,
	}

	mock.ExpectBegin()
	mock.ExpectExec("WHERE EXCLUDED.version_code >= app_catalog.version_code").
		WithArgs(
			entry.PackageName,
			entry.Source,
			entry.DisplayName,
			entry.Summary,
			entry.IconURL,
			entry.VersionName,
			entry.VersionCode,
			entry.APKURL,
			entry.APKSHA256,
			entry.APKSizeBytes,
			entry.MinSDK,
			entry.NativeCode,
			entry.License,
			entry.CategoriesJSON,
			entry.AntiFeaturesJSON,
			entry.Recommended,
			entry.CatalogUpdatedAt,
		).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("AND package_name NOT IN").
		WithArgs(entry.Source, entry.PackageName).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	count, err := st.UpsertAppCatalogEntries(context.Background(), []AppCatalogEntry{entry})
	if err != nil {
		t.Fatalf("UpsertAppCatalogEntries returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("upserted count = %d, want 0 for a rejected downgrade", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestDisableAppCatalogSourceDisablesEveryEnabledStaleEntry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	mock.ExpectExec("WHERE source = \\$1").
		WithArgs("fdroid").
		WillReturnResult(sqlmock.NewResult(0, 3))

	count, err := st.DisableAppCatalogSource(context.Background(), "fdroid")
	if err != nil {
		t.Fatalf("DisableAppCatalogSource: %v", err)
	}
	if count != 3 {
		t.Fatalf("disabled count = %d, want 3", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCatalogBootstrapSeedNeverOverwritesAuthoritativeRows(t *testing.T) {
	seedStart := strings.Index(schemaSQL, "INSERT INTO app_catalog")
	if seedStart < 0 {
		t.Fatal("schema is missing app catalog bootstrap seed")
	}
	seedEndOffset := strings.Index(schemaSQL[seedStart:], "CREATE TABLE IF NOT EXISTS runtimes")
	if seedEndOffset < 0 {
		t.Fatal("schema is missing the boundary after app catalog bootstrap seed")
	}
	seedSQL := schemaSQL[seedStart : seedStart+seedEndOffset]
	if !strings.Contains(seedSQL, "ON CONFLICT (package_name) DO NOTHING") {
		t.Fatalf("catalog bootstrap seed must preserve every existing authoritative row: %s", seedSQL)
	}
	if strings.Contains(seedSQL, "DO UPDATE") || strings.Contains(seedSQL, "enabled = TRUE") {
		t.Fatalf("catalog bootstrap seed can overwrite or re-enable existing rows: %s", seedSQL)
	}
}

func TestReplaceAccountAppSelectionsValidatesPackages(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("org.videolan.vlc").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("org.missing.app").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	_, err = st.ReplaceAccountAppSelections(context.Background(), accountID, []string{
		"org.videolan.vlc",
		"org.missing.app",
	})
	if err == nil || !strings.Contains(err.Error(), "org.missing.app") {
		t.Fatalf("ReplaceAccountAppSelections error = %v, want unavailable package error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestStartRuntimeSerializesTransitionAndMakesRetryIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	hostID := "host-1"

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeStateRows(now, runtimeID, accountID, "stopped", "stopped", "offline", hostID, 46000))
	mock.ExpectQuery("SELECT account_id, source, status").
		WithArgs(accountID).
		WillReturnRows(entitlementRows(accountID, now))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM runtime_start_events").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM sessions s").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT h.id FROM hosts h").
		WithArgs(hostID, true, true, false, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(hostID))
	mock.ExpectQuery(`UPDATE runtimes(?s:.*?)started_at = NOW\(\)`).
		WithArgs(accountID, runtimeID, hostID, sql.NullInt32{Int32: 46000, Valid: true}, true).
		WillReturnRows(runtimeStartRows(now, runtimeID, accountID, hostID, 2, 46000))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "user", "info", "Runtime start requested. persona_version=2.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO runtime_start_events").
		WithArgs(accountID, runtimeID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	runtime, err := st.StartRuntime(context.Background(), accountID, runtimeID)
	if err != nil {
		t.Fatalf("StartRuntime returned error: %v", err)
	}
	if runtime.PersonaVersion != 2 {
		t.Fatalf("persona_version = %d, want 2", runtime.PersonaVersion)
	}
	if runtime.ActivePersonaJSON != nil {
		t.Fatalf("active_persona_json = %q, want nil", *runtime.ActivePersonaJSON)
	}
	if runtime.ContainerName != nil {
		t.Fatalf("container_name = %q, want nil", *runtime.ContainerName)
	}
	if runtime.ADBPort != nil {
		t.Fatalf("adb_port = %d, want nil", *runtime.ADBPort)
	}

	// A second caller obtains the same row lock after the first transaction and
	// observes desired_state=running. No entitlement query, host selection,
	// UPDATE, log, or start-event expectation follows: any duplicate transition
	// would fail this test as an unexpected SQL operation.
	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeStartRows(now, runtimeID, accountID, hostID, 2, 46000))
	mock.ExpectCommit()

	retried, err := st.StartRuntime(context.Background(), accountID, runtimeID)
	if err != nil {
		t.Fatalf("retry StartRuntime returned error: %v", err)
	}
	if retried.PersonaVersion != runtime.PersonaVersion || retried.Status != runtime.Status || retried.DesiredState != "running" {
		t.Fatalf("retry runtime = %+v, want unchanged start transition %+v", retried, runtime)
	}
	if retried.ViewerPort == nil || *retried.ViewerPort != 46000 {
		t.Fatalf("retry viewer_port = %v, want preserved 46000", retried.ViewerPort)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestEnsureRuntimeStartEntitlementRejectsExhaustedTrialTime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT account_id, source, status").
		WithArgs(accountID).
		WillReturnRows(entitlementRows(accountID, now))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM runtime_start_events").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("FROM sessions s").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(int64(3600)))
	mock.ExpectRollback()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := ensureRuntimeStartEntitlementTX(context.Background(), tx, accountID, runtimeID); !errors.Is(err, ErrRuntimeTrialTimeQuota) {
		t.Fatalf("ensureRuntimeStartEntitlementTX error = %v, want %v", err, ErrRuntimeTrialTimeQuota)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestNormalizedCreateInputRejectsUnsafeProfile(t *testing.T) {
	if _, err := normalizedCreateInput(CreateRuntimeInput{AndroidImage: "attacker/image:latest"}); !errors.Is(err, ErrRuntimeProfile) {
		t.Fatalf("custom android image error = %v, want %v", err, ErrRuntimeProfile)
	}
	if _, err := normalizedCreateInput(CreateRuntimeInput{WidthPx: 1920, HeightPx: 1080, DensityDpi: 320}); !errors.Is(err, ErrRuntimeProfile) {
		t.Fatalf("oversized profile error = %v, want %v", err, ErrRuntimeProfile)
	}
}

func TestNormalizedCreateInputPreservesExplicitAudioAndAllowsPhotoImport(t *testing.T) {
	input, err := normalizedCreateInput(CreateRuntimeInput{
		AudioEnabled:    false,
		AudioEnabledSet: true,
		CameraMode:      "photo-import",
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.AudioEnabled {
		t.Fatal("explicit audio disable was replaced by the default")
	}
	if input.CameraMode != "photo-import" {
		t.Fatalf("camera mode = %q", input.CameraMode)
	}
}

func TestNormalizeAccountStorageInputRejectsUserFundingMaterial(t *testing.T) {
	wallet := "user-wallet"
	seed := "encrypted-seed"
	hint := "seed-hint"
	tests := []struct {
		name  string
		input UpdateAccountStorageInput
	}{
		{
			name: "user funded mode",
			input: UpdateAccountStorageInput{
				Provider:     "sia-renterd",
				FundingModel: "user-funded",
			},
		},
		{
			name: "wallet address",
			input: UpdateAccountStorageInput{
				Provider:      "sia-renterd",
				FundingModel:  "operator-pooled",
				WalletAddress: &wallet,
			},
		},
		{
			name: "seed material",
			input: UpdateAccountStorageInput{
				Provider:           "sia-renterd",
				FundingModel:       "operator-pooled",
				EncryptedSeedBlob:  &seed,
				SeedEncryptionHint: &hint,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := normalizeAccountStorageInput(tt.input); err == nil {
				t.Fatal("normalizeAccountStorageInput accepted user funding material")
			}
		})
	}
}

func TestNormalizeAccountStorageInputAllowsOperatorPooledWithoutWallet(t *testing.T) {
	normalized, err := normalizeAccountStorageInput(UpdateAccountStorageInput{
		Provider:     "sia-renterd",
		FundingModel: "operator-pooled",
		Status:       "ready",
	})
	if err != nil {
		t.Fatalf("normalizeAccountStorageInput: %v", err)
	}
	if normalized.WalletAddress != nil || normalized.EncryptedSeedBlob != nil || normalized.SeedEncryptionHint != nil {
		t.Fatalf("operator-pooled storage retained account funding material: %+v", normalized)
	}
}

func TestHashRelayTokenDoesNotStoreBearerToken(t *testing.T) {
	token := "relay-secret-token"
	hashed := hashRelayToken(token)

	if hashed == token {
		t.Fatal("hashRelayToken returned the raw bearer token")
	}
	if hashed == hashRelayToken("different-token") {
		t.Fatal("hashRelayToken returned the same value for different tokens")
	}
	if hashed != hashRelayToken(token) {
		t.Fatal("hashRelayToken is not stable for the same token")
	}
}

func TestGetSessionStateReturnsResumableActiveSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	sessionID := "55555555-5555-5555-5555-555555555555"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	hostID := "host-1"

	mock.ExpectQuery("SELECT s.id, s.runtime_id").
		WithArgs(sessionID, deviceID, accountID).
		WillReturnRows(sessionStateRows(
			now,
			sessionID,
			runtimeID,
			deviceID,
			"active",
			now.Add(2*time.Minute),
			accountID,
			"running",
			"running",
			"online",
			hostID,
			46000,
		))

	state, err := st.GetSessionState(context.Background(), accountID, deviceID, sessionID)
	if err != nil {
		t.Fatalf("GetSessionState returned error: %v", err)
	}
	if state.EffectiveStatus != "active" || state.IsExpired || !state.RuntimeReady || !state.CanResume {
		t.Fatalf("state = %+v, want resumable active session", state)
	}
	if state.Runtime.ID != runtimeID || state.Session.ID != sessionID {
		t.Fatalf("state IDs = session %s runtime %s", state.Session.ID, state.Runtime.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetSessionStateMarksExpiredSessionNotResumable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	sessionID := "55555555-5555-5555-5555-555555555555"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	hostID := "host-1"

	mock.ExpectQuery("SELECT s.id, s.runtime_id").
		WithArgs(sessionID, deviceID, accountID).
		WillReturnRows(sessionStateRows(
			now,
			sessionID,
			runtimeID,
			deviceID,
			"active",
			now.Add(-time.Second),
			accountID,
			"running",
			"running",
			"online",
			hostID,
			46000,
		))

	state, err := st.GetSessionState(context.Background(), accountID, deviceID, sessionID)
	if err != nil {
		t.Fatalf("GetSessionState returned error: %v", err)
	}
	if state.EffectiveStatus != "expired" || !state.IsExpired || state.CanResume {
		t.Fatalf("state = %+v, want expired non-resumable session", state)
	}
	if !state.RuntimeReady {
		t.Fatal("runtime should still be reported ready even though session expired")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetRuntimeStateReportsConnectableRuntimeAndCurrentSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	sessionID := "55555555-5555-5555-5555-555555555555"
	hostID := "host-1"

	mock.ExpectQuery("deleted_at IS NULL").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeStateRows(now, runtimeID, accountID, "running", "running", "online", hostID, 46000))
	mock.ExpectQuery("SELECT id").
		WithArgs(runtimeID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(sessionID))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	state, err := st.GetRuntimeState(context.Background(), accountID, deviceID, runtimeID)
	if err != nil {
		t.Fatalf("GetRuntimeState returned error: %v", err)
	}
	if state.EffectiveState != "running" || !state.RuntimeReady || !state.CanConnect || state.IsBusy {
		t.Fatalf("state = %+v, want connectable running runtime", state)
	}
	if !state.HasActiveSession || !state.HasCurrentDeviceSession || state.CurrentDeviceSessionID == nil || *state.CurrentDeviceSessionID != sessionID {
		t.Fatalf("session state = %+v, want current active session %s", state, sessionID)
	}
	if state.CanStart {
		t.Fatal("running runtime should not report can_start")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBuildRuntimeStateBlocksStartUntilFailedRuntimeCleanupFinishes(t *testing.T) {
	hostID := "host-1"
	lastError := "snapshot still pending"
	assigned := buildRuntimeState(Runtime{
		Status:           "error",
		DesiredState:     "stopped",
		ConnectionStatus: "offline",
		HostID:           &hostID,
		LastError:        &lastError,
	}, false, sql.NullString{})
	if assigned.CanStart || !assigned.CanStop || assigned.EffectiveState != "error" {
		t.Fatalf("assigned failed runtime state = %+v, want recovery stop only", assigned)
	}

	cleaned := buildRuntimeState(Runtime{
		Status:           "stopped",
		DesiredState:     "stopped",
		ConnectionStatus: "offline",
	}, false, sql.NullString{})
	if !cleaned.CanStart || cleaned.CanStop || cleaned.EffectiveState != "stopped" {
		t.Fatalf("cleaned stopped runtime state = %+v, want start only", cleaned)
	}

	pending := buildRuntimeState(Runtime{
		Status:           "stopped",
		DesiredState:     "stopped",
		ConnectionStatus: "offline",
		HostID:           &hostID,
		CleanupPending:   true,
	}, false, sql.NullString{})
	if pending.CanStart || pending.CanDelete || !pending.IsBusy || pending.BlockedReason != ErrRuntimeCleanupPending.Error() {
		t.Fatalf("cleanup-pending runtime state = %+v, want busy with lifecycle actions blocked", pending)
	}
}

func TestGetRuntimeStateReportsDeletingRuntimeAsBusy(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	hostID := "host-1"

	mock.ExpectQuery("deleted_at IS NULL").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeStateRows(now, runtimeID, accountID, "deleting", "deleted", "offline", hostID, 46000))
	mock.ExpectQuery("SELECT id").
		WithArgs(runtimeID, deviceID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	state, err := st.GetRuntimeState(context.Background(), accountID, deviceID, runtimeID)
	if err != nil {
		t.Fatalf("GetRuntimeState returned error: %v", err)
	}
	if state.EffectiveState != "deleting" || !state.IsBusy || state.CanConnect || state.CanStart || state.CanDelete {
		t.Fatalf("state = %+v, want busy deleting runtime with actions blocked", state)
	}
	if state.HasActiveSession || state.HasCurrentDeviceSession {
		t.Fatalf("session state = %+v, want no active sessions", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetRuntimeStateRejectsSoftDeletedRuntime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectQuery("deleted_at IS NULL").
		WithArgs(accountID, runtimeID).
		WillReturnError(sql.ErrNoRows)

	_, err = st.GetRuntimeState(context.Background(), accountID, deviceID, runtimeID)
	if !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("GetRuntimeState error = %v, want %v", err, ErrRuntimeNotFound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestWipeRuntimeOnHostAssignsStoppedRuntimeForNodeCleanup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	hostID := "host-1"

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(accountID, runtimeID, hostID).
		WillReturnRows(runtimeStateRows(now, runtimeID, accountID, "wiping", "stopped", "offline", hostID, nil))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(runtimeID, "runtime wiped").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WithArgs(runtimeID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "user", "warn", "Runtime wipe requested. User data will be removed.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	runtime, err := st.WipeRuntimeOnHost(context.Background(), accountID, runtimeID, hostID)
	if err != nil {
		t.Fatalf("WipeRuntimeOnHost returned error: %v", err)
	}
	if runtime.HostID == nil || *runtime.HostID != hostID || runtime.Status != "wiping" {
		t.Fatalf("runtime = %+v, want host-assigned wiping runtime", runtime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestStopRuntimeIsIdempotentWhenAlreadyStopped(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeStateRows(now, runtimeID, accountID, "stopped", "stopped", "offline", nil, nil))
	mock.ExpectCommit()

	runtime, err := st.StopRuntime(context.Background(), accountID, runtimeID)
	if err != nil {
		t.Fatalf("StopRuntime: %v", err)
	}
	if runtime.Status != "stopped" || runtime.DesiredState != "stopped" {
		t.Fatalf("runtime = %+v, want unchanged stopped runtime", runtime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestDeleteRuntimeOnHostAssignsStoppedRuntimeForNodeCleanup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	hostID := "host-1"

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(accountID, runtimeID, hostID).
		WillReturnRows(runtimeStateRows(now, runtimeID, accountID, "deleting", "deleted", "offline", hostID, nil))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(runtimeID, "runtime deleted").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WithArgs(runtimeID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "user", "warn", "Runtime scheduled for deletion.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	runtime, err := st.DeleteRuntimeOnHost(context.Background(), accountID, runtimeID, hostID)
	if err != nil {
		t.Fatalf("DeleteRuntimeOnHost returned error: %v", err)
	}
	if runtime.HostID == nil || *runtime.HostID != hostID || runtime.Status != "deleting" {
		t.Fatalf("runtime = %+v, want host-assigned deleting runtime", runtime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRuntimeBlobKeyTargetWipeUsesLocalBlobOwnerHost(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	blobHostID := "host-blob"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeRowsWithBlob(now, runtimeID, accountID, "stopped", "stopped", "offline", nil, nil, "local-disk", `{"store":"local-disk"}`, blobHostID))
	mock.ExpectQuery("SELECT h.id, h.name, h.advertise_addr").
		WithArgs(blobHostID).
		WillReturnRows(hostRows(now, blobHostID, "node-public-key"))
	mock.ExpectCommit()

	_, host, err := st.RuntimeBlobKeyTarget(context.Background(), accountID, runtimeID, "wipe")
	if err != nil {
		t.Fatalf("RuntimeBlobKeyTarget returned error: %v", err)
	}
	if host.ID != blobHostID {
		t.Fatalf("host.ID = %q, want %q", host.ID, blobHostID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRuntimeBlobKeyTargetDuplicateStopUsesLocalBlobOwnerHost(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"
	blobHostID := "host-blob"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeRowsWithBlob(now, runtimeID, accountID, "stopped", "stopped", "offline", nil, nil, "local-disk", `{"store":"local-disk"}`, blobHostID))
	mock.ExpectQuery("SELECT h.id, h.name, h.advertise_addr").
		WithArgs(blobHostID).
		WillReturnRows(hostRows(now, blobHostID, "node-public-key"))
	mock.ExpectCommit()

	_, host, err := st.RuntimeBlobKeyTarget(context.Background(), accountID, runtimeID, "stop")
	if err != nil {
		t.Fatalf("RuntimeBlobKeyTarget returned error: %v", err)
	}
	if host.ID != blobHostID {
		t.Fatalf("host.ID = %q, want %q", host.ID, blobHostID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAttachStorageQuotaSkipsDeletingRuntimeWithoutEntitlement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	mock.ExpectRollback()

	runtimes := []Runtime{{
		ID:           "33333333-3333-3333-3333-333333333333",
		AccountID:    "11111111-1111-1111-1111-111111111111",
		Status:       "deleting",
		DesiredState: "deleted",
	}}
	if err := attachStorageQuotaToRuntimesTX(context.Background(), tx, runtimes); err != nil {
		t.Fatalf("attachStorageQuotaToRuntimesTX returned error: %v", err)
	}
	if runtimes[0].StorageBytesLimit != nil || runtimes[0].StorageBytesUsed != nil {
		t.Fatalf("deleting runtime unexpectedly received quota data: %+v", runtimes[0])
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRuntimeBlobKeyTargetRejectsHostlessLocalBlobWithoutOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	runtimeID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeRowsWithBlob(now, runtimeID, accountID, "stopped", "stopped", "offline", nil, nil, "local-disk", `{"store":"local-disk"}`, nil))
	mock.ExpectRollback()

	_, _, err = st.RuntimeBlobKeyTarget(context.Background(), accountID, runtimeID, "wipe")
	if !errors.Is(err, ErrRuntimeBlobOwner) {
		t.Fatalf("RuntimeBlobKeyTarget error = %v, want %v", err, ErrRuntimeBlobOwner)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestListRuntimeStatesReturnsBackendLifecycleSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runningRuntimeID := "33333333-3333-3333-3333-333333333333"
	stoppingRuntimeID := "44444444-4444-4444-4444-444444444444"
	sessionID := "55555555-5555-5555-5555-555555555555"
	hostID := "host-1"

	rows := runtimeStateListRows()
	rows.AddRow(runtimeStateListRowValues(
		now,
		runningRuntimeID,
		accountID,
		"running",
		"running",
		"online",
		hostID,
		46000,
		sessionID,
		true,
	)...)
	rows.AddRow(runtimeStateListRowValues(
		now,
		stoppingRuntimeID,
		accountID,
		"stopping",
		"stopped",
		"disconnecting",
		hostID,
		46001,
		nil,
		false,
	)...)
	mock.ExpectQuery("SELECT r.id, r.account_id").
		WithArgs(accountID, deviceID).
		WillReturnRows(rows)

	states, err := st.ListRuntimeStates(context.Background(), accountID, deviceID)
	if err != nil {
		t.Fatalf("ListRuntimeStates returned error: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}
	if states[0].Runtime.ID != runningRuntimeID || states[0].EffectiveState != "running" || !states[0].CanConnect {
		t.Fatalf("first state = %+v, want connectable running runtime", states[0])
	}
	if states[0].CurrentDeviceSessionID == nil || *states[0].CurrentDeviceSessionID != sessionID {
		t.Fatalf("current session id = %v, want %s", states[0].CurrentDeviceSessionID, sessionID)
	}
	if states[1].Runtime.ID != stoppingRuntimeID || states[1].EffectiveState != "stopping" || !states[1].IsBusy || states[1].CanStart {
		t.Fatalf("second state = %+v, want busy stopping runtime", states[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestListAssignedRuntimesRestoresMissingViewerPort(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM runtimes").
		WithArgs("host-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow("33333333-3333-3333-3333-333333333333"))
	mock.ExpectQuery("SELECT viewer_port FROM runtimes").
		WillReturnRows(sqlmock.NewRows([]string{"viewer_port"}))
	mock.ExpectExec("UPDATE runtimes").
		WithArgs("33333333-3333-3333-3333-333333333333", 46000, "host-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(
			"33333333-3333-3333-3333-333333333333",
			"system",
			"info",
			"Viewer port 46000 restored for running runtime assignment.",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT id, account_id, name").
		WithArgs("host-1").
		WillReturnRows(runtimeRows(now, 46000))
	mock.ExpectQuery("SELECT c.package_name").
		WithArgs("11111111-1111-1111-1111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{
			"package_name", "source", "display_name", "summary", "icon_url",
			"version_name", "version_code", "apk_url", "apk_sha256", "apk_size_bytes",
			"min_sdk", "native_code", "license", "categories_json", "anti_features_json",
			"recommended", "catalog_updated_at", "selected",
		}))
	mock.ExpectQuery("SELECT storage_bytes_limit").
		WithArgs("11111111-1111-1111-1111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{"storage_bytes_limit"}).AddRow(int64(1073741824)))
	mock.ExpectQuery("SELECT blob_manifest_json").
		WithArgs("11111111-1111-1111-1111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{"blob_manifest_json"}))
	mock.ExpectCommit()

	runtimes, err := st.ListAssignedRuntimes(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("ListAssignedRuntimes returned error: %v", err)
	}
	if len(runtimes) != 1 {
		t.Fatalf("len(runtimes) = %d, want 1", len(runtimes))
	}
	if runtimes[0].ViewerPort == nil || *runtimes[0].ViewerPort != 46000 {
		t.Fatalf("viewer port = %v, want 46000", runtimes[0].ViewerPort)
	}
	if runtimes[0].StorageBytesLimit == nil || *runtimes[0].StorageBytesLimit != 1073741824 ||
		runtimes[0].StorageBytesUsed == nil || *runtimes[0].StorageBytesUsed != 0 {
		t.Fatalf("storage quota = limit %v used %v, want 1073741824 and 0", runtimes[0].StorageBytesLimit, runtimes[0].StorageBytesUsed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestHeartbeatSessionExtendsLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	heartbeatAt := now.Add(time.Second)
	expiresAt := now.Add(2 * time.Minute)

	mock.ExpectQuery("UPDATE sessions AS s").
		WithArgs(
			"33333333-3333-3333-3333-333333333333",
			"22222222-2222-2222-2222-222222222222",
			"11111111-1111-1111-1111-111111111111",
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "runtime_id", "device_id", "status", "created_at", "updated_at",
			"last_client_heartbeat_at", "ended_at", "end_reason", "expires_at",
		}).AddRow(
			"33333333-3333-3333-3333-333333333333",
			"44444444-4444-4444-4444-444444444444",
			"22222222-2222-2222-2222-222222222222",
			"active",
			now,
			heartbeatAt,
			heartbeatAt,
			nil,
			nil,
			expiresAt,
		))

	session, err := st.HeartbeatSession(
		context.Background(),
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
	)
	if err != nil {
		t.Fatalf("HeartbeatSession returned error: %v", err)
	}
	if session.RelayToken != "" {
		t.Fatal("HeartbeatSession returned a bearer relay token")
	}
	if session.LastClientHeartbeatAt == nil || !session.LastClientHeartbeatAt.Equal(heartbeatAt) {
		t.Fatalf("last heartbeat = %v, want %v", session.LastClientHeartbeatAt, heartbeatAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestResolveSessionRelayTargetConsumesRelayToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	sessionID := "33333333-3333-3333-3333-333333333333"
	runtimeID := "44444444-4444-4444-4444-444444444444"
	deviceID := "22222222-2222-2222-2222-222222222222"
	hostID := "host-1"

	mock.ExpectQuery("relay_token_consumed_at IS NULL").
		WithArgs(sessionID, hashRelayToken("relay-token")).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "runtime_id", "device_id", "host_id", "viewer_port",
		}).AddRow(
			sessionID,
			runtimeID,
			deviceID,
			hostID,
			46000,
		))

	target, err := st.ResolveSessionRelayTarget(context.Background(), sessionID, "relay-token")
	if err != nil {
		t.Fatalf("ResolveSessionRelayTarget returned error: %v", err)
	}
	if target.SessionID != sessionID || target.RuntimeID != runtimeID || target.HostID != hostID || target.ViewerPort != 46000 {
		t.Fatalf("target = %+v, want resolved relay target", target)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestIssueSessionRelayTokenRequiresUnrevokedDevice(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	sessionID := "33333333-3333-3333-3333-333333333333"
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectQuery("d.revoked_at IS NULL").
		WithArgs(sessionID, deviceID, accountID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "runtime_id", "device_id", "status", "created_at", "updated_at",
			"last_client_heartbeat_at", "ended_at", "end_reason", "expires_at",
		}).AddRow(
			sessionID,
			runtimeID,
			deviceID,
			"active",
			now,
			now,
			now,
			nil,
			nil,
			now.Add(2*time.Minute),
		))

	session, err := st.IssueSessionRelayToken(context.Background(), accountID, deviceID, sessionID)
	if err != nil {
		t.Fatalf("IssueSessionRelayToken returned error: %v", err)
	}
	if session.RelayToken == "" {
		t.Fatal("IssueSessionRelayToken did not return a fresh relay token")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateSessionRequiresUnrevokedDevice(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectQuery("d.revoked_at IS NULL").
		WithArgs(runtimeID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "status", "desired_state", "connection_status"}).
			AddRow(accountID, "running", "running", "online"))
	expectTrialTimeAvailable(mock, accountID, 0)
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(sqlmock.AnyArg(), runtimeID, deviceID, "pending", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "runtime_id", "device_id", "status", "created_at", "updated_at",
			"last_client_heartbeat_at", "ended_at", "end_reason", "expires_at",
		}).AddRow(
			"33333333-3333-3333-3333-333333333333",
			runtimeID,
			deviceID,
			"pending",
			now,
			now,
			now,
			nil,
			nil,
			now.Add(2*time.Minute),
		))

	session, err := st.CreateSession(context.Background(), deviceID, runtimeID)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if session.RelayToken == "" {
		t.Fatal("CreateSession did not return an initial attach relay token")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateSessionRejectsExhaustedTrialTime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "44444444-4444-4444-4444-444444444444"
	mock.ExpectQuery("d.revoked_at IS NULL").
		WithArgs(runtimeID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "status", "desired_state", "connection_status"}).
			AddRow(accountID, "running", "running", "online"))
	mock.ExpectQuery("SELECT source, trial_runtime_seconds").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"source", "trial_runtime_seconds"}).AddRow("trial", 3600))
	mock.ExpectQuery("FROM sessions s").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(int64(3600)))

	if _, err := st.CreateSession(context.Background(), deviceID, runtimeID); !errors.Is(err, ErrRuntimeTrialTimeQuota) {
		t.Fatalf("CreateSession error = %v, want %v", err, ErrRuntimeTrialTimeQuota)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateSessionRejectsDifferentLiveActor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectQuery("d.revoked_at IS NULL").
		WithArgs(runtimeID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "status", "desired_state", "connection_status"}).
			AddRow(accountID, "running", "running", "online"))
	expectTrialTimeAvailable(mock, accountID, 0)
	mock.ExpectQuery("ON CONFLICT \\(runtime_id\\) WHERE status IN").
		WithArgs(sqlmock.AnyArg(), runtimeID, deviceID, "pending", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "runtime_id", "device_id", "status", "created_at", "updated_at",
			"last_client_heartbeat_at", "ended_at", "end_reason", "expires_at",
		}))

	if _, err := st.CreateSession(context.Background(), deviceID, runtimeID); !errors.Is(err, ErrSessionAlreadyActive) {
		t.Fatalf("CreateSession error = %v, want %v", err, ErrSessionAlreadyActive)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateSessionRejectsSameLiveActorWithoutRotatingToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectQuery("d.revoked_at IS NULL").
		WithArgs(runtimeID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "status", "desired_state", "connection_status"}).
			AddRow(accountID, "running", "running", "online"))
	expectTrialTimeAvailable(mock, accountID, 0)
	mock.ExpectQuery(`WHERE sessions.expires_at <= NOW\(\)`).
		WithArgs(sqlmock.AnyArg(), runtimeID, deviceID, "pending", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "runtime_id", "device_id", "status", "created_at", "updated_at",
			"last_client_heartbeat_at", "ended_at", "end_reason", "expires_at",
		}))

	if _, err := st.CreateSession(context.Background(), deviceID, runtimeID); !errors.Is(err, ErrSessionAlreadyActive) {
		t.Fatalf("CreateSession error = %v, want %v", err, ErrSessionAlreadyActive)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestSessionSchemaReconcilesLegacyDuplicatesBeforeUniqueInvariant(t *testing.T) {
	reconcileAt := strings.Index(schemaSQL, "WITH ranked_live_sessions AS")
	uniqueAt := strings.Index(schemaSQL, "CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_one_live_per_runtime")
	if reconcileAt < 0 || uniqueAt < 0 {
		t.Fatal("session schema is missing duplicate reconciliation or the one-live-session unique index")
	}
	if reconcileAt >= uniqueAt {
		t.Fatal("session duplicate reconciliation must run before creating the unique index")
	}
}

func TestCreateSessionWithCapabilityDoesNotStoreDeviceID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	capabilityID := "capability-1"
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectQuery("JOIN runtime_capabilities").
		WithArgs(runtimeID, accountID, capabilityID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "desired_state", "connection_status"}).
			AddRow("running", "running", "online"))
	expectTrialTimeAvailable(mock, accountID, 0)
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(sqlmock.AnyArg(), runtimeID, capabilityID, "pending", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "runtime_id", "device_id", "capability_id", "status", "created_at", "updated_at",
			"last_client_heartbeat_at", "ended_at", "end_reason", "expires_at",
		}).AddRow(
			"33333333-3333-3333-3333-333333333333",
			runtimeID,
			"",
			capabilityID,
			"pending",
			now,
			now,
			now,
			nil,
			nil,
			now.Add(2*time.Minute),
		))

	session, err := st.CreateSessionWithCapability(context.Background(), accountID, capabilityID, runtimeID)
	if err != nil {
		t.Fatalf("CreateSessionWithCapability returned error: %v", err)
	}
	if session.DeviceID != "" {
		t.Fatalf("capability session device id = %q, want empty", session.DeviceID)
	}
	if session.CapabilityID == nil || *session.CapabilityID != capabilityID {
		t.Fatalf("capability id = %#v, want %q", session.CapabilityID, capabilityID)
	}
	if session.RelayToken == "" {
		t.Fatal("CreateSessionWithCapability did not return an initial attach relay token")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAbortPendingSessionBindsExactRelayToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	sessionID := "33333333-3333-3333-3333-333333333333"
	relayToken := "one-time-relay-token"
	mock.ExpectExec("UPDATE sessions").
		WithArgs(sessionID, hashRelayToken(relayToken), "viewer preparation failed").
		WillReturnResult(sqlmock.NewResult(0, 1))

	aborted, err := st.AbortPendingSession(context.Background(), sessionID, relayToken, "viewer preparation failed")
	if err != nil {
		t.Fatalf("AbortPendingSession returned error: %v", err)
	}
	if !aborted {
		t.Fatal("AbortPendingSession did not report the exact pending reservation as closed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCloseSessionWithCapabilityRevokesCapability(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	capabilityID := "capability-1"
	sessionID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE sessions AS s").
		WithArgs(sessionID, capabilityID, accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WithArgs(accountID, capabilityID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := st.CloseSessionWithCapability(context.Background(), accountID, capabilityID, sessionID); err != nil {
		t.Fatalf("CloseSessionWithCapability returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestEndSessionAndStopRuntimeWithCapabilityRevokesRuntimeCapabilities(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	now := time.Now().UTC()
	accountID := "11111111-1111-1111-1111-111111111111"
	capabilityID := "capability-1"
	sessionID := "33333333-3333-3333-3333-333333333333"
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions AS s").
		WithArgs(sessionID, capabilityID, accountID, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"runtime_id"}).AddRow(runtimeID))
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(runtimeStateRows(now, runtimeID, accountID, "stopping", "stopped", "disconnecting", "host-1", 46000))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "user", "info", "Session closed and runtime stop queued.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WithArgs(runtimeID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	runtime, err := st.EndSessionAndStopRuntimeWithCapability(context.Background(), accountID, capabilityID, sessionID, runtimeID)
	if err != nil {
		t.Fatalf("EndSessionAndStopRuntimeWithCapability returned error: %v", err)
	}
	if runtime.ID != runtimeID || runtime.DesiredState != "stopped" {
		t.Fatalf("runtime = %+v, want stopped runtime %s", runtime, runtimeID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestRequireSessionRuntimeRejectsDifferentEnvelopeRuntime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	sessionID := "33333333-3333-3333-3333-333333333333"
	envelopeRuntimeID := "55555555-5555-5555-5555-555555555555"

	mock.ExpectQuery("AND s.runtime_id = \\$2").
		WithArgs(sessionID, envelopeRuntimeID, deviceID, accountID).
		WillReturnError(sql.ErrNoRows)

	err = st.RequireSessionRuntime(context.Background(), accountID, deviceID, sessionID, envelopeRuntimeID)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("RequireSessionRuntime error = %v, want %v", err, ErrSessionNotFound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestEndSessionDoesNotMutateWhenExpectedRuntimeDiffers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	accountID := "11111111-1111-1111-1111-111111111111"
	deviceID := "22222222-2222-2222-2222-222222222222"
	sessionID := "33333333-3333-3333-3333-333333333333"
	envelopeRuntimeID := "55555555-5555-5555-5555-555555555555"

	mock.ExpectBegin()
	mock.ExpectQuery("AND s.runtime_id = \\$4").
		WithArgs(sessionID, deviceID, accountID, envelopeRuntimeID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = st.EndSessionAndStopRuntime(context.Background(), accountID, deviceID, sessionID, envelopeRuntimeID)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("EndSessionAndStopRuntime error = %v, want %v", err, ErrSessionNotFound)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAppendSecurityEventLimitedStopsAfterNodeRateLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	event := SecurityEventInput{
		NodeID:    "node-1",
		Rule:      "Virtroid Shell Spawned In Managed Container",
		Priority:  "error",
		Output:    "shell spawned in Virtroid container",
		Tags:      []string{},
		EventJSON: "{}",
	}

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM security_events").
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM security_event_ingest_limits").
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO security_event_ingest_limits").
		WithArgs("node-1", sqlmock.AnyArg(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"event_count"}).AddRow(1))
	mock.ExpectExec("INSERT INTO security_events").
		WithArgs(
			"node-1",
			"falco",
			"Virtroid Shell Spawned In Managed Container",
			"error",
			"shell spawned in Virtroid container",
			"[]",
			"{}",
			nil,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := st.AppendSecurityEventLimited(context.Background(), event, 1, time.Hour); err != nil {
		t.Fatalf("first AppendSecurityEventLimited error = %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM security_events").
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM security_event_ingest_limits").
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO security_event_ingest_limits").
		WithArgs("node-1", sqlmock.AnyArg(), 1).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	err = st.AppendSecurityEventLimited(context.Background(), event, 1, time.Hour)
	if !errors.Is(err, ErrSecurityEventRateLimit) {
		t.Fatalf("second AppendSecurityEventLimited error = %v, want %v", err, ErrSecurityEventRateLimit)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReapStaleSessionsStopsIdleRuntime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow("11111111-1111-1111-1111-111111111111"))
	mock.ExpectQuery("UPDATE sessions").
		WithArgs("120 seconds").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow("22222222-2222-2222-2222-222222222222"))
	mock.ExpectQuery("WITH usage AS").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("WITH latest_session").
		WithArgs("120 seconds", "180 seconds").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).
			AddRow(runtimeID))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(
			runtimeID,
			"system",
			"warn",
			"Runtime stop queued because no active client session is heartbeating.",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WithArgs(runtimeID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE runtime_capabilities AS c").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("DELETE FROM runtime_capability_nonces").
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec("DELETE FROM runtime_blob_key_handoffs").
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectCommit()

	result, err := st.ReapStaleSessions(context.Background(), 2*time.Minute, 3*time.Minute)
	if err != nil {
		t.Fatalf("ReapStaleSessions returned error: %v", err)
	}
	if result.ExpiredPendingSessions != 1 || result.StaleActiveSessions != 1 {
		t.Fatalf("session counts = %+v, want one expired pending and one stale active", result)
	}
	if len(result.StoppedRuntimeIDs) != 1 || result.StoppedRuntimeIDs[0] != runtimeID {
		t.Fatalf("stopped runtime ids = %v, want [%s]", result.StoppedRuntimeIDs, runtimeID)
	}
	if result.RevokedRuntimeCapabilities != 6 || result.PrunedRuntimeCapabilityNonces != 4 {
		t.Fatalf("capability cleanup = %+v, want 6 revoked capabilities and 4 pruned nonces", result)
	}
	if result.PrunedBlobKeyHandoffs != 5 {
		t.Fatalf("pruned blob-key handoffs = %d, want 5", result.PrunedBlobKeyHandoffs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestIdleRuntimeReaperUsesNewestRuntimeOrSessionActivity(t *testing.T) {
	const want = "GREATEST(COALESCE(ls.last_session_at, r.started_at, r.created_at), COALESCE(r.started_at, r.created_at))"
	if idleRuntimeLastActivitySQL != want {
		t.Fatalf("idle runtime last-activity expression = %q, want %q", idleRuntimeLastActivitySQL, want)
	}
	if strings.Contains(idleRuntimeLastActivitySQL, "r.updated_at") {
		t.Fatal("idle runtime activity must not use node-refreshed runtimes.updated_at")
	}
}

func TestReapStaleSessionsStopsRuntimeAtTrialTimeQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	st := &Store{db: db}
	runtimeID := "55555555-5555-5555-5555-555555555555"

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("UPDATE sessions").
		WithArgs("120 seconds").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("WITH usage AS").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(runtimeID))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(runtimeID, "trial runtime time quota reached").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(
			runtimeID,
			"system",
			"warn",
			"Runtime stop queued because the trial runtime time quota was reached.",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("WITH latest_session").
		WithArgs("120 seconds", "180 seconds").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WithArgs(runtimeID).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("UPDATE runtime_capabilities AS c").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec("UPDATE runtime_capabilities").
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectExec("DELETE FROM runtime_capability_nonces").
		WillReturnResult(sqlmock.NewResult(0, 5))
	mock.ExpectExec("DELETE FROM runtime_blob_key_handoffs").
		WillReturnResult(sqlmock.NewResult(0, 6))
	mock.ExpectCommit()

	result, err := st.ReapStaleSessions(context.Background(), 2*time.Minute, 3*time.Minute)
	if err != nil {
		t.Fatalf("ReapStaleSessions returned error: %v", err)
	}
	if result.ExpiredPendingSessions != 0 || result.StaleActiveSessions != 0 {
		t.Fatalf("session counts = %+v, want no expired or stale sessions", result)
	}
	if len(result.StoppedRuntimeIDs) != 1 || result.StoppedRuntimeIDs[0] != runtimeID {
		t.Fatalf("stopped runtime ids = %v, want [%s]", result.StoppedRuntimeIDs, runtimeID)
	}
	if result.RevokedRuntimeCapabilities != 9 || result.PrunedRuntimeCapabilityNonces != 5 {
		t.Fatalf("capability cleanup = %+v, want 9 revoked capabilities and 5 pruned nonces", result)
	}
	if result.PrunedBlobKeyHandoffs != 6 {
		t.Fatalf("pruned blob-key handoffs = %d, want 6", result.PrunedBlobKeyHandoffs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func runtimeRows(now time.Time, viewerPort any) *sqlmock.Rows {
	return sqlmock.NewRows(runtimeColumnNames()).AddRow(runtimeRowValues(
		now,
		"33333333-3333-3333-3333-333333333333",
		"11111111-1111-1111-1111-111111111111",
		"stopped",
		"stopped",
		"offline",
		nil,
		viewerPort,
	)...)
}

func runtimeStateRows(
	now time.Time,
	runtimeID string,
	accountID string,
	status string,
	desiredState string,
	connectionStatus string,
	hostID any,
	viewerPort any,
) *sqlmock.Rows {
	return sqlmock.NewRows(runtimeColumnNames()).AddRow(runtimeRowValues(
		now,
		runtimeID,
		accountID,
		status,
		desiredState,
		connectionStatus,
		hostID,
		viewerPort,
	)...)
}

func runtimeRowsWithBlob(
	now time.Time,
	runtimeID string,
	accountID string,
	status string,
	desiredState string,
	connectionStatus string,
	hostID any,
	viewerPort any,
	blobStoreKind any,
	blobManifestJSON any,
	blobHostID any,
) *sqlmock.Rows {
	values := runtimeRowValues(
		now,
		runtimeID,
		accountID,
		status,
		desiredState,
		connectionStatus,
		hostID,
		viewerPort,
	)
	values[19] = blobStoreKind
	values[20] = blobManifestJSON
	values[21] = blobHostID
	return sqlmock.NewRows(runtimeColumnNames()).AddRow(values...)
}

func runtimeStateListRows() *sqlmock.Rows {
	columns := append(runtimeColumnNames(), "current_device_session_id", "has_active_session")
	return sqlmock.NewRows(columns)
}

func runtimeStateListRowValues(
	now time.Time,
	runtimeID string,
	accountID string,
	status string,
	desiredState string,
	connectionStatus string,
	hostID any,
	viewerPort any,
	currentDeviceSessionID any,
	hasActiveSession bool,
) []driver.Value {
	values := runtimeRowValues(
		now,
		runtimeID,
		accountID,
		status,
		desiredState,
		connectionStatus,
		hostID,
		viewerPort,
	)
	values = append(values, currentDeviceSessionID, hasActiveSession)
	return values
}

func runtimeStartRows(now time.Time, runtimeID, accountID, hostID string, personaVersion int, viewerPort int) *sqlmock.Rows {
	return sqlmock.NewRows(runtimeColumnNames()).AddRow(
		runtimeID,
		accountID,
		"Primary runtime",
		"starting",
		"running",
		"connecting",
		hostID,
		personaVersion,
		nil,
		defaultAndroidImage,
		"android-14",
		720,
		1600,
		320,
		true,
		"disabled",
		"upload-only",
		true,
		7,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		viewerPort,
		false,
		false,
		int64(1),
		nil,
		nil,
		now,
		now,
	)
}

func sessionStateRows(
	now time.Time,
	sessionID string,
	runtimeID string,
	deviceID string,
	sessionStatus string,
	expiresAt time.Time,
	accountID string,
	runtimeStatus string,
	desiredState string,
	connectionStatus string,
	hostID any,
	viewerPort any,
) *sqlmock.Rows {
	columns := append(
		[]string{
			"id",
			"runtime_id",
			"device_id",
			"status",
			"created_at",
			"updated_at",
			"last_client_heartbeat_at",
			"ended_at",
			"end_reason",
			"expires_at",
		},
		runtimeColumnNames()...,
	)
	values := []driver.Value{
		sessionID,
		runtimeID,
		deviceID,
		sessionStatus,
		now,
		now,
		now,
		nil,
		nil,
		expiresAt,
	}
	values = append(values, runtimeRowValues(
		now,
		runtimeID,
		accountID,
		runtimeStatus,
		desiredState,
		connectionStatus,
		hostID,
		viewerPort,
	)...)
	return sqlmock.NewRows(columns).AddRow(values...)
}

func runtimeRowValues(
	now time.Time,
	runtimeID string,
	accountID string,
	status string,
	desiredState string,
	connectionStatus string,
	hostID any,
	viewerPort any,
) []driver.Value {
	return []driver.Value{
		runtimeID,
		accountID,
		"Primary runtime",
		status,
		desiredState,
		connectionStatus,
		hostID,
		1,
		nil,
		defaultAndroidImage,
		"android-14",
		720,
		1600,
		320,
		true,
		"disabled",
		"upload-only",
		true,
		7,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		viewerPort,
		false,
		false,
		int64(1),
		nil,
		nil,
		now,
		now,
	}
}

func hostRows(now time.Time, hostID, publicKey string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"name",
		"advertise_addr",
		"relay_port",
		"docker_socket",
		"binder",
		"audio_streaming",
		"file_import",
		"camera_passthrough",
		"camera_slots",
		"public_key",
		"blob_store_kind",
		"storage_preflight_kind",
		"storage_preflight_status",
		"storage_preflight_json",
		"storage_preflight_at",
		"storage_wallet_address",
		"created_at",
		"updated_at",
		"last_heartbeat_at",
	}).AddRow(
		hostID,
		hostID,
		"virtnoded",
		8090,
		true,
		true,
		true,
		true,
		false,
		0,
		publicKey,
		"local-disk",
		nil,
		nil,
		nil,
		nil,
		nil,
		now,
		now,
		now,
	)
}

func entitlementRows(accountID string, now time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"account_id",
		"source",
		"status",
		"runtime_limit",
		"active_runtime_limit",
		"runtime_starts_per_day",
		"storage_bytes_limit",
		"trial_runtime_seconds",
		"expires_at",
		"created_at",
		"updated_at",
	}).AddRow(
		accountID,
		"trial",
		"active",
		3,
		1,
		10,
		int64(1073741824),
		3600,
		nil,
		now,
		now,
	)
}

func expectTrialTimeAvailable(mock sqlmock.Sqlmock, accountID string, used int64) {
	mock.ExpectQuery("SELECT source, trial_runtime_seconds").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"source", "trial_runtime_seconds"}).AddRow("trial", 3600))
	mock.ExpectQuery("FROM sessions s").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"seconds"}).AddRow(used))
}

func runtimeColumnNames() []string {
	return []string{
		"id",
		"account_id",
		"name",
		"status",
		"desired_state",
		"connection_status",
		"host_id",
		"persona_version",
		"active_persona_json",
		"android_image",
		"android_version",
		"width_px",
		"height_px",
		"density_dpi",
		"audio_enabled",
		"camera_mode",
		"file_mode",
		"blob_auto_snapshot",
		"blob_retain_days",
		"blob_store_kind",
		"blob_manifest_json",
		"blob_host_id",
		"blob_last_snapshot_at",
		"started_at",
		"load_average",
		"container_name",
		"adb_port",
		"viewer_port",
		"wipe_requested",
		"cleanup_pending",
		"operation_generation",
		"last_error",
		"deleted_at",
		"created_at",
		"updated_at",
	}
}
