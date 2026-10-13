package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// ChargeResult is what one charging pass did.
type ChargeResult struct {
	RuntimeCharges    int
	StorageCharges    int
	StoppedRuntimeIDs []string
}

// ChargeUsage charges paid accounts for runtime time since the last pass and
// for each day's storage, then queues stops for runtimes of accounts whose
// balance has run out. Runtime time accumulates into one ledger entry per
// runtime and UTC hour, storage into one per account and UTC day, so passing
// again, or twice at once, never charges anything twice.
func (s *Store) ChargeUsage(ctx context.Context) (ChargeResult, error) {
	var result ChargeResult
	if !s.pricing.Enabled() {
		return result, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	if result.RuntimeCharges, err = chargeRuntimeTimeTX(ctx, tx, s.pricing.RuntimeHourMicro); err != nil {
		return result, fmt.Errorf("charge runtime time: %w", err)
	}
	if result.StorageCharges, err = chargeStorageTX(ctx, tx, s.pricing.StorageGiBMonthMicro); err != nil {
		return result, fmt.Errorf("charge storage: %w", err)
	}
	if result.StoppedRuntimeIDs, err = stopRuntimesWithoutBalanceTX(ctx, tx); err != nil {
		return result, fmt.Errorf("stop runtimes without balance: %w", err)
	}
	return result, tx.Commit()
}

// chargeRuntimeTimeTX charges each running runtime of a paid account for the
// time since it was last charged or started. A runtime that is desired running
// but down (in error or on a lost node) is not charged for that time.
func chargeRuntimeTimeTX(ctx context.Context, tx *sql.Tx, hourMicro int64) (int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT r.id::text, r.account_id::text, r.status,
		        GREATEST(COALESCE(r.billed_through, r.started_at), r.started_at), NOW()
		   FROM runtimes r
		   JOIN account_entitlements e ON e.account_id = r.account_id
		  WHERE e.source = 'paid'
		    AND r.deleted_at IS NULL
		    AND r.desired_state = 'running'
		    AND r.started_at IS NOT NULL
		    FOR UPDATE OF r SKIP LOCKED`,
	)
	if err != nil {
		return 0, err
	}
	type due struct {
		runtimeID, accountID, status string
		since, now                   time.Time
	}
	var runtimes []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.runtimeID, &d.accountID, &d.status, &d.since, &d.now); err != nil {
			rows.Close()
			return 0, err
		}
		runtimes = append(runtimes, d)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	charges := 0
	for _, d := range runtimes {
		if d.status == "starting" || d.status == "running" {
			for _, part := range splitByHour(d.since, d.now) {
				amount := mulDivRound(part.duration.Microseconds(), hourMicro, time.Hour.Microseconds())
				if amount <= 0 {
					continue
				}
				reference := "runtime:" + d.runtimeID + ":" + part.start.Format("2006-01-02T15")
				if err := addUsageChargeTX(ctx, tx, d.accountID, "runtime", reference, amount); err != nil {
					return charges, err
				}
				charges++
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runtimes SET billed_through = $2 WHERE id = $1`,
			d.runtimeID, d.now,
		); err != nil {
			return charges, err
		}
	}
	return charges, nil
}

// chargeStorageTX charges each paid account once per UTC day for the
// encrypted runtime data it stores at that moment.
func chargeStorageTX(ctx context.Context, tx *sql.Tx, gibMonthMicro int64) (int, error) {
	if gibMonthMicro <= 0 {
		return 0, nil
	}
	var today string
	if err := tx.QueryRowContext(ctx, `SELECT to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD')`).Scan(&today); err != nil {
		return 0, err
	}
	reference := "storage:" + today
	accountIDs, err := queryStringColumnTX(ctx, tx,
		`SELECT e.account_id::text
		   FROM account_entitlements e
		   JOIN accounts a ON a.id = e.account_id
		  WHERE e.source = 'paid'
		    AND a.deleted_at IS NULL
		    AND NOT EXISTS (
		        SELECT 1 FROM account_ledger l
		         WHERE l.account_id = e.account_id AND l.kind = 'storage' AND l.reference = $1
		    )`,
		reference,
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
		amount := mulDivRound(used, gibMonthMicro, (1<<30)*30)
		if amount <= 0 {
			continue
		}
		if err := addUsageChargeTX(ctx, tx, accountID, "storage", reference, amount); err != nil {
			return charges, err
		}
		charges++
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

// stopRuntimesWithoutBalanceTX queues a stop for every running runtime of a
// paid account whose balance is zero or less. Their stored data is kept.
func stopRuntimesWithoutBalanceTX(ctx context.Context, tx *sql.Tx) ([]string, error) {
	runtimeIDs, err := queryStringColumnTX(ctx, tx,
		`WITH candidates AS (
		     SELECT r.id
		       FROM runtimes r
		       JOIN account_entitlements e ON e.account_id = r.account_id
		      WHERE e.source = 'paid'
		        AND r.deleted_at IS NULL
		        AND r.desired_state = 'running'
		        AND (SELECT COALESCE(SUM(l.amount_micro), 0) FROM account_ledger l WHERE l.account_id = r.account_id) <= 0
		        FOR UPDATE OF r SKIP LOCKED
		 )
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
		if err := closeRuntimeSessionsTX(ctx, tx, runtimeID, "USDT balance used up"); err != nil {
			return nil, err
		}
		if err := appendRuntimeLogTX(ctx, tx, runtimeID, "system", "warn", "Runtime stop queued because the account's USDT balance is used up."); err != nil {
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
