package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresRuntimeInactivityPeriodsIntegration(t *testing.T) {
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

	accountID, deviceID := uuid.NewString(), uuid.NewString()
	if _, err := st.BootstrapAccountWithIdentity(ctx, accountID, deviceID, "Inactivity QA", integrationNodePublicKey(t), CreateRuntimeInput{}); err != nil {
		t.Fatalf("bootstrap account: %v", err)
	}
	t.Cleanup(func() { _ = st.DeleteAccount(context.Background(), accountID) })
	if _, err := st.db.ExecContext(ctx, `UPDATE account_entitlements SET runtime_limit = 5 WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "Odd period", IdleTimeoutSeconds: 1234}); !errors.Is(err, ErrRuntimeIdleTimeout) {
		t.Fatalf("an unoffered period: err=%v, want %v", err, ErrRuntimeIdleTimeout)
	}
	halfHour, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "Half hour"})
	if err != nil || halfHour.IdleTimeoutSeconds != DefaultRuntimeIdleTimeout {
		t.Fatalf("default period = %d err=%v, want %d", halfHour.IdleTimeoutSeconds, err, DefaultRuntimeIdleTimeout)
	}
	hour, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "Hour", IdleTimeoutSeconds: 3600})
	if err != nil || hour.IdleTimeoutSeconds != 3600 {
		t.Fatalf("one-hour period = %d err=%v", hour.IdleTimeoutSeconds, err)
	}
	viewed, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "Viewed"})
	if err != nil {
		t.Fatal(err)
	}

	// All three run; the viewer of the first two closed 40 minutes ago, and
	// the third's viewer is open now.
	for _, id := range []string{halfHour.ID, hour.ID, viewed.ID} {
		if _, err := st.db.ExecContext(ctx,
			`UPDATE runtimes SET desired_state = 'running', status = 'running', started_at = NOW() - INTERVAL '2 hours' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{halfHour.ID, hour.ID} {
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO sessions (id, runtime_id, device_id, status, relay_token, created_at, updated_at, last_client_heartbeat_at, expires_at)
			 VALUES ($1, $2, $3, 'stale', 'qa', NOW() - INTERVAL '1 hour', NOW() - INTERVAL '40 minutes', NOW() - INTERVAL '40 minutes', NOW() - INTERVAL '1 minute')`,
			uuid.NewString(), id, deviceID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO sessions (id, runtime_id, device_id, status, relay_token, last_client_heartbeat_at, expires_at)
		 VALUES ($1, $2, $3, 'active', 'qa', NOW(), NOW() + INTERVAL '1 hour')`,
		uuid.NewString(), viewed.ID, deviceID); err != nil {
		t.Fatal(err)
	}

	// The countdown: stopped 30 or 60 minutes after the viewer closed, none
	// while a viewer is open.
	for _, check := range []struct {
		id    string
		until time.Duration
	}{{halfHour.ID, -10 * time.Minute}, {hour.ID, 20 * time.Minute}} {
		state, err := st.GetRuntimeState(ctx, accountID, deviceID, check.id)
		if err != nil || state.IdleStopAt == nil {
			t.Fatalf("state of %s = %+v err=%v, want a stop time", check.id, state, err)
		}
		if got := time.Until(*state.IdleStopAt); got < check.until-time.Minute || got > check.until+time.Minute {
			t.Fatalf("%s stops in %s, want about %s", check.id, got.Round(time.Second), check.until)
		}
	}
	if state, err := st.GetRuntimeState(ctx, accountID, deviceID, viewed.ID); err != nil || state.IdleStopAt != nil {
		t.Fatalf("viewed runtime stop time = %v err=%v, want none while its viewer is open", state.IdleStopAt, err)
	}
	states, err := st.ListRuntimeStates(ctx, accountID, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.Runtime.ID == hour.ID && state.IdleStopAt == nil {
			t.Fatal("the runtime list has no stop time for an idle runtime")
		}
	}

	// Only the runtime past its own period stops.
	result, err := st.ReapStaleSessions(ctx, 2*time.Minute)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if !slices.Contains(result.StoppedRuntimeIDs, halfHour.ID) || slices.Contains(result.StoppedRuntimeIDs, hour.ID) ||
		slices.Contains(result.StoppedRuntimeIDs, viewed.ID) {
		t.Fatalf("stopped %v, want only %s", result.StoppedRuntimeIDs, halfHour.ID)
	}

	// Shortening the period applies at once.
	half := DefaultRuntimeIdleTimeout
	if updated, err := st.UpdateRuntimeSettings(ctx, accountID, hour.ID, UpdateRuntimeInput{IdleTimeoutSeconds: &half}); err != nil || updated.IdleTimeoutSeconds != half {
		t.Fatalf("shorten period: %+v err=%v", updated.IdleTimeoutSeconds, err)
	}
	odd := 99
	if _, err := st.UpdateRuntimeSettings(ctx, accountID, hour.ID, UpdateRuntimeInput{IdleTimeoutSeconds: &odd}); !errors.Is(err, ErrRuntimeIdleTimeout) {
		t.Fatalf("an unoffered period on update: err=%v", err)
	}
	result, err = st.ReapStaleSessions(ctx, 2*time.Minute)
	if err != nil || !slices.Contains(result.StoppedRuntimeIDs, hour.ID) {
		t.Fatalf("after shortening: stopped %v err=%v, want %s", result.StoppedRuntimeIDs, err, hour.ID)
	}
}
