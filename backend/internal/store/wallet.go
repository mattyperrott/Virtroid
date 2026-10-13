package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrDepositAccountNotFound means no live account owns a deposit index.
var ErrDepositAccountNotFound = errors.New("no account owns this deposit address")

// Wallet is an account's USDT balance, in millionths of a USDT, with its
// deposit index and recent history.
type Wallet struct {
	DepositIndex int64
	BalanceMicro int64
	Deposits     []WalletDeposit
	Entries      []LedgerEntry
}

type WalletDeposit struct {
	TxID        string    `json:"tx_id"`
	AmountMicro int64     `json:"amount_micro"`
	BlockTime   time.Time `json:"block_time"`
	CreditedAt  time.Time `json:"credited_at"`
}

type LedgerEntry struct {
	Kind        string    `json:"kind"`
	AmountMicro int64     `json:"amount_micro"`
	Reference   string    `json:"reference"`
	CreatedAt   time.Time `json:"created_at"`
}

// AccountWallet gives the account a deposit index the first time and returns
// its wallet. Opening the wallet also has the payments service check the
// account's address often for a while.
func (s *Store) AccountWallet(ctx context.Context, accountID string) (Wallet, error) {
	accountID = strings.TrimSpace(accountID)
	var wallet Wallet
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wallet, err
	}
	defer tx.Rollback()
	// COALESCE evaluates nextval only for an account without an index.
	err = tx.QueryRowContext(ctx,
		`UPDATE accounts
		    SET deposit_index = COALESCE(deposit_index, nextval('account_deposit_index_seq')),
		        wallet_viewed_at = NOW()
		  WHERE id = $1 AND deleted_at IS NULL
		  RETURNING deposit_index`,
		accountID,
	).Scan(&wallet.DepositIndex)
	if errors.Is(err, sql.ErrNoRows) {
		return wallet, ErrAccountNotFound
	}
	if err != nil {
		return wallet, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount_micro), 0) FROM account_ledger WHERE account_id = $1`,
		accountID,
	).Scan(&wallet.BalanceMicro); err != nil {
		return wallet, err
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT tx_id, amount_micro, block_time, credited_at
		   FROM usdt_deposits WHERE account_id = $1
		  ORDER BY block_time DESC LIMIT 20`,
		accountID,
	)
	if err != nil {
		return wallet, err
	}
	for rows.Next() {
		var deposit WalletDeposit
		if err := rows.Scan(&deposit.TxID, &deposit.AmountMicro, &deposit.BlockTime, &deposit.CreditedAt); err != nil {
			rows.Close()
			return wallet, err
		}
		wallet.Deposits = append(wallet.Deposits, deposit)
	}
	if err := rows.Close(); err != nil {
		return wallet, err
	}

	rows, err = tx.QueryContext(ctx,
		`SELECT kind, amount_micro, reference, created_at
		   FROM account_ledger WHERE account_id = $1
		  ORDER BY id DESC LIMIT 50`,
		accountID,
	)
	if err != nil {
		return wallet, err
	}
	for rows.Next() {
		var entry LedgerEntry
		if err := rows.Scan(&entry.Kind, &entry.AmountMicro, &entry.Reference, &entry.CreatedAt); err != nil {
			rows.Close()
			return wallet, err
		}
		wallet.Entries = append(wallet.Entries, entry)
	}
	if err := rows.Close(); err != nil {
		return wallet, err
	}
	return wallet, tx.Commit()
}

// DepositWatch is a deposit address the payments service watches.
type DepositWatch struct {
	Index    int64
	ViewedAt *time.Time
}

// DepositWatchList lists accounts' deposit indexes, those whose wallet was
// opened most recently first.
func (s *Store) DepositWatchList(ctx context.Context, limit int) ([]DepositWatch, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT deposit_index, wallet_viewed_at FROM accounts
		  WHERE deposit_index IS NOT NULL AND deleted_at IS NULL
		  ORDER BY wallet_viewed_at DESC NULLS LAST, deposit_index
		  LIMIT $1`,
		max(limit, 1),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var watched []DepositWatch
	for rows.Next() {
		var watch DepositWatch
		var viewedAt sql.NullTime
		if err := rows.Scan(&watch.Index, &viewedAt); err != nil {
			return nil, err
		}
		if viewedAt.Valid {
			watch.ViewedAt = &viewedAt.Time
		}
		watched = append(watched, watch)
	}
	return watched, rows.Err()
}

// DepositCredit is a confirmed USDT transfer to a deposit address.
type DepositCredit struct {
	TxID        string
	Address     string
	Index       int64
	AmountMicro int64
	BlockTime   time.Time
}

// CreditDeposit credits a deposit to the account that owns its index, once:
// reporting the same transfer again changes nothing and returns false.
func (s *Store) CreditDeposit(ctx context.Context, deposit DepositCredit) (bool, error) {
	if deposit.AmountMicro <= 0 {
		return false, errors.New("deposit amount must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var accountID string
	err = tx.QueryRowContext(ctx,
		`SELECT id::text FROM accounts WHERE deposit_index = $1 AND deleted_at IS NULL FOR UPDATE`,
		deposit.Index,
	).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDepositAccountNotFound
	}
	if err != nil {
		return false, err
	}
	var inserted string
	err = tx.QueryRowContext(ctx,
		`INSERT INTO usdt_deposits (tx_id, address, account_id, amount_micro, block_time)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (tx_id, address) DO NOTHING
		 RETURNING tx_id`,
		strings.ToLower(deposit.TxID),
		deposit.Address,
		accountID,
		deposit.AmountMicro,
		deposit.BlockTime.UTC(),
	).Scan(&inserted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO account_ledger (account_id, kind, amount_micro, reference) VALUES ($1, 'deposit', $2, $3)`,
		accountID,
		deposit.AmountMicro,
		"tron:"+strings.ToLower(deposit.TxID),
	); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
