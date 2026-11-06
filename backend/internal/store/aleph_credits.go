package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// How soon Aleph would start refusing VMs before someone should be told, in
// days of the account's own spending.
const (
	creditWarnDays     = 14
	creditCriticalDays = 3
)

const (
	CreditLevelGood = "good"
	CreditLevelWarn = "warn"
	CreditLevelBad  = "bad"
)

// AlephCreditStatus is a reading of the account that pays for runtime VMs.
type AlephCreditStatus struct {
	Address       string
	Credits       int64
	CreditsPerDay int64
	CheckedAt     time.Time
}

// CreditRunway is how long until Aleph starts refusing new VMs.
type CreditRunway struct {
	Level string
	// Needed estimates what Aleph wants the account to hold to create a VM: a
	// day of everything it pays for, and of the new VM. Aleph goes by what the
	// account pays for right now, so after a busy week this runs a little high.
	Needed int64
	// BelowNeeded is the account holding less than Needed.
	BelowNeeded bool
	// DaysLeft is until the account holds less than Needed, or -1 when it
	// spends nothing and so never will.
	DaysLeft float64
}

// Runway works out when Aleph starts refusing VMs, given what a VM costs in
// credits an hour. Aleph refuses them long before the balance reaches zero,
// so the balance alone is no warning.
func (s AlephCreditStatus) Runway(vmCreditsPerHour int64) CreditRunway {
	runway := CreditRunway{Level: CreditLevelGood, DaysLeft: -1}
	runway.Needed = s.CreditsPerDay + 24*max(vmCreditsPerHour, 0)
	if s.Credits < runway.Needed {
		runway.Level, runway.BelowNeeded, runway.DaysLeft = CreditLevelBad, true, 0
		return runway
	}
	if s.CreditsPerDay <= 0 {
		return runway
	}
	runway.DaysLeft = float64(s.Credits-runway.Needed) / float64(s.CreditsPerDay)
	switch {
	case runway.DaysLeft <= creditCriticalDays:
		runway.Level = CreditLevelBad
	case runway.DaysLeft <= creditWarnDays:
		runway.Level = CreditLevelWarn
	}
	return runway
}

// SaveAlephCreditStatus records the provisioner's latest reading.
func (s *Store) SaveAlephCreditStatus(ctx context.Context, status AlephCreditStatus) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO aleph_credit_status (address, credits, credits_per_day, checked_at)
		 VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (address) DO UPDATE
		    SET credits = EXCLUDED.credits, credits_per_day = EXCLUDED.credits_per_day, checked_at = EXCLUDED.checked_at`,
		strings.TrimSpace(status.Address), status.Credits, status.CreditsPerDay,
	)
	return err
}

// LatestAlephCreditStatus is the newest reading, or nil before the first. It
// is the newest because a paying account that changes leaves its old row.
func (s *Store) LatestAlephCreditStatus(ctx context.Context) (*AlephCreditStatus, error) {
	var status AlephCreditStatus
	err := s.db.QueryRowContext(ctx,
		`SELECT address, credits, credits_per_day, checked_at
		   FROM aleph_credit_status ORDER BY checked_at DESC LIMIT 1`,
	).Scan(&status.Address, &status.Credits, &status.CreditsPerDay, &status.CheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &status, nil
}

// OperatorNoticeDelivery is one encrypted notice to one operator device.
type OperatorNoticeDelivery struct {
	ID                 string
	EventID            string
	DeviceID           string
	EnvelopeCiphertext string
	ExpiresAt          time.Time
}

// ListOperatorNotificationSubscriptions lists the devices of operator accounts
// that can receive notifications.
func (s *Store) ListOperatorNotificationSubscriptions(ctx context.Context) ([]DeviceNotificationSubscription, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.device_id, p.account_id, p.encryption_public_key, p.created_at,
		        p.updated_at, p.last_connected_at
		   FROM device_notification_subscriptions p
		   JOIN devices d ON d.id = p.device_id AND d.account_id = p.account_id
		   JOIN accounts a ON a.id = p.account_id
		   JOIN account_entitlements e ON e.account_id = p.account_id
		  WHERE e.source = 'operator' AND d.revoked_at IS NULL AND a.deleted_at IS NULL`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var subscriptions []DeviceNotificationSubscription
	for rows.Next() {
		var item DeviceNotificationSubscription
		if err := rows.Scan(
			&item.DeviceID, &item.AccountID, &item.EncryptionPublicKey,
			&item.CreatedAt, &item.UpdatedAt, &item.LastConnectedAt,
		); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, item)
	}
	return subscriptions, rows.Err()
}

// EnqueueOperatorNoticeDelivery queues a notice for a device, once per event,
// and reports whether it was new.
func (s *Store) EnqueueOperatorNoticeDelivery(ctx context.Context, delivery OperatorNoticeDelivery) (bool, error) {
	if _, err := uuid.Parse(strings.TrimSpace(delivery.ID)); err != nil {
		return false, ErrNotificationDelivery
	}
	if _, err := uuid.Parse(strings.TrimSpace(delivery.EventID)); err != nil {
		return false, ErrNotificationDelivery
	}
	if strings.TrimSpace(delivery.DeviceID) == "" || strings.TrimSpace(delivery.EnvelopeCiphertext) == "" ||
		!delivery.ExpiresAt.After(time.Now().UTC()) {
		return false, ErrNotificationDelivery
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM operator_notice_deliveries
		  WHERE expires_at < NOW()
		     OR (delivered_at IS NOT NULL AND delivered_at < NOW() - INTERVAL '24 hours')`); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO operator_notice_deliveries (id, event_id, device_id, envelope_ciphertext, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (event_id, device_id) DO NOTHING`,
		delivery.ID, delivery.EventID, delivery.DeviceID, delivery.EnvelopeCiphertext, delivery.ExpiresAt.UTC(),
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}
