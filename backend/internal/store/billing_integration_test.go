package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresUsageCharges(t *testing.T) {
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

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := st.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	newRunningRuntime := func() (string, string) {
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
		runtime, err := st.CreateRuntime(ctx, accountID, CreateRuntimeInput{Name: "CI billing runtime", FileMode: "upload-only"})
		if err != nil {
			t.Fatalf("create runtime: %v", err)
		}
		// Running for the last 30 minutes, with 64 MiB of stored data.
		exec(`UPDATE runtimes
		         SET desired_state = 'running', status = 'running',
		             started_at = NOW() - INTERVAL '30 minutes',
		             blob_store_kind = 'local-disk',
		             blob_manifest_json = $2
		       WHERE id = $1`,
			runtime.ID,
			`{"version":3,"snapshot_id":"billing","generation":1,"total_bytes":67108864,"chunks":[{"size":16777216},{"size":16777216},{"size":16777216},{"size":16777216}]}`,
		)
		return accountID, runtime.ID
	}
	deposit := func(accountID string, amount int64) {
		t.Helper()
		wallet, err := st.AccountWallet(ctx, accountID)
		if err != nil {
			t.Fatalf("open wallet: %v", err)
		}
		txID := make([]byte, 32)
		_, _ = rand.Read(txID)
		if credited, err := st.CreditDeposit(ctx, DepositCredit{
			TxID: hex.EncodeToString(txID), Address: "T-test-" + accountID, Index: wallet.DepositIndex,
			AmountMicro: amount, BlockTime: time.Now(),
		}); err != nil || !credited {
			t.Fatalf("credit deposit = %v, %v", credited, err)
		}
	}
	charged := func(accountID, kind string) int64 {
		t.Helper()
		var total int64
		if err := st.db.QueryRowContext(ctx,
			`SELECT COALESCE(-SUM(amount_micro), 0) FROM account_ledger WHERE account_id = $1 AND kind = $2`,
			accountID, kind,
		).Scan(&total); err != nil {
			t.Fatalf("read charges: %v", err)
		}
		return total
	}
	charge := func() ChargeResult {
		t.Helper()
		result, err := st.ChargeUsage(ctx)
		if err != nil {
			t.Fatalf("charge usage: %v", err)
		}
		return result
	}
	near := func(got, want, tolerance int64) bool { return got >= want-tolerance && got <= want+tolerance }

	trialAccount, _ := newRunningRuntime()
	paidAccount, paidRuntime := newRunningRuntime()

	// Charging is off until prices are set.
	deposit(paidAccount, 1_000_000)
	if result := charge(); result.RuntimeCharges != 0 || result.StorageCharges != 0 {
		t.Fatalf("charged without prices: %+v", result)
	}

	// 1 USDT per runtime hour is 1000 per second; 64 MiB at this price is
	// 64000 per day.
	st.SetPricing(Pricing{RuntimeHourMicro: 3_600_000, StorageGiBMonthMicro: 30 * 1024 * 1000})
	summary, err := st.GetAccountEntitlementSummary(ctx, paidAccount)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Source != "paid" || summary.BalanceMicro == nil || *summary.BalanceMicro != 1_000_000 ||
		summary.StartRuntimeBlockedCode == RuntimeBalanceInsufficientCode {
		t.Fatalf("paid summary = %+v", summary)
	}

	// The deposit ended the trial and started charging from then, not from
	// when the runtime started half an hour ago.
	charge()
	if got := charged(paidAccount, "runtime"); got > 5_000 {
		t.Fatalf("runtime time before the deposit charged: %d", got)
	}
	exec(`UPDATE runtimes SET billed_through = NOW() - INTERVAL '5 minutes' WHERE id = $1`, paidRuntime)
	before := charged(paidAccount, "runtime")
	charge()
	if got := charged(paidAccount, "runtime") - before; !near(got, 300_000, 5_000) {
		t.Fatalf("5 minutes charged %d, want about 300000", got)
	}
	if got := charged(paidAccount, "storage"); got != 64_000 {
		t.Fatalf("storage charged %d, want 64000", got)
	}
	// Charging again moves on from where the last pass stopped and charges
	// storage once a day, into one entry per month.
	before = charged(paidAccount, "runtime")
	charge()
	if got := charged(paidAccount, "runtime") - before; got > 5_000 {
		t.Fatalf("second pass charged %d again", got)
	}
	if got := charged(paidAccount, "storage"); got != 64_000 {
		t.Fatalf("storage charged twice: %d", got)
	}
	var storageEntries int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_ledger WHERE account_id = $1 AND kind = 'storage' AND reference = 'storage:' || to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM')`, paidAccount,
	).Scan(&storageEntries); err != nil || storageEntries != 1 {
		t.Fatalf("storage ledger entries this month = %d err=%v, want 1", storageEntries, err)
	}
	// The next day's charge adds to the same entry.
	exec(`UPDATE accounts SET storage_billed_on = storage_billed_on - 1 WHERE id = $1`, paidAccount)
	charge()
	if got := charged(paidAccount, "storage"); got != 128_000 {
		t.Fatalf("storage after a second day = %d, want 128000", got)
	}
	var hourEntries int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_ledger WHERE account_id = $1 AND kind = 'runtime'`, paidAccount,
	).Scan(&hourEntries); err != nil || hourEntries > 2 {
		t.Fatalf("runtime ledger entries = %d err=%v, want one per hour", hourEntries, err)
	}

	// Trial accounts are never charged.
	if got := charged(trialAccount, "runtime") + charged(trialAccount, "storage"); got != 0 {
		t.Fatalf("trial account charged %d", got)
	}

	// Another 30 minutes use up the balance: the runtime is stopped and cannot
	// start again, but its data stays.
	exec(`UPDATE runtimes SET billed_through = NOW() - INTERVAL '30 minutes' WHERE id = $1`, paidRuntime)
	result := charge()
	if !slices.Contains(result.StoppedRuntimeIDs, paidRuntime) {
		t.Fatalf("runtime without balance not stopped: %+v", result)
	}
	var desired, manifest string
	if err := st.db.QueryRowContext(ctx,
		`SELECT desired_state, COALESCE(blob_manifest_json, '') FROM runtimes WHERE id = $1`, paidRuntime,
	).Scan(&desired, &manifest); err != nil || desired != "stopped" || manifest == "" {
		t.Fatalf("stopped runtime desired=%q manifest kept=%t err=%v", desired, manifest != "", err)
	}
	summary, err = st.GetAccountEntitlementSummary(ctx, paidAccount)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.CanStartRuntime || summary.StartRuntimeBlockedCode != RuntimeBalanceInsufficientCode {
		t.Fatalf("summary without balance = %+v", summary)
	}
	if _, err := st.StartRuntime(ctx, paidAccount, paidRuntime); !errors.Is(err, ErrRuntimeBalance) {
		t.Fatalf("start without balance error = %v, want %v", err, ErrRuntimeBalance)
	}

	// Ten minutes of credit (166667 at this price) is enough to start.
	deposit(paidAccount, 1_000_000-*summary.BalanceMicro)
	summary, err = st.GetAccountEntitlementSummary(ctx, paidAccount)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !summary.CanStartRuntime || *summary.BalanceMicro != 1_000_000 {
		t.Fatalf("summary after topping up = %+v", summary)
	}

	// The operator console sees the account, its deposits and the services.
	if err := st.TouchServiceHeartbeat(ctx, "payments"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	payments, err := st.OperatorPayments(ctx)
	if err != nil {
		t.Fatalf("operator payments: %v", err)
	}
	if payments.PaidAccounts < 1 || payments.DepositCount < 2 || payments.PaymentsSeenAt == nil || payments.ChargesTotalMicro <= 0 {
		t.Fatalf("operator payments = %+v", payments)
	}
	var seen bool
	for _, balance := range payments.Accounts {
		if balance.AccountID == paidAccount {
			seen = balance.BalanceMicro == 1_000_000 && balance.Source == "paid" &&
				balance.DepositedMicro-balance.ChargedMicro == balance.BalanceMicro
		}
	}
	if !seen {
		t.Fatalf("paid account missing or wrong in %+v", payments.Accounts)
	}
	fleet, err := st.OperatorVMs(ctx)
	if err != nil {
		t.Fatalf("operator VMs: %v", err)
	}
	if fleet.StoredBytesByKind["local-disk"] < 2*67108864 {
		t.Fatalf("stored bytes = %+v", fleet.StoredBytesByKind)
	}
}
