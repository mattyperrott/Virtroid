package store

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestBootstrapAccountDoesNotReserveViewerPort(t *testing.T) {
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
	mock.ExpectQuery("INSERT INTO runtimes").
		WithArgs(
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			"Primary runtime",
			defaultAndroidImage,
			"android-12",
			720,
			1600,
			320,
			true,
			"disabled",
			"upload-only",
			true,
			7,
		).
		WillReturnRows(runtimeRows(now, nil))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs("33333333-3333-3333-3333-333333333333", "system", "info", "Primary runtime created for new account.").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := st.BootstrapAccount(context.Background(), "Pixel", "public-key", CreateRuntimeInput{})
	if err != nil {
		t.Fatalf("BootstrapAccount returned error: %v", err)
	}
	if result.Runtime.ViewerPort != nil {
		t.Fatalf("bootstrap runtime reserved viewer port %d; want nil until start", *result.Runtime.ViewerPort)
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
	mock.ExpectQuery("INSERT INTO runtimes").
		WithArgs(
			sqlmock.AnyArg(),
			accountID,
			"Primary runtime",
			defaultAndroidImage,
			"android-12",
			720,
			1600,
			320,
			true,
			"disabled",
			"upload-only",
			true,
			7,
		).
		WillReturnRows(runtimeRows(now, nil))
	mock.ExpectExec("INSERT INTO runtime_logs").
		WithArgs("33333333-3333-3333-3333-333333333333", "system", "info", "Primary runtime created for new account.").
		WillReturnResult(sqlmock.NewResult(1, 1))
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
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
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

func runtimeRows(now time.Time, viewerPort any) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
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
		"container_name",
		"adb_port",
		"viewer_port",
		"wipe_requested",
		"last_error",
		"deleted_at",
		"created_at",
		"updated_at",
	}).AddRow(
		"33333333-3333-3333-3333-333333333333",
		"11111111-1111-1111-1111-111111111111",
		"Primary runtime",
		"stopped",
		"stopped",
		"offline",
		nil,
		1,
		nil,
		defaultAndroidImage,
		"android-12",
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
		viewerPort,
		false,
		nil,
		nil,
		now,
		now,
	)
}
