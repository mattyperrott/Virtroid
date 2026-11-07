package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A node retrying a failed stop sends one line every 10 seconds. Runs of the
// same line become one row with a count, a different line in between starts
// a new row, and retention goes by the latest repeat, not the first.
func TestPostgresRuntimeLogRepeatsIntegration(t *testing.T) {
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
	t.Cleanup(func() { _ = st.Close() })
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	accountID := uuid.NewString()
	if _, err := st.BootstrapAccountWithIdentity(
		ctx, accountID, uuid.NewString(), "Runtime log QA", integrationNodePublicKey(t), CreateRuntimeInput{},
	); err != nil {
		t.Fatalf("bootstrap account: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteAccount(context.Background(), accountID) })
	if _, err := st.db.ExecContext(ctx, `UPDATE account_entitlements SET runtime_limit = 5 WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	runtime, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "Log repeats"})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	const failure = `Reconcile failed: snapshot source contains unsupported symlink "data/org.fdroid.fdroid/files/fdroid/index.html"`
	countRows := func(message string) (rows, repeats int) {
		t.Helper()
		if err := st.db.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(repeat_count), 0) FROM runtime_logs WHERE runtime_id = $1 AND message = $2`,
			runtime.ID, message,
		).Scan(&rows, &repeats); err != nil {
			t.Fatal(err)
		}
		return rows, repeats
	}

	for range 3 {
		if err := st.AppendRuntimeLog(ctx, runtime.ID, "node", "error", failure); err != nil {
			t.Fatalf("append the failure: %v", err)
		}
	}
	if rows, repeats := countRows(failure); rows != 1 || repeats != 3 {
		t.Fatalf("three of the same line = %d rows, %d repeats; want 1 row counting 3", rows, repeats)
	}
	logs, err := st.ListRuntimeLogs(ctx, accountID, runtime.ID, 25)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) == 0 || logs[0].Message != failure || logs[0].RepeatCount != 3 || logs[0].LastSeenAt == nil || logs[0].LastSeenAt.Before(logs[0].CreatedAt) {
		t.Fatalf("latest log = %+v, want the failure counted 3 times with a last-seen time", logs[0])
	}

	// The same text at another level, or after a different line, is new.
	if err := st.AppendRuntimeLog(ctx, runtime.ID, "node", "warn", failure); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRuntimeLog(ctx, runtime.ID, "node", "info", "Runtime start requested."); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRuntimeLog(ctx, runtime.ID, "node", "error", failure); err != nil {
		t.Fatal(err)
	}
	if rows, repeats := countRows(failure); rows != 3 || repeats != 5 {
		t.Fatalf("after other lines = %d rows, %d repeats; want 3 rows counting 5", rows, repeats)
	}

	// A line first seen 40 days ago but repeated just now is still current.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE runtime_logs SET created_at = NOW() - INTERVAL '40 days', last_seen_at = NOW()
		  WHERE runtime_id = $1 AND message = $2 AND repeat_count = 3`,
		runtime.ID, failure,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PruneRuntimeLogs(ctx, 30*24*time.Hour); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if rows, _ := countRows(failure); rows != 3 {
		t.Fatalf("pruning removed a line that is still repeating: %d rows left, want 3", rows)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE runtime_logs SET last_seen_at = NOW() - INTERVAL '40 days'
		  WHERE runtime_id = $1 AND message = $2 AND repeat_count = 3`,
		runtime.ID, failure,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PruneRuntimeLogs(ctx, 30*24*time.Hour); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if rows, _ := countRows(failure); rows != 2 {
		t.Fatalf("pruning kept a line last seen 40 days ago: %d rows left, want 2", rows)
	}
}
