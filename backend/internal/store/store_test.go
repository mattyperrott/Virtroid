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
