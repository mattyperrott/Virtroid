package store

import (
	"context"
	"database/sql"
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
