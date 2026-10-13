package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
)

// ErrRuntimeBalance means a paid account's USDT balance cannot cover starting
// or using a runtime.
var ErrRuntimeBalance = errors.New("the account's USDT balance is too low")

// RuntimeBalanceInsufficientCode is the API code for ErrRuntimeBalance.
const RuntimeBalanceInsufficientCode = "runtime_balance_insufficient"

// Pricing is what paid accounts are charged, in millionths of a USDT. Zero
// prices leave charging off.
type Pricing struct {
	RuntimeHourMicro     int64
	StorageGiBMonthMicro int64
}

// Enabled reports whether paid accounts are charged.
func (p Pricing) Enabled() bool {
	return p.RuntimeHourMicro > 0 || p.StorageGiBMonthMicro > 0
}

// MinimumStartMicro is the balance a paid account needs to start a runtime:
// ten minutes of runtime use.
func (p Pricing) MinimumStartMicro() int64 {
	return p.RuntimeHourMicro / 6
}

// SetPricing sets what paid accounts are charged. Call it before serving.
func (s *Store) SetPricing(pricing Pricing) {
	s.pricing = pricing
}

// UsageResult is what one metering pass did.
type UsageResult struct {
	RuntimeCharges    int
	StorageCharges    int
	TrialSeconds      int64
	StoppedRuntimeIDs []string
}

// MeterUsage runs every minute. It adds the time trial accounts' runtimes ran
// to their trial, charges paid accounts for runtime time and each day's
// storage, and queues stops for runtimes whose trial hour or balance is used
// up. Runtime time is counted from where the last pass stopped, and charges
// accumulate into one ledger entry per runtime and UTC hour and one per
// account and UTC month for storage, so passing again, or twice at once,
// never counts anything twice.
func (s *Store) MeterUsage(ctx context.Context) (UsageResult, error) {
	var result UsageResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	if result.RuntimeCharges, result.TrialSeconds, err = meterRuntimeTimeTX(ctx, tx, s.pricing.RuntimeHourMicro); err != nil {
		return result, fmt.Errorf("meter runtime time: %w", err)
	}
	if s.pricing.Enabled() {
		if result.StorageCharges, err = chargeStorageTX(ctx, tx, s.pricing.StorageGiBMonthMicro); err != nil {
			return result, fmt.Errorf("charge storage: %w", err)
		}
	}
	trialOver, err := queueRuntimeStopsTX(ctx, tx,
		`SELECT r.id
		   FROM runtimes r
		   JOIN account_entitlements e ON e.account_id = r.account_id
		  WHERE e.source = 'trial'
		    AND r.deleted_at IS NULL
		    AND r.desired_state = 'running'
		    AND e.trial_runtime_seconds_used >= e.trial_runtime_seconds
		    FOR UPDATE OF r SKIP LOCKED`,
		"trial runtime time used up",
		"Runtime stop queued because the trial's hour of running time is used up.",
	)
	if err != nil {
		return result, fmt.Errorf("stop runtimes out of trial time: %w", err)
	}
	result.StoppedRuntimeIDs = trialOver
	if s.pricing.Enabled() {
		withoutBalance, err := queueRuntimeStopsTX(ctx, tx,
			`SELECT r.id
			   FROM runtimes r
			   JOIN account_entitlements e ON e.account_id = r.account_id
			  WHERE e.source = 'paid'
			    AND r.deleted_at IS NULL
			    AND r.desired_state = 'running'
			    AND (SELECT COALESCE(SUM(l.amount_micro), 0) FROM account_ledger l WHERE l.account_id = r.account_id) <= 0
			    FOR UPDATE OF r SKIP LOCKED`,
			"USDT balance used up",
			"Runtime stop queued because the account's USDT balance is used up.",
		)
		if err != nil {
			return result, fmt.Errorf("stop runtimes without balance: %w", err)
		}
		result.StoppedRuntimeIDs = append(result.StoppedRuntimeIDs, withoutBalance...)
	}
	return result, tx.Commit()
}

