package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// OperatorPayments summarises USDT wallets for the operator console.
type OperatorPayments struct {
	WalletAccounts    int
	PaidAccounts      int
	DepositCount      int
	DepositTotalMicro int64
	Deposits24hMicro  int64
	LastDepositAt     *time.Time
	BalancesMicro     int64
	OverdrawnAccounts int
	RuntimeCharges24h int64
	StorageCharges24h int64
	ChargesTotalMicro int64
	PaymentsSeenAt    *time.Time
	RecentDeposits    []OperatorDeposit
	Accounts          []OperatorBalance
}

type OperatorDeposit struct {
	TxID        string
	AccountID   string
	AmountMicro int64
	BlockTime   time.Time
}

type OperatorBalance struct {
	AccountID      string
	Source         string
	BalanceMicro   int64
	DepositedMicro int64
	ChargedMicro   int64
	LastEntryAt    *time.Time
}

// OperatorVM is a runtime VM the provisioner manages.
type OperatorVM struct {
	ID           string
	AccountID    string
	Region       string
	State        string
	Desired      string
	ItemHash     string
	Progress     string
	RetireReason string
	Failure      string
	RequestedAt  time.Time
	ReadyAt      *time.Time
	LastUsedAt   *time.Time
	DeletedAt    *time.Time
}

// OperatorVMs is the runtime VM fleet: VMs that exist or were deleted in the
// last week, and how many VM-hours existed in the last 24 hours.
type OperatorVMs struct {
	VMs               []OperatorVM
	VMHours24h        float64
	ProvisionerSeen   *time.Time
	SiaAccounts       int
	StoredBytesByKind map[string]int64
}

// TouchServiceHeartbeat records that a service reached the control plane.
func (s *Store) TouchServiceHeartbeat(ctx context.Context, service string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO service_heartbeats (service, seen_at) VALUES ($1, NOW())
		 ON CONFLICT (service) DO UPDATE SET seen_at = EXCLUDED.seen_at`,
		strings.TrimSpace(service),
	)
	return err
}

func serviceSeenAt(ctx context.Context, queryer queryRowContexter, service string) (*time.Time, error) {
	var seen sql.NullTime
	err := queryer.QueryRowContext(ctx, `SELECT seen_at FROM service_heartbeats WHERE service = $1`, service).Scan(&seen)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if !seen.Valid {
		return nil, nil
	}
	return &seen.Time, nil
}

// OperatorPayments reads the wallet summary, the latest deposits and the
// largest balances.
func (s *Store) OperatorPayments(ctx context.Context) (OperatorPayments, error) {
	var out OperatorPayments
	var lastDeposit sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM accounts WHERE deposit_index IS NOT NULL AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM account_entitlements e JOIN accounts a ON a.id = e.account_id
			  WHERE e.source = 'paid' AND a.deleted_at IS NULL),
			(SELECT COUNT(*) FROM usdt_deposits),
			(SELECT COALESCE(SUM(amount_micro), 0) FROM usdt_deposits),
			(SELECT COALESCE(SUM(amount_micro), 0) FROM usdt_deposits WHERE credited_at > NOW() - INTERVAL '24 hours'),
			(SELECT MAX(block_time) FROM usdt_deposits),
			(SELECT COALESCE(SUM(amount_micro), 0) FROM account_ledger),
			(SELECT COUNT(*) FROM (SELECT account_id FROM account_ledger GROUP BY account_id HAVING SUM(amount_micro) < 0) overdrawn),
			(SELECT COALESCE(-SUM(amount_micro), 0) FROM account_ledger WHERE kind = 'runtime' AND created_at > NOW() - INTERVAL '24 hours'),
			(SELECT COALESCE(-SUM(amount_micro), 0) FROM account_ledger WHERE kind = 'storage' AND created_at > NOW() - INTERVAL '24 hours'),
			(SELECT COALESCE(-SUM(amount_micro), 0) FROM account_ledger WHERE kind IN ('runtime', 'storage'))
	`).Scan(
		&out.WalletAccounts, &out.PaidAccounts, &out.DepositCount, &out.DepositTotalMicro, &out.Deposits24hMicro,
		&lastDeposit, &out.BalancesMicro, &out.OverdrawnAccounts, &out.RuntimeCharges24h, &out.StorageCharges24h,
		&out.ChargesTotalMicro,
	)
	if err != nil {
		return out, err
	}
	if lastDeposit.Valid {
		out.LastDepositAt = &lastDeposit.Time
	}
	if out.PaymentsSeenAt, err = serviceSeenAt(ctx, s.db, "payments"); err != nil {
		return out, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT tx_id, account_id::text, amount_micro, block_time
		  FROM usdt_deposits ORDER BY block_time DESC LIMIT 20`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var deposit OperatorDeposit
		if err := rows.Scan(&deposit.TxID, &deposit.AccountID, &deposit.AmountMicro, &deposit.BlockTime); err != nil {
			rows.Close()
			return out, err
		}
		out.RecentDeposits = append(out.RecentDeposits, deposit)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT l.account_id::text, COALESCE(e.source, 'none'),
		       SUM(l.amount_micro),
		       COALESCE(SUM(l.amount_micro) FILTER (WHERE l.kind = 'deposit'), 0),
		       COALESCE(-SUM(l.amount_micro) FILTER (WHERE l.kind IN ('runtime', 'storage')), 0),
		       MAX(l.created_at)
		  FROM account_ledger l
		  LEFT JOIN account_entitlements e ON e.account_id = l.account_id
		 GROUP BY l.account_id, e.source
		 ORDER BY MAX(l.created_at) DESC
		 LIMIT 50`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var balance OperatorBalance
		var last sql.NullTime
		if err := rows.Scan(&balance.AccountID, &balance.Source, &balance.BalanceMicro, &balance.DepositedMicro, &balance.ChargedMicro, &last); err != nil {
			return out, err
		}
		if last.Valid {
			balance.LastEntryAt = &last.Time
		}
		out.Accounts = append(out.Accounts, balance)
	}
	return out, rows.Err()
}

