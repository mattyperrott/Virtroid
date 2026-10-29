package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A stream that already sent the oldest deliveries gets the newer ones next,
// even though the phone never acked the old ones.
func TestPostgresPendingNotificationDeliveriesSkipWhatTheStreamSent(t *testing.T) {
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
	accountID, deviceID := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = st.db.ExecContext(cleanupCtx, `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	if _, err := st.BootstrapAccountWithIdentity(
		ctx, accountID, deviceID, "CI device", integrationNodePublicKey(t), CreateRuntimeInput{},
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
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO device_notification_subscriptions (device_id, account_id, encryption_public_key)
		 VALUES ($1, $2, 'ci-key')`, deviceID, accountID); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	var ids []string
	for i := 0; i < 25; i++ {
		id := uuid.NewString()
		ids = append(ids, id)
		if _, err := st.db.ExecContext(ctx,
			`INSERT INTO runtime_notification_deliveries (id, runtime_id, event_id, device_id, envelope_ciphertext, created_at, expires_at)
			 VALUES ($1, $2, $3, $4, 'opaque', NOW() - make_interval(secs => $5), NOW() + INTERVAL '1 day')`,
			id, runtime.ID, uuid.NewString(), deviceID, 100-i); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	first, err := st.ListPendingNotificationDeliveries(ctx, accountID, deviceID, 20, nil)
	if err != nil || len(first) != 20 || first[0].ID != ids[0] || first[19].ID != ids[19] {
		t.Fatalf("first batch = %d deliveries err=%v, want the oldest 20", len(first), err)
	}
	next, err := st.ListPendingNotificationDeliveries(ctx, accountID, deviceID, 20, ids[:20])
	if err != nil || len(next) != 5 {
		t.Fatalf("next batch = %d deliveries err=%v, want the newest 5", len(next), err)
	}
	for i, delivery := range next {
		if delivery.ID != ids[20+i] {
			t.Fatalf("next batch[%d] = %s, want %s", i, delivery.ID, ids[20+i])
		}
	}
}