// meterRuntimeTimeTX counts the time each running runtime of a trial or paid
// account ran since the last pass or its start: into the trial for a trial
// account, as a charge for a paid one. Only time the node reports the runtime
// running counts; booting, or being down in error or on a lost node, does not.
func meterRuntimeTimeTX(ctx context.Context, tx *sql.Tx, hourMicro int64) (int, int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT r.id::text, r.account_id::text, e.source, r.status,
		        GREATEST(COALESCE(r.billed_through, r.started_at), r.started_at), NOW()
		   FROM runtimes r
		   JOIN account_entitlements e ON e.account_id = r.account_id
		  WHERE e.source IN ('trial', 'paid')
		    AND r.deleted_at IS NULL
		    AND r.desired_state = 'running'
		    AND r.started_at IS NOT NULL
		    FOR UPDATE OF r SKIP LOCKED`,
	)
	if err != nil {
		return 0, 0, err
	}
	type due struct {
		runtimeID, accountID, source, status string
		since, now                           time.Time
	}
	var runtimes []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.runtimeID, &d.accountID, &d.source, &d.status, &d.since, &d.now); err != nil {
			rows.Close()
			return 0, 0, err
		}
		runtimes = append(runtimes, d)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}

	charges := 0
	var trialSeconds int64
	for _, d := range runtimes {
		if d.status == "running" && d.now.After(d.since) {
			switch d.source {
			case "trial":
				seconds := int64(math.Round(d.now.Sub(d.since).Seconds()))
				if _, err := tx.ExecContext(ctx,
					`UPDATE account_entitlements
					    SET trial_runtime_seconds_used = trial_runtime_seconds_used + $2, updated_at = NOW()
					  WHERE account_id = $1`,
					d.accountID, seconds,
				); err != nil {
					return charges, trialSeconds, err
				}
				trialSeconds += seconds
			case "paid":
				for _, part := range splitByHour(d.since, d.now) {
					amount := mulDivRound(part.duration.Microseconds(), hourMicro, time.Hour.Microseconds())
					if amount <= 0 {
						continue
					}
					reference := "runtime:" + d.runtimeID + ":" + part.start.Format("2006-01-02T15")
					if err := addUsageChargeTX(ctx, tx, d.accountID, "runtime", reference, amount); err != nil {
						return charges, trialSeconds, err
					}
					charges++
				}
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runtimes SET billed_through = $2 WHERE id = $1`,
			d.runtimeID, d.now,
		); err != nil {
			return charges, trialSeconds, err
		}
	}
	return charges, trialSeconds, nil
}