// OperatorVMs reads the runtime VM fleet and where runtime data is stored.
func (s *Store) OperatorVMs(ctx context.Context) (OperatorVMs, error) {
	out := OperatorVMs{StoredBytesByKind: map[string]int64{}}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(account_id::text, ''), region, state, desired, COALESCE(item_hash, ''),
		       COALESCE(progress, ''), COALESCE(retire_reason, ''), COALESCE(failure, ''),
		       requested_at, ready_at, last_used_at, deleted_at
		  FROM provisioned_vms
		 WHERE deleted_at IS NULL OR deleted_at > NOW() - INTERVAL '7 days'
		 ORDER BY requested_at DESC
		 LIMIT 100`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var vm OperatorVM
		var ready, used, deleted sql.NullTime
		if err := rows.Scan(&vm.ID, &vm.AccountID, &vm.Region, &vm.State, &vm.Desired, &vm.ItemHash,
			&vm.Progress, &vm.RetireReason, &vm.Failure, &vm.RequestedAt, &ready, &used, &deleted); err != nil {
			rows.Close()
			return out, err
		}
		vm.ReadyAt = nullTimePtr(ready)
		vm.LastUsedAt = nullTimePtr(used)
		vm.DeletedAt = nullTimePtr(deleted)
		out.VMs = append(out.VMs, vm)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	// A VM is paid for from its request until it is deleted.
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(EXTRACT(EPOCH FROM (
		           LEAST(COALESCE(deleted_at, NOW()), NOW()) - GREATEST(requested_at, NOW() - INTERVAL '24 hours')
		       ))), 0) / 3600.0
		  FROM provisioned_vms
		 WHERE requested_at < NOW()
		   AND COALESCE(deleted_at, NOW()) > NOW() - INTERVAL '24 hours'`,
	).Scan(&out.VMHours24h); err != nil {
		return out, err
	}
	if out.ProvisionerSeen, err = serviceSeenAt(ctx, s.db, "provisioner"); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM accounts WHERE sia_registered_at IS NOT NULL AND sia_released_at IS NULL`,
	).Scan(&out.SiaAccounts); err != nil {
		return out, err
	}

	manifests, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(TRIM(blob_store_kind), ''), 'local-disk'), blob_manifest_json
		  FROM runtimes
		 WHERE deleted_at IS NULL AND NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NOT NULL`)
	if err != nil {
		return out, err
	}
	defer manifests.Close()
	for manifests.Next() {
		var kind, raw string
		if err := manifests.Scan(&kind, &raw); err != nil {
			return out, err
		}
		size, err := storedBlobManifestBytes(raw)
		if err != nil {
			continue
		}
		out.StoredBytesByKind[kind] += size
	}
	return out, manifests.Err()
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
