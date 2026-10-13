package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// SiaCredentialAccount returns the account of a runtime the host holds,
// including one it is deleting, and whether that account has been deleted.
// A runtime VM never qualifies for another account's runtime.
func (s *Store) SiaCredentialAccount(ctx context.Context, runtimeID, hostID string) (string, bool, error) {
	runtimeID = strings.TrimSpace(runtimeID)
	hostID = strings.TrimSpace(hostID)
	if runtimeID == "" || hostID == "" {
		return "", false, ErrRuntimeNotFound
	}
	var accountID string
	var deleted bool
	err := s.db.QueryRowContext(ctx,
		`SELECT r.account_id::text, a.deleted_at IS NOT NULL
		   FROM runtimes r
		   JOIN accounts a ON a.id = r.account_id
		  WHERE r.id = $1
		    AND r.host_id = $2
		    AND r.deleted_at IS NULL
		    AND NOT EXISTS (
		        SELECT 1 FROM provisioned_vms v
		         WHERE v.id = $2 AND v.account_id IS DISTINCT FROM r.account_id
		    )`,
		runtimeID,
		hostID,
	).Scan(&accountID, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrRuntimeNotFound
	}
	return accountID, deleted, err
}

// MarkAccountSiaRegistered records that the account has keys on the Sia
// indexer, so deleting the account also removes it there.
func (s *Store) MarkAccountSiaRegistered(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET sia_registered_at = COALESCE(sia_registered_at, NOW()) WHERE id = $1`,
		strings.TrimSpace(accountID),
	)
	return err
}

// DeletedAccountsOnSia lists deleted accounts that are still on the Sia
// indexer, oldest deletion first.
func (s *Store) DeletedAccountsOnSia(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id::text FROM accounts
		  WHERE deleted_at IS NOT NULL
		    AND sia_registered_at IS NOT NULL
		    AND sia_released_at IS NULL
		  ORDER BY deleted_at
		  LIMIT $1`,
		max(limit, 1),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accountIDs []string
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, accountID)
	}
	return accountIDs, rows.Err()
}

// ReleaseAccountSia records that a deleted account is gone from the Sia
// indexer, taking its snapshots there with it, and finishes the deletion of
// its runtimes that only waited for those snapshots.
func (s *Store) ReleaseAccountSia(ctx context.Context, accountID, blobStoreKind string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		`UPDATE accounts SET sia_released_at = NOW()
		  WHERE id = $1 AND deleted_at IS NOT NULL AND sia_released_at IS NULL`,
		strings.TrimSpace(accountID),
	)
	if err != nil {
		return 0, err
	}
	if released, err := result.RowsAffected(); err != nil || released == 0 {
		return 0, err
	}
	result, err = tx.ExecContext(ctx,
		`UPDATE runtimes
		    SET status = 'deleted',
		        deleted_at = NOW(),
		        container_name = NULL,
		        adb_port = NULL,
		        viewer_port = NULL,
		        blob_manifest_json = NULL,
		        blob_last_snapshot_at = NULL,
		        cleanup_pending = FALSE,
		        connection_status = 'offline',
		        updated_at = NOW()
		  WHERE account_id = $1
		    AND deleted_at IS NULL
		    AND desired_state = 'deleted'
		    AND host_id IS NULL
		    AND LOWER(COALESCE(NULLIF(TRIM(blob_store_kind), ''), 'local-disk')) = $2`,
		strings.TrimSpace(accountID),
		strings.ToLower(strings.TrimSpace(blobStoreKind)),
	)
	if err != nil {
		return 0, err
	}
	finished, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(finished), tx.Commit()
}