// chargeStorageTX charges each paid account once per UTC day for the
// encrypted runtime data it stores at that moment. A month's daily charges
// add up in one ledger entry.
func chargeStorageTX(ctx context.Context, tx *sql.Tx, gibMonthMicro int64) (int, error) {
	if gibMonthMicro <= 0 {
		return 0, nil
	}
	var month string
	if err := tx.QueryRowContext(ctx, `SELECT to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM')`).Scan(&month); err != nil {
		return 0, err
	}
	accountIDs, err := queryStringColumnTX(ctx, tx,
		`SELECT a.id::text
		   FROM accounts a
		   JOIN account_entitlements e ON e.account_id = a.id
		  WHERE e.source = 'paid'
		    AND a.deleted_at IS NULL
		    AND (a.storage_billed_on IS NULL OR a.storage_billed_on < (NOW() AT TIME ZONE 'UTC')::date)
		    FOR UPDATE OF a SKIP LOCKED`,
	)
	if err != nil {
		return 0, err
	}
	charges := 0
	for _, accountID := range accountIDs {
		used, err := accountStorageBytesUsed(ctx, tx, accountID)
		if err != nil {
			return charges, err
		}
		// A month is taken as 30 days.
		if amount := mulDivRound(used, gibMonthMicro, (1<<30)*30); amount > 0 {
			if err := addUsageChargeTX(ctx, tx, accountID, "storage", "storage:"+month, amount); err != nil {
				return charges, err
			}
			charges++
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE accounts SET storage_billed_on = (NOW() AT TIME ZONE 'UTC')::date WHERE id = $1`,
			accountID,
		); err != nil {
			return charges, err
		}
	}
	return charges, nil
}

func addUsageChargeTX(ctx context.Context, tx *sql.Tx, accountID, kind, reference string, amount int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO account_ledger (account_id, kind, amount_micro, reference)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (account_id, reference) WHERE kind IN ('runtime', 'storage')
		 DO UPDATE SET amount_micro = account_ledger.amount_micro + EXCLUDED.amount_micro`,
		accountID, kind, -amount, reference,
	)
	return err
}

// queueRuntimeStopsTX queues a stop for the runtimes candidates selects, closes
// their sessions and says why in their logs. Their stored data is kept.
func queueRuntimeStopsTX(ctx context.Context, tx *sql.Tx, candidates, sessionReason, logMessage string) ([]string, error) {
	runtimeIDs, err := queryStringColumnTX(ctx, tx,
		`WITH candidates AS (`+candidates+`)
		 UPDATE runtimes r
		    SET status = CASE WHEN r.host_id IS NULL THEN 'stopped' ELSE 'stopping' END,
		        desired_state = 'stopped',
		        operation_generation = r.operation_generation + 1,
		        connection_status = CASE WHEN r.host_id IS NULL THEN 'offline' ELSE 'disconnecting' END,
		        started_at = NULL,
		        last_error = NULL,
		        updated_at = NOW()
		   FROM candidates c
		  WHERE r.id = c.id
		  RETURNING r.id::text`,
	)
	if err != nil {
		return nil, err
	}
	for _, runtimeID := range runtimeIDs {
		if err := closeRuntimeSessionsTX(ctx, tx, runtimeID, sessionReason); err != nil {
			return nil, err
		}
		if err := appendRuntimeLogTX(ctx, tx, runtimeID, "system", "warn", logMessage); err != nil {
			return nil, err
		}
		if _, err := revokeRuntimeCapabilitiesForRuntimeTX(ctx, tx, runtimeID); err != nil {
			return nil, err
		}
	}
	return runtimeIDs, nil
}

// paidBalanceCheck returns ErrRuntimeBalance when a paid account's balance is
// below minimum. Other accounts, and any account while charging is off, pass.
func paidBalanceCheck(ctx context.Context, queryer queryRowContexter, pricing Pricing, accountID, source string, minimum int64) error {
	if !pricing.Enabled() || !strings.EqualFold(strings.TrimSpace(source), "paid") {
		return nil
	}
	balance, err := accountBalance(ctx, queryer, accountID)
	if err != nil {
		return err
	}
	if balance < minimum || balance <= 0 {
		return ErrRuntimeBalance
	}
	return nil
}

func accountBalance(ctx context.Context, queryer queryRowContexter, accountID string) (int64, error) {
	var balance int64
	err := queryer.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_micro), 0) FROM account_ledger WHERE account_id = $1`,
		strings.TrimSpace(accountID),
	).Scan(&balance)
	return balance, err
}

type hourPart struct {
	start    time.Time
	duration time.Duration
}

// splitByHour splits [from, to) at UTC hour boundaries.
func splitByHour(from, to time.Time) []hourPart {
	from, to = from.UTC(), to.UTC()
	var parts []hourPart
	for from.Before(to) {
		hour := from.Truncate(time.Hour)
		end := hour.Add(time.Hour)
		if end.After(to) {
			end = to
		}
		parts = append(parts, hourPart{start: hour, duration: end.Sub(from)})
		from = end
	}
	return parts
}

// mulDivRound returns a*b/c rounded half up, for non-negative a and b and
// positive c, without overflowing on the way.
func mulDivRound(a, b, c int64) int64 {
	if a <= 0 || b <= 0 || c <= 0 {
		return 0
	}
	product := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	product.Add(product, big.NewInt(c/2))
	quotient := product.Quo(product, big.NewInt(c))
	if !quotient.IsInt64() {
		return 0
	}
	return quotient.Int64()
}
