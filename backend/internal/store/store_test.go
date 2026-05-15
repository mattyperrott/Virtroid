package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

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
	mock.ExpectQuery("WITH revoked_sessions").
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
		WithArgs(runtimeID, "host-1", sql.NullString{String: lastError, Valid: true}).
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
		HostID:    "host-1",
		Deleted:   true,
		LastError: &lastError,
	}); err != nil {
		t.Fatalf("UpdateRuntimeObservation returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
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
	if !summary.CanCreateRuntime || !summary.CanStartRuntime {
		t.Fatalf("trial summary blocked create=%v start=%v; want both allowed", summary.CanCreateRuntime, summary.CanStartRuntime)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestStartRuntimeRotatesPersonaForStoppedRuntime(t *testing.T) {
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
	mock.ExpectQuery("SELECT host_id, viewer_port, desired_state FROM runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"host_id", "viewer_port", "desired_state"}).
			AddRow(hostID, 46000, "stopped"))
	mock.ExpectQuery("SELECT account_id, source, status").
		WithArgs(accountID).
		WillReturnRows(entitlementRows(accountID, now))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM runtimes").
		WithArgs(accountID, runtimeID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM runtime_start_events").
		WithArgs(accountID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT id FROM hosts").
		WithArgs(hostID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(hostID))
	mock.ExpectQuery("UPDATE runtimes").
		WithArgs(accountID, runtimeID, hostID, sql.NullInt32{Int32: 46000, Valid: true}, true).
		WillReturnRows(runtimeStartRows(now, runtimeID, accountID, hostID, 2, 46000))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs(runtimeID, "user", "info", "Runtime start requested on host host-1. persona_version=2.").
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
	deviceID := "22222222-2222-2222-2222-222222222222"
	runtimeID := "44444444-4444-4444-4444-444444444444"

	mock.ExpectQuery("d.revoked_at IS NULL").
		WithArgs(runtimeID, deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "desired_state", "connection_status"}).
			AddRow("running", "running", "online"))
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
		viewerPort,
		false,
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
		viewerPort,
		false,
		nil,
		nil,
		now,
		now,
	}
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
		"blob_last_snapshot_at",
		"started_at",
		"load_average",
		"container_name",
		"adb_port",
		"viewer_port",
		"wipe_requested",
		"last_error",
		"deleted_at",
		"created_at",
		"updated_at",
	}
}
