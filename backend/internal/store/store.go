package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaSQL string

const runtimeColumns = `id, account_id, name, status, desired_state, connection_status, host_id, persona_version, active_persona_json, android_image, android_version, width_px, height_px, density_dpi, audio_enabled, camera_mode, file_mode, blob_auto_snapshot, blob_retain_days, blob_store_kind, blob_manifest_json, blob_host_id, blob_last_snapshot_at, started_at, load_average, container_name, adb_port, viewer_port, wipe_requested, cleanup_pending, operation_generation, last_error, deleted_at, created_at, updated_at`

const (
	// "Virtroid" encoded as a positive signed 64-bit integer. PostgreSQL holds
	// this transaction-scoped advisory lock while schema changes are applied.
	schemaMigrationLockKey  int64 = 0x56697274726f6964
	currentSchemaVersion    int64 = 2026071902
	schemaVersionLabel            = "security lifecycle and durable handoff remediation 2026-07-19"
	runtimeLogRetentionRows       = 2000
	runtimeLogSourceRunes         = 64
	runtimeLogLevelRunes          = 32
	runtimeLogMessageRunes        = 4096
)

const (
	defaultAndroidImage  = "redroid/redroid:14.0.0_64only-latest"
	viewerPortStart      = 46000
	viewerPortEnd        = 46099
	sessionAttachTTL     = 2 * time.Minute
	runtimeCapabilityTTL = 24 * time.Hour
	// Keep this aligned with the Android client's MAX_CATALOG_ITEMS bound so a
	// valid server response is never rejected solely because of cardinality.
	catalogResponseLimit = 250
)

var allowedAndroidImages = map[string]struct{}{
	defaultAndroidImage: {},
}

type Store struct {
	db *sql.DB
}

var (
	ErrNoReadyHost             = errors.New("no redroid-capable host available")
	ErrDeviceNotFound          = errors.New("device not found")
	ErrRuntimeNotFound         = errors.New("runtime not found")
	ErrRuntimeNotReady         = errors.New("runtime is not ready for sessions")
	ErrSessionNotFound         = errors.New("session not found")
	ErrSessionAlreadyActive    = errors.New("runtime already has an active session")
	ErrIdentityNotFound        = errors.New("identity password is not configured for this device")
	ErrIdentityAlreadySet      = errors.New("identity password is already configured for this device")
	ErrIdentityAuthFailed      = errors.New("identity authentication failed")
	ErrIdentityKeyRequired     = errors.New("blob access key is required")
	ErrDeviceRequestReplay     = errors.New("device request nonce has already been used")
	ErrNodeRequestReplay       = errors.New("node request nonce has already been used")
	ErrHostNotFound            = errors.New("host not found")
	ErrRuntimeEntitlement      = errors.New("runtime entitlement is required")
	ErrRuntimeQuota            = errors.New("runtime quota exceeded")
	ErrRuntimeActiveQuota      = errors.New("active runtime quota exceeded")
	ErrRuntimeStartQuota       = errors.New("runtime start quota exceeded")
	ErrRuntimeProfile          = errors.New("runtime profile is not allowed")
	ErrSecurityEventRateLimit  = errors.New("security event rate limit exceeded")
	ErrAccountNotFound         = errors.New("account not found")
	ErrRuntimeBlobOwner        = errors.New("runtime local snapshot owner is unknown")
	ErrRuntimeCleanupPending   = errors.New("runtime cleanup is still pending")
	ErrRuntimeObservationStale = errors.New("runtime observation generation is stale")
	ErrRuntimeBlobKeyHandoff   = errors.New("runtime blob-key handoff is not available")
	ErrRuntimeCapability       = errors.New("runtime capability is invalid or expired")
	ErrRuntimeCapabilityReplay = errors.New("runtime capability nonce has already been used")
)

const (
	RuntimeEntitlementRequiredCode = "runtime_entitlement_required"
	RuntimeQuotaExceededCode       = "runtime_quota_exceeded"
	ActiveRuntimeQuotaExceededCode = "active_runtime_quota_exceeded"
	RuntimeStartQuotaExceededCode  = "runtime_start_quota_exceeded"
	RuntimeProfileNotAllowedCode   = "runtime_profile_not_allowed"
	NoReadyHostCode                = "no_ready_host"
	RuntimeBlobOwnerCode           = "runtime_blob_owner_unknown"
	RuntimeCleanupPendingCode      = "runtime_cleanup_pending"
)

type BootstrapResult struct {
	Account Account  `json:"account"`
	Device  Device   `json:"device"`
	Runtime *Runtime `json:"runtime,omitempty"`
}

type Account struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type AccountStorage struct {
	AccountID             string     `json:"account_id"`
	Provider              string     `json:"provider"`
	FundingModel          string     `json:"funding_model"`
	WalletAddress         *string    `json:"wallet_address,omitempty"`
	FundingAddress        *string    `json:"funding_address,omitempty"`
	EncryptedSeedBlob     *string    `json:"encrypted_seed_blob,omitempty"`
	SeedEncryptionHint    *string    `json:"seed_encryption_hint,omitempty"`
	Status                string     `json:"status"`
	LastPreflightStatus   *string    `json:"last_preflight_status,omitempty"`
	LastPreflightJSON     *string    `json:"last_preflight_json,omitempty"`
	LastPreflightAt       *time.Time `json:"last_preflight_at,omitempty"`
	EncryptedSeedBackedUp bool       `json:"encrypted_seed_backed_up"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type AccountEntitlement struct {
	AccountID           string     `json:"account_id"`
	Source              string     `json:"source"`
	Status              string     `json:"status"`
	RuntimeLimit        int        `json:"runtime_limit"`
	ActiveRuntimeLimit  int        `json:"active_runtime_limit"`
	RuntimeStartsPerDay int        `json:"runtime_starts_per_day"`
	StorageBytesLimit   int64      `json:"storage_bytes_limit"`
	TrialRuntimeSeconds int        `json:"trial_runtime_seconds"`
	ExpiresAt           *time.Time `json:"expires_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type AccountEntitlementSummary struct {
	AccountID                   string     `json:"account_id"`
	Source                      string     `json:"source"`
	Status                      string     `json:"status"`
	RuntimeLimit                int        `json:"runtime_limit"`
	RuntimeCount                int        `json:"runtime_count"`
	RuntimeRemaining            int        `json:"runtime_remaining"`
	ActiveRuntimeLimit          int        `json:"active_runtime_limit"`
	ActiveRuntimeCount          int        `json:"active_runtime_count"`
	ActiveRuntimeRemaining      int        `json:"active_runtime_remaining"`
	RuntimeStartsPerDay         int        `json:"runtime_starts_per_day"`
	RuntimeStartsUsedToday      int        `json:"runtime_starts_used_today"`
	RuntimeStartsRemainingToday int        `json:"runtime_starts_remaining_today"`
	StorageBytesLimit           int64      `json:"storage_bytes_limit"`
	TrialRuntimeSeconds         int        `json:"trial_runtime_seconds"`
	ExpiresAt                   *time.Time `json:"expires_at,omitempty"`
	CanCreateRuntime            bool       `json:"can_create_runtime"`
	CanStartRuntime             bool       `json:"can_start_runtime"`
	CreateRuntimeBlockedCode    string     `json:"create_runtime_blocked_code,omitempty"`
	CreateRuntimeBlockedReason  string     `json:"create_runtime_blocked_reason,omitempty"`
	StartRuntimeBlockedCode     string     `json:"start_runtime_blocked_code,omitempty"`
	StartRuntimeBlockedReason   string     `json:"start_runtime_blocked_reason,omitempty"`
}

type AppCatalogEntry struct {
	PackageName      string    `json:"package_name"`
	Source           string    `json:"source"`
	DisplayName      string    `json:"display_name"`
	Summary          string    `json:"summary"`
	IconURL          string    `json:"icon_url,omitempty"`
	VersionName      string    `json:"version_name"`
	VersionCode      int64     `json:"version_code"`
	APKURL           string    `json:"apk_url,omitempty"`
	APKSHA256        string    `json:"apk_sha256,omitempty"`
	APKSizeBytes     int64     `json:"apk_size_bytes"`
	MinSDK           int       `json:"min_sdk"`
	NativeCode       string    `json:"native_code,omitempty"`
	License          string    `json:"license,omitempty"`
	CategoriesJSON   string    `json:"categories_json"`
	AntiFeaturesJSON string    `json:"anti_features_json"`
	Recommended      bool      `json:"recommended"`
	Selected         bool      `json:"selected"`
	CatalogUpdatedAt time.Time `json:"catalog_updated_at"`
}

type Device struct {
	ID              string     `json:"id"`
	AccountID       string     `json:"account_id"`
	Name            string     `json:"name"`
	PublicKey       string     `json:"public_key"`
	BlobKeyVerifier *string    `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

type Runtime struct {
	ID                  string            `json:"id"`
	AccountID           string            `json:"account_id"`
	Name                string            `json:"name"`
	Status              string            `json:"status"`
	DesiredState        string            `json:"desired_state"`
	ConnectionStatus    string            `json:"connection_status"`
	HostID              *string           `json:"host_id,omitempty"`
	PersonaVersion      int               `json:"persona_version"`
	ActivePersonaJSON   *string           `json:"active_persona_json,omitempty"`
	AndroidImage        string            `json:"android_image"`
	AndroidVersion      string            `json:"android_version"`
	WidthPx             int               `json:"width_px"`
	HeightPx            int               `json:"height_px"`
	DensityDpi          int               `json:"density_dpi"`
	AudioEnabled        bool              `json:"audio_enabled"`
	CameraMode          string            `json:"camera_mode"`
	FileMode            string            `json:"file_mode"`
	BlobAutoSnapshot    bool              `json:"blob_auto_snapshot"`
	BlobRetainDays      int               `json:"blob_retain_days"`
	BlobStoreKind       *string           `json:"blob_store_kind,omitempty"`
	BlobManifestJSON    *string           `json:"blob_manifest_json,omitempty"`
	BlobHostID          *string           `json:"-"`
	BlobLastSnapshotAt  *time.Time        `json:"blob_last_snapshot_at,omitempty"`
	StartedAt           *time.Time        `json:"started_at,omitempty"`
	LoadAverage         *float64          `json:"load_average,omitempty"`
	ContainerName       *string           `json:"container_name,omitempty"`
	ADBPort             *int              `json:"adb_port,omitempty"`
	ViewerPort          *int              `json:"viewer_port,omitempty"`
	WipeRequested       bool              `json:"wipe_requested"`
	CleanupPending      bool              `json:"cleanup_pending"`
	OperationGeneration int64             `json:"operation_generation"`
	LastError           *string           `json:"last_error,omitempty"`
	SelectedApps        []AppCatalogEntry `json:"selected_apps,omitempty"`
	DeletedAt           *time.Time        `json:"deleted_at,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
}

type RuntimeLogEntry struct {
	ID        int64     `json:"id"`
	RuntimeID string    `json:"runtime_id"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type RuntimeCapability struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"account_id"`
	RuntimeID  string     `json:"runtime_id"`
	PublicKey  string     `json:"-"`
	Scopes     string     `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type SecurityEventInput struct {
	NodeID    string
	Source    string
	Rule      string
	Priority  string
	Output    string
	Tags      []string
	EventJSON string
	EventTime *time.Time
}

type CreateRuntimeInput struct {
	Name             string
	AndroidImage     string
	AndroidVersion   string
	WidthPx          int
	HeightPx         int
	DensityDpi       int
	AudioEnabled     bool
	CameraMode       string
	FileMode         string
	BlobAutoSnapshot bool
	BlobRetainDays   int
}

type UpdateRuntimeInput struct {
	Name             *string
	AndroidImage     *string
	AndroidVersion   *string
	WidthPx          *int
	HeightPx         *int
	DensityDpi       *int
	AudioEnabled     *bool
	CameraMode       *string
	FileMode         *string
	BlobAutoSnapshot *bool
	BlobRetainDays   *int
}

type UpdateAccountStorageInput struct {
	Provider           string
	FundingModel       string
	WalletAddress      *string
	EncryptedSeedBlob  *string
	SeedEncryptionHint *string
	Status             string
}

type Host struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	AdvertiseAddr          string     `json:"advertise_addr"`
	RelayPort              int        `json:"relay_port"`
	DockerSocket           bool       `json:"docker_socket"`
	Binder                 bool       `json:"binder"`
	PublicKey              string     `json:"public_key,omitempty"`
	BlobStoreKind          string     `json:"blob_store_kind"`
	StoragePreflightKind   *string    `json:"storage_preflight_kind,omitempty"`
	StoragePreflightStatus *string    `json:"storage_preflight_status,omitempty"`
	StoragePreflightJSON   *string    `json:"storage_preflight_json,omitempty"`
	StoragePreflightAt     *time.Time `json:"storage_preflight_at,omitempty"`
	StorageWalletAddress   *string    `json:"storage_wallet_address,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	LastHeartbeatAt        time.Time  `json:"last_heartbeat_at"`
}

type HostHeartbeat struct {
	ID                     string
	Name                   string
	AdvertiseAddr          string
	RelayPort              int
	DockerSocket           bool
	Binder                 bool
	PublicKey              string
	BlobStoreKind          string
	StoragePreflightKind   string
	StoragePreflightStatus string
	StoragePreflightJSON   string
	StoragePreflightAt     *time.Time
	StorageWalletAddress   string
}

type RuntimeObservation struct {
	HostID              string
	Status              string
	ConnectionStatus    string
	ContainerName       *string
	ADBPort             *int
	LastError           *string
	BlobStoreKind       *string
	BlobManifestJSON    *string
	BlobLastSnapshotAt  *time.Time
	LoadAverage         *float64
	ClearWipeRequested  bool
	ActivePersonaJSON   *string
	ClearActivePersona  bool
	ClearBlobManifest   bool
	CleanupComplete     bool
	OperationGeneration int64
	Deleted             bool
}

type RuntimeBlobKeyHandoff struct {
	AccountID       string
	RuntimeID       string
	HostID          string
	Operation       string
	LeaseID         string
	EnvelopeJSON    string
	BlobKeyVerifier string
	ExpiresAt       time.Time
}

type Session struct {
	ID                    string     `json:"id"`
	RuntimeID             string     `json:"runtime_id"`
	DeviceID              string     `json:"-"`
	CapabilityID          *string    `json:"-"`
	Status                string     `json:"status"`
	RelayToken            string     `json:"relay_token,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	LastClientHeartbeatAt *time.Time `json:"last_client_heartbeat_at,omitempty"`
	EndedAt               *time.Time `json:"ended_at,omitempty"`
	EndReason             *string    `json:"end_reason,omitempty"`
	ExpiresAt             time.Time  `json:"expires_at"`
}

type SessionState struct {
	Session         Session `json:"session"`
	Runtime         Runtime `json:"runtime"`
	EffectiveStatus string  `json:"effective_status"`
	IsExpired       bool    `json:"is_expired"`
	RuntimeReady    bool    `json:"runtime_ready"`
	CanResume       bool    `json:"can_resume"`
}

type RuntimeState struct {
	Runtime                 Runtime `json:"runtime"`
	CurrentDeviceSessionID  *string `json:"current_device_session_id,omitempty"`
	EffectiveState          string  `json:"effective_state"`
	RuntimeReady            bool    `json:"runtime_ready"`
	HasActiveSession        bool    `json:"has_active_session"`
	HasCurrentDeviceSession bool    `json:"has_current_device_session"`
	CanConnect              bool    `json:"can_connect"`
	CanStart                bool    `json:"can_start"`
	CanStop                 bool    `json:"can_stop"`
	CanWipe                 bool    `json:"can_wipe"`
	CanDelete               bool    `json:"can_delete"`
	IsBusy                  bool    `json:"is_busy"`
	BlockedReason           string  `json:"blocked_reason,omitempty"`
}

type SessionRelayTarget struct {
	SessionID  string `json:"session_id"`
	RuntimeID  string `json:"runtime_id"`
	DeviceID   string `json:"-"`
	HostID     string `json:"host_id"`
	ViewerPort int    `json:"viewer_port"`
}

type SessionReapResult struct {
	ExpiredPendingSessions        int      `json:"expired_pending_sessions"`
	StaleActiveSessions           int      `json:"stale_active_sessions"`
	RevokedRuntimeCapabilities    int      `json:"revoked_runtime_capabilities"`
	PrunedRuntimeCapabilityNonces int      `json:"pruned_runtime_capability_nonces"`
	StoppedRuntimeIDs             []string `json:"stopped_runtime_ids"`
}

type ActiveRuntimeSessionSummary struct {
	SessionID             string     `json:"session_id"`
	RuntimeID             string     `json:"runtime_id"`
	RuntimeName           string     `json:"runtime_name"`
	Status                string     `json:"status"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	ExpiresAt             time.Time  `json:"expires_at"`
	LastClientHeartbeatAt *time.Time `json:"last_client_heartbeat_at,omitempty"`
	CapabilityID          *string    `json:"capability_id,omitempty"`
	CapabilityExpiresAt   *time.Time `json:"capability_expires_at,omitempty"`
	CapabilityLastUsedAt  *time.Time `json:"capability_last_used_at,omitempty"`
}

type ActiveRuntimeCapabilitySummary struct {
	ID          string     `json:"id"`
	RuntimeID   string     `json:"runtime_id"`
	RuntimeName string     `json:"runtime_name"`
	Scopes      string     `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}

	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetMaxIdleConns(4)
	db.SetMaxOpenConns(12)

	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("lock schema migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			description TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("create schema migration ledger: %w", err)
	}

	var newestVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&newestVersion); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if newestVersion > currentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than binary version %d", newestVersion, currentSchemaVersion)
	}

	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema version %d: %w", currentSchemaVersion, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, description)
		VALUES ($1, $2)
		ON CONFLICT (version) DO NOTHING
	`, currentSchemaVersion, schemaVersionLabel); err != nil {
		return fmt.Errorf("record schema version %d: %w", currentSchemaVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema version %d: %w", currentSchemaVersion, err)
	}
	return nil
}

func (s *Store) BootstrapAccount(ctx context.Context, deviceName, publicKey string, defaults CreateRuntimeInput) (BootstrapResult, error) {
	return s.BootstrapAccountWithIdentity(ctx, "", "", deviceName, publicKey, defaults)
}

func (s *Store) BootstrapAccountWithIdentity(
	ctx context.Context,
	accountID string,
	deviceID string,
	deviceName string,
	publicKey string,
	_ CreateRuntimeInput,
) (BootstrapResult, error) {
	if strings.TrimSpace(deviceName) == "" {
		return BootstrapResult{}, errors.New("device name is required")
	}
	if strings.TrimSpace(publicKey) == "" {
		return BootstrapResult{}, errors.New("device public key is required")
	}

	accountID, err := normalizeBootstrapUUID(accountID, "account id")
	if err != nil {
		return BootstrapResult{}, err
	}
	deviceID, err = normalizeBootstrapUUID(deviceID, "device id")
	if err != nil {
		return BootstrapResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer tx.Rollback()

	var result BootstrapResult

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO accounts (id) VALUES ($1) RETURNING id, created_at`,
		accountID,
	).Scan(&result.Account.ID, &result.Account.CreatedAt); err != nil {
		return BootstrapResult{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO account_entitlements (account_id)
			 VALUES ($1)
			 ON CONFLICT (account_id) DO NOTHING`,
		accountID,
	); err != nil {
		return BootstrapResult{}, err
	}

	if err := tx.QueryRowContext(ctx,
		`INSERT INTO devices (id, account_id, name, public_key) VALUES ($1, $2, $3, $4)
		 RETURNING id, account_id, name, public_key, blob_key_verifier, created_at, last_seen_at`,
		deviceID, accountID, strings.TrimSpace(deviceName), strings.TrimSpace(publicKey),
	).Scan(
		&result.Device.ID,
		&result.Device.AccountID,
		&result.Device.Name,
		&result.Device.PublicKey,
		&result.Device.BlobKeyVerifier,
		&result.Device.CreatedAt,
		&result.Device.LastSeenAt,
	); err != nil {
		return BootstrapResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return BootstrapResult{}, err
	}

	return result, nil
}

func normalizeBootstrapUUID(value string, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return uuid.NewString(), nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s must be a valid uuid", field)
	}
	return parsed.String(), nil
}

func (s *Store) DeleteAccount(ctx context.Context, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ErrAccountNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var foundAccountID string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`,
		accountID,
	).Scan(&foundAccountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAccountNotFound
		}
		return err
	}

	rows, err := tx.QueryContext(ctx,
		`UPDATE runtimes
		 SET host_id = COALESCE(host_id, blob_host_id),
		     operation_generation = CASE
		         WHEN desired_state = 'deleted' THEN operation_generation
		         ELSE operation_generation + 1
		     END,
		     status = CASE
		         WHEN COALESCE(host_id, blob_host_id) IS NULL
		          AND NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NULL THEN 'deleted'
		         ELSE 'deleting'
		     END,
		     desired_state = 'deleted',
		     connection_status = 'offline',
		     started_at = NULL,
		     container_name = CASE WHEN COALESCE(host_id, blob_host_id) IS NULL THEN NULL ELSE container_name END,
		     adb_port = CASE WHEN COALESCE(host_id, blob_host_id) IS NULL THEN NULL ELSE adb_port END,
		     viewer_port = CASE WHEN COALESCE(host_id, blob_host_id) IS NULL THEN NULL ELSE viewer_port END,
		     deleted_at = CASE
		         WHEN COALESCE(host_id, blob_host_id) IS NULL
		          AND NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NULL THEN NOW()
		         ELSE deleted_at
		     END,
		     updated_at = NOW()
		 WHERE account_id = $1
		   AND deleted_at IS NULL
		 RETURNING id, host_id`,
		accountID,
	)
	if err != nil {
		return err
	}

	var queuedRuntimeIDs []string
	for rows.Next() {
		var runtimeID string
		var hostID sql.NullString
		if err := rows.Scan(&runtimeID, &hostID); err != nil {
			rows.Close()
			return err
		}
		if hostID.Valid && strings.TrimSpace(hostID.String) != "" {
			queuedRuntimeIDs = append(queuedRuntimeIDs, runtimeID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, runtimeID := range queuedRuntimeIDs {
		if err := appendRuntimeLogTX(ctx, tx, runtimeID, "system", "warn", "Account deletion requested; runtime cleanup queued."); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM device_request_nonces WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_storage WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_entitlements WHERE account_id = $1`, accountID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, accountID); err != nil {
		return err
	}
	if err := deleteAccountIfRuntimeCleanupDoneTX(ctx, tx, accountID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) ListDevices(ctx context.Context, accountID string) ([]Device, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return []Device{}, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.account_id, d.name, d.public_key, d.blob_key_verifier, d.created_at, d.last_seen_at, d.revoked_at
		 FROM devices d
		 JOIN accounts a ON a.id = d.account_id
		 WHERE d.account_id = $1
		   AND a.deleted_at IS NULL
		   AND d.revoked_at IS NULL
		 ORDER BY COALESCE(d.last_seen_at, d.created_at) DESC, d.created_at DESC`,
		accountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var device Device
		if err := rows.Scan(
			&device.ID,
			&device.AccountID,
			&device.Name,
			&device.PublicKey,
			&device.BlobKeyVerifier,
			&device.CreatedAt,
			&device.LastSeenAt,
			&device.RevokedAt,
		); err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if devices == nil {
		return []Device{}, nil
	}
	return devices, nil
}

func (s *Store) RevokeDevice(ctx context.Context, accountID, targetDeviceID string) (Device, []string, error) {
	accountID = strings.TrimSpace(accountID)
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	if accountID == "" || targetDeviceID == "" {
		return Device{}, nil, ErrDeviceNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, nil, err
	}
	defer tx.Rollback()

	var device Device
	if err := tx.QueryRowContext(ctx,
		`UPDATE devices d
		 SET revoked_at = NOW(),
		     last_seen_at = NOW()
		 FROM accounts a
		 WHERE d.account_id = $1
		   AND d.id = $2
		   AND d.account_id = a.id
		   AND a.deleted_at IS NULL
		   AND d.revoked_at IS NULL
		 RETURNING d.id, d.account_id, d.name, d.public_key, d.blob_key_verifier, d.created_at, d.last_seen_at, d.revoked_at`,
		accountID,
		targetDeviceID,
	).Scan(
		&device.ID,
		&device.AccountID,
		&device.Name,
		&device.PublicKey,
		&device.BlobKeyVerifier,
		&device.CreatedAt,
		&device.LastSeenAt,
		&device.RevokedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, nil, ErrDeviceNotFound
		}
		return Device{}, nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM device_request_nonces WHERE account_id = $1 AND device_id = $2`,
		accountID,
		targetDeviceID,
	); err != nil {
		return Device{}, nil, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE runtime_capabilities
		    SET revoked_at = NOW(),
		        updated_at = NOW()
		  WHERE account_id = $1
		    AND revoked_at IS NULL`,
		accountID,
	); err != nil {
		return Device{}, nil, err
	}

	rows, err := tx.QueryContext(ctx,
		`WITH account_caps AS (
		     SELECT id FROM runtime_capabilities WHERE account_id = $1
		 ),
		 revoked_sessions AS (
		     UPDATE sessions AS s
		        SET status = 'revoked',
		            updated_at = NOW(),
		            ended_at = NOW(),
		            end_reason = 'device revoked'
		       FROM runtimes AS runtime_owner
		      WHERE (s.device_id = $2 OR s.capability_id IN (SELECT id FROM account_caps))
		        AND runtime_owner.id = s.runtime_id
		        AND runtime_owner.account_id = $1
		        AND s.status IN ('pending', 'active')
		      RETURNING s.runtime_id
		 ),
		 candidate_runtimes AS (
		     SELECT DISTINCT r.id
		       FROM runtimes r
		       JOIN revoked_sessions s ON s.runtime_id = r.id
		      WHERE r.account_id = $1
		        AND r.deleted_at IS NULL
		        AND r.desired_state = 'running'
		        AND NOT EXISTS (
		            SELECT 1
		              FROM sessions live
		             WHERE live.runtime_id = r.id
		               AND live.device_id IS NOT NULL
		               AND live.device_id <> $2
		               AND live.status IN ('pending', 'active')
		               AND live.expires_at > NOW()
		        )
		 ),
		 updated AS (
		     UPDATE runtimes r
		        SET status = CASE WHEN r.host_id IS NULL THEN 'stopped' ELSE 'stopping' END,
		            desired_state = 'stopped',
		            operation_generation = r.operation_generation + 1,
		            connection_status = CASE WHEN r.host_id IS NULL THEN 'offline' ELSE 'disconnecting' END,
		            started_at = NULL,
		            last_error = NULL,
		            updated_at = NOW()
		       FROM candidate_runtimes c
		      WHERE r.id = c.id
		      RETURNING r.id
		 )
		 SELECT id FROM updated`,
		accountID,
		targetDeviceID,
	)
	if err != nil {
		return Device{}, nil, err
	}
	var stoppedRuntimeIDs []string
	for rows.Next() {
		var runtimeID string
		if err := rows.Scan(&runtimeID); err != nil {
			rows.Close()
			return Device{}, nil, err
		}
		stoppedRuntimeIDs = append(stoppedRuntimeIDs, runtimeID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Device{}, nil, err
	}
	if err := rows.Close(); err != nil {
		return Device{}, nil, err
	}

	for _, runtimeID := range stoppedRuntimeIDs {
		if err := appendRuntimeLogTX(ctx, tx, runtimeID, "system", "warn", "Runtime stop queued because a linked device was revoked."); err != nil {
			return Device{}, nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return Device{}, nil, err
	}
	return device, stoppedRuntimeIDs, nil
}

func deleteAccountIfRuntimeCleanupDoneTX(ctx context.Context, tx *sql.Tx, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil
	}

	var deletedAt sql.NullTime
	if err := tx.QueryRowContext(ctx,
		`SELECT deleted_at FROM accounts WHERE id = $1`,
		accountID,
	).Scan(&deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if !deletedAt.Valid {
		return nil
	}

	var remaining int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtimes WHERE account_id = $1 AND deleted_at IS NULL`,
		accountID,
	).Scan(&remaining); err != nil {
		return err
	}
	if remaining > 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM accounts WHERE id = $1 AND deleted_at IS NOT NULL`,
		accountID,
	); err != nil {
		return err
	}
	return nil
}

func (s *Store) CreateRuntime(ctx context.Context, accountID string, input CreateRuntimeInput) (Runtime, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Runtime{}, errors.New("account id is required")
	}

	input, err := normalizedCreateInput(input)
	if err != nil {
		return Runtime{}, err
	}
	runtimeID := uuid.NewString()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM accounts WHERE id = $1 AND deleted_at IS NULL`, accountID); err != nil {
		return Runtime{}, err
	}
	if err := ensureRuntimeCreateEntitlementTX(ctx, tx, accountID); err != nil {
		return Runtime{}, err
	}
	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`INSERT INTO runtimes (
			id, account_id, name, status, desired_state, connection_status, android_image, android_version,
			width_px, height_px, density_dpi, audio_enabled, camera_mode, file_mode, blob_auto_snapshot, blob_retain_days
		) VALUES ($1, $2, $3, 'stopped', 'stopped', 'offline', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING %s`, runtimeColumns),
		runtimeID,
		accountID,
		input.Name,
		input.AndroidImage,
		input.AndroidVersion,
		input.WidthPx,
		input.HeightPx,
		input.DensityDpi,
		input.AudioEnabled,
		input.CameraMode,
		input.FileMode,
		input.BlobAutoSnapshot,
		input.BlobRetainDays,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		return Runtime{}, err
	}

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", "Runtime profile created. A fresh container will be assigned when a session starts."); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) ListRuntimes(ctx context.Context, accountID string) ([]Runtime, error) {
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
		 WHERE account_id = $1
		   AND deleted_at IS NULL
		 ORDER BY created_at DESC`, runtimeColumns),
		accountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runtimes []Runtime
	for rows.Next() {
		var runtime Runtime
		if err := rows.Scan(scanRuntimeDest(&runtime)...); err != nil {
			return nil, err
		}
		runtimes = append(runtimes, runtime)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if runtimes == nil {
		return []Runtime{}, nil
	}
	return runtimes, nil
}

func (s *Store) ListRuntimeStates(ctx context.Context, accountID, deviceID string) ([]RuntimeState, error) {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	if accountID == "" || deviceID == "" {
		return []RuntimeState{}, nil
	}

	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT %s,
		           (
		               SELECT s.id
		                 FROM sessions AS s
		                WHERE s.runtime_id = r.id
		                  AND s.device_id = $2
		                  AND s.status IN ('pending', 'active')
		                  AND s.expires_at > NOW()
		                ORDER BY s.updated_at DESC
		                LIMIT 1
		           ) AS current_device_session_id,
		           EXISTS (
		               SELECT 1
		                 FROM sessions AS live
		                WHERE live.runtime_id = r.id
		                  AND live.status IN ('pending', 'active')
		                  AND live.expires_at > NOW()
		           ) AS has_active_session
		      FROM runtimes AS r
		     WHERE r.account_id = $1
		       AND r.deleted_at IS NULL
		     ORDER BY r.created_at DESC`, runtimeColumnsWithAlias("r")),
		accountID,
		deviceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []RuntimeState
	for rows.Next() {
		var runtime Runtime
		var currentDeviceSessionID sql.NullString
		var hasActiveSession bool
		dest := scanRuntimeDest(&runtime)
		dest = append(dest, &currentDeviceSessionID, &hasActiveSession)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		states = append(states, buildRuntimeState(runtime, hasActiveSession, currentDeviceSessionID))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if states == nil {
		return []RuntimeState{}, nil
	}
	return states, nil
}

func (s *Store) GetRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	var runtime Runtime
	err := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
			 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL`, runtimeColumns),
		accountID,
		runtimeID,
	).Scan(scanRuntimeDest(&runtime)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Runtime{}, ErrRuntimeNotFound
	}
	return runtime, err
}

func (s *Store) GetRuntimeState(ctx context.Context, accountID, deviceID, runtimeID string) (RuntimeState, error) {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	runtimeID = strings.TrimSpace(runtimeID)
	if accountID == "" || deviceID == "" || runtimeID == "" {
		return RuntimeState{}, ErrRuntimeNotFound
	}

	var runtime Runtime
	err := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
			 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL`, runtimeColumns),
		accountID,
		runtimeID,
	).Scan(scanRuntimeDest(&runtime)...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RuntimeState{}, ErrRuntimeNotFound
		}
		return RuntimeState{}, err
	}

	var currentDeviceSessionID sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT id
		   FROM sessions
		  WHERE runtime_id = $1
		    AND device_id = $2
		    AND status IN ('pending', 'active')
		    AND expires_at > NOW()
		  ORDER BY updated_at DESC
		  LIMIT 1`,
		runtimeID,
		deviceID,
	).Scan(&currentDeviceSessionID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RuntimeState{}, err
	}

	var hasActiveSession bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (
		     SELECT 1
		       FROM sessions
		      WHERE runtime_id = $1
		        AND status IN ('pending', 'active')
		        AND expires_at > NOW()
		 )`,
		runtimeID,
	).Scan(&hasActiveSession); err != nil {
		return RuntimeState{}, err
	}

	state := buildRuntimeState(runtime, hasActiveSession, currentDeviceSessionID)
	return state, nil
}

func (s *Store) UpdateRuntimeSettings(ctx context.Context, accountID, runtimeID string, input UpdateRuntimeInput) (Runtime, error) {
	current, err := s.GetRuntime(ctx, accountID, runtimeID)
	if err != nil {
		return Runtime{}, err
	}

	if input.Name != nil {
		current.Name = defaultString(*input.Name, current.Name)
	}
	if input.AndroidImage != nil {
		androidImage, err := normalizeAndroidImage(defaultString(*input.AndroidImage, current.AndroidImage))
		if err != nil {
			return Runtime{}, err
		}
		current.AndroidImage = androidImage
	}
	if input.AndroidVersion != nil {
		current.AndroidVersion = defaultString(*input.AndroidVersion, current.AndroidVersion)
	}
	if input.WidthPx != nil && *input.WidthPx > 0 {
		current.WidthPx = *input.WidthPx
	}
	if input.HeightPx != nil && *input.HeightPx > 0 {
		current.HeightPx = *input.HeightPx
	}
	if input.DensityDpi != nil && *input.DensityDpi > 0 {
		current.DensityDpi = *input.DensityDpi
	}
	if input.AudioEnabled != nil {
		current.AudioEnabled = *input.AudioEnabled
	}
	if input.CameraMode != nil {
		current.CameraMode = normalizeChoice(*input.CameraMode, current.CameraMode, "disabled", "passthrough", "upload-only")
	}
	if input.FileMode != nil {
		current.FileMode = normalizeChoice(*input.FileMode, current.FileMode, "upload-only", "download-only", "bidirectional")
	}
	if input.BlobAutoSnapshot != nil {
		current.BlobAutoSnapshot = *input.BlobAutoSnapshot
	}
	if input.BlobRetainDays != nil && *input.BlobRetainDays > 0 {
		current.BlobRetainDays = *input.BlobRetainDays
	}
	if err := validateRuntimeProfile(CreateRuntimeInput{
		Name:             current.Name,
		AndroidImage:     current.AndroidImage,
		AndroidVersion:   current.AndroidVersion,
		WidthPx:          current.WidthPx,
		HeightPx:         current.HeightPx,
		DensityDpi:       current.DensityDpi,
		AudioEnabled:     current.AudioEnabled,
		CameraMode:       current.CameraMode,
		FileMode:         current.FileMode,
		BlobAutoSnapshot: current.BlobAutoSnapshot,
		BlobRetainDays:   current.BlobRetainDays,
	}); err != nil {
		return Runtime{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET name = $3,
		     android_image = $4,
		     android_version = $5,
		     width_px = $6,
		     height_px = $7,
		     density_dpi = $8,
		     audio_enabled = $9,
		     camera_mode = $10,
		     file_mode = $11,
		     blob_auto_snapshot = $12,
		     blob_retain_days = $13,
		     updated_at = NOW()
		 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'
		 RETURNING %s`, runtimeColumns),
		accountID,
		runtimeID,
		current.Name,
		current.AndroidImage,
		current.AndroidVersion,
		current.WidthPx,
		current.HeightPx,
		current.DensityDpi,
		current.AudioEnabled,
		current.CameraMode,
		current.FileMode,
		current.BlobAutoSnapshot,
		current.BlobRetainDays,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	message := fmt.Sprintf(
		"Settings updated. image=%s version=%s display=%dx%d@%ddpi camera=%s files=%s snapshots=%t/%dd",
		runtime.AndroidImage,
		runtime.AndroidVersion,
		runtime.WidthPx,
		runtime.HeightPx,
		runtime.DensityDpi,
		runtime.CameraMode,
		runtime.FileMode,
		runtime.BlobAutoSnapshot,
		runtime.BlobRetainDays,
	)
	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", message); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) DeleteRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	return s.deleteRuntime(ctx, accountID, runtimeID, "")
}

func (s *Store) DeleteRuntimeOnHost(ctx context.Context, accountID, runtimeID, hostID string) (Runtime, error) {
	return s.deleteRuntime(ctx, accountID, runtimeID, strings.TrimSpace(hostID))
}

func (s *Store) deleteRuntime(ctx context.Context, accountID, runtimeID, hostID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET host_id = COALESCE(host_id, NULLIF($3, '')),
		     operation_generation = CASE
		         WHEN desired_state = 'deleted' THEN operation_generation
		         ELSE operation_generation + 1
		     END,
		     status = CASE
		         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN 'deleted'
		         ELSE 'deleting'
		     END,
		     desired_state = 'deleted',
		     connection_status = 'offline',
		     started_at = NULL,
		     active_persona_json = CASE
		         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NULL
		         ELSE active_persona_json
		     END,
		     blob_store_kind = CASE
		         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NULL
		         ELSE blob_store_kind
		     END,
			     blob_manifest_json = CASE
			         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NULL
			         ELSE blob_manifest_json
			     END,
			     blob_host_id = CASE
			         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NULL
			         ELSE blob_host_id
			     END,
			     wipe_requested = FALSE,
		     container_name = CASE
		         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NULL
		         ELSE container_name
		     END,
		     adb_port = CASE
		         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NULL
		         ELSE adb_port
		     END,
		     viewer_port = CASE
		         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NULL
		         ELSE viewer_port
		     END,
		     deleted_at = CASE
		         WHEN COALESCE(host_id, NULLIF($3, '')) IS NULL THEN NOW()
		         ELSE deleted_at
		     END,
		     updated_at = NOW()
		 WHERE account_id = $1
			   AND id = $2
			   AND deleted_at IS NULL
			   AND (
			       $3 = ''
			       OR host_id = $3
			       OR (
			           host_id IS NULL
			           AND (
			               blob_host_id = $3
			               OR (
			                   blob_host_id IS NULL
			                   AND (
			                       NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NULL
			                       OR COALESCE(NULLIF(TRIM(blob_store_kind), ''), 'local-disk') <> 'local-disk'
			                   )
			               )
			           )
			       )
			   )
			 RETURNING %s`, runtimeColumns),
		accountID,
		runtimeID,
		hostID,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	if runtime.HostID == nil {
		if err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`UPDATE runtimes
			 SET status = 'deleted',
			     deleted_at = NOW(),
			     container_name = NULL,
			     adb_port = NULL,
			     viewer_port = NULL,
			     started_at = NULL,
			     updated_at = NOW()
			 WHERE id = $1
			 RETURNING %s`, runtimeColumns),
			runtime.ID,
		).Scan(scanRuntimeDest(&runtime)...); err != nil {
			return Runtime{}, err
		}
	}

	if err := closeRuntimeSessionsTX(ctx, tx, runtime.ID, "runtime deleted"); err != nil {
		return Runtime{}, err
	}
	if _, err := revokeRuntimeCapabilitiesForRuntimeTX(ctx, tx, runtime.ID); err != nil {
		return Runtime{}, err
	}

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "warn", "Runtime scheduled for deletion."); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) RegisterDeviceBlobKeyVerifier(
	ctx context.Context,
	accountID string,
	deviceID string,
	blobKeyVerifier string,
) error {
	return s.setDeviceBlobKeyVerifier(ctx, accountID, deviceID, blobKeyVerifier, "", false)
}

func (s *Store) ChangeDeviceBlobKeyVerifier(
	ctx context.Context,
	accountID string,
	deviceID string,
	blobKeyVerifier string,
	currentBlobKeyVerifier string,
) error {
	return s.setDeviceBlobKeyVerifier(ctx, accountID, deviceID, blobKeyVerifier, currentBlobKeyVerifier, true)
}

func (s *Store) setDeviceBlobKeyVerifier(
	ctx context.Context,
	accountID string,
	deviceID string,
	blobKeyVerifier string,
	currentBlobKeyVerifier string,
	requireCurrentVerifier bool,
) error {
	verifier, err := normalizeBlobKeyVerifier(blobKeyVerifier)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var storedVerifier sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT blob_key_verifier
		 FROM devices
		 WHERE account_id = $1 AND id = $2
		   AND revoked_at IS NULL
		 FOR UPDATE`,
		accountID,
		deviceID,
	).Scan(&storedVerifier); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeviceNotFound
		}
		return err
	}

	if storedVerifier.Valid && strings.TrimSpace(storedVerifier.String) != "" {
		if !requireCurrentVerifier {
			return ErrIdentityAlreadySet
		}
		stored, err := normalizeBlobKeyVerifier(storedVerifier.String)
		if err != nil {
			return ErrIdentityAuthFailed
		}
		current, err := normalizeBlobKeyVerifier(currentBlobKeyVerifier)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(current), []byte(stored)) != 1 {
			return ErrIdentityAuthFailed
		}
	} else if requireCurrentVerifier {
		return ErrIdentityNotFound
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE devices
		 SET blob_key_verifier = $3,
		     last_seen_at = NOW()
		 WHERE account_id = $1 AND id = $2
		   AND revoked_at IS NULL`,
		accountID,
		deviceID,
		verifier,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrDeviceNotFound
	}
	return tx.Commit()
}

func (s *Store) GetAccountStorage(ctx context.Context, accountID string) (AccountStorage, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AccountStorage{}, errors.New("account id is required")
	}

	var storage AccountStorage
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, provider, funding_model, wallet_address, encrypted_seed_blob,
		        seed_encryption_hint, status, last_preflight_status, last_preflight_json,
		        last_preflight_at, created_at, updated_at
		   FROM account_storage
		  WHERE account_id = $1`,
		accountID,
	).Scan(scanAccountStorageDest(&storage)...)
	if err == nil {
		storage.EncryptedSeedBackedUp = storage.EncryptedSeedBlob != nil && strings.TrimSpace(*storage.EncryptedSeedBlob) != ""
		s.applyLatestStoragePreflight(ctx, &storage)
		return storage, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AccountStorage{}, err
	}

	var createdAt time.Time
	if err := s.db.QueryRowContext(ctx, `SELECT created_at FROM accounts WHERE id = $1`, accountID).Scan(&createdAt); err != nil {
		return AccountStorage{}, err
	}
	now := time.Now().UTC()
	storage = AccountStorage{
		AccountID:       accountID,
		Provider:        "local-disk",
		FundingModel:    "operator",
		Status:          "not_configured",
		CreatedAt:       createdAt,
		UpdatedAt:       now,
		LastPreflightAt: nil,
	}
	s.applyLatestStoragePreflight(ctx, &storage)
	return storage, nil
}

func (s *Store) applyLatestStoragePreflight(ctx context.Context, storage *AccountStorage) {
	if storage == nil {
		return
	}
	var kind sql.NullString
	var status sql.NullString
	var payload sql.NullString
	var at sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT storage_preflight_kind, storage_preflight_status, storage_preflight_json,
		        storage_preflight_at
		   FROM hosts
		  WHERE storage_preflight_kind = 'sia-renterd'
		    AND last_heartbeat_at >= NOW() - INTERVAL '5 minutes'
		  ORDER BY CASE WHEN storage_preflight_status = 'ready' THEN 0 ELSE 1 END,
		           last_heartbeat_at DESC
		  LIMIT 1`,
	).Scan(&kind, &status, &payload, &at)
	if err != nil {
		return
	}
	if strings.TrimSpace(storage.Provider) == "" || storage.Provider == "local-disk" {
		storage.Provider = "sia-renterd"
		storage.FundingModel = "operator-pooled"
	}
	if status.Valid && strings.TrimSpace(storage.Status) == "not_configured" {
		storage.Status = strings.TrimSpace(status.String)
	}
	if payload.Valid && strings.TrimSpace(payload.String) != "" {
		storage.LastPreflightJSON = sanitizedStoragePreflightJSON(payload.String)
	}
	if status.Valid && strings.TrimSpace(status.String) != "" {
		storage.LastPreflightStatus = &status.String
	}
	if at.Valid {
		storage.LastPreflightAt = &at.Time
	}
	// A host heartbeat is node-controlled and is not an authenticated payment
	// instruction. Never copy its wallet address into account-facing funding or
	// wallet fields. Account wallet settings remain exclusively user supplied.
	storage.FundingAddress = nil
	_ = kind
}

func sanitizedStoragePreflightJSON(raw string) *string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil
	}
	redactStoragePaymentAddresses(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	result := string(encoded)
	return &result
}

func redactStoragePaymentAddresses(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalizedKey := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(strings.TrimSpace(key)))
			switch normalizedKey {
			case "walletaddress", "fundingaddress", "storagewalletaddress":
				delete(typed, key)
			default:
				redactStoragePaymentAddresses(child)
			}
		}
	case []any:
		for _, child := range typed {
			redactStoragePaymentAddresses(child)
		}
	}
}

func (s *Store) ListAppCatalog(ctx context.Context, accountID, search string) ([]AppCatalogEntry, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, errors.New("account id is required")
	}
	search = strings.TrimSpace(strings.ToLower(search))
	if len(search) > 80 {
		search = search[:80]
	}
	term := "%"
	if search != "" {
		term = "%" + search + "%"
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT c.package_name, c.source, c.display_name, c.summary, c.icon_url,
		        c.version_name, c.version_code, c.apk_url, c.apk_sha256, c.apk_size_bytes,
		        c.min_sdk, c.native_code, c.license, c.categories_json, c.anti_features_json,
		        c.recommended, c.catalog_updated_at,
		        EXISTS (
		            SELECT 1
		              FROM account_app_selections s
		             WHERE s.account_id = $1
		               AND s.package_name = c.package_name
		        ) AS selected
		   FROM app_catalog c
		  WHERE c.enabled = TRUE
		    AND ($2 = '%' OR LOWER(c.display_name) LIKE $2 OR LOWER(c.package_name) LIKE $2 OR LOWER(c.summary) LIKE $2)
		  ORDER BY c.recommended DESC, LOWER(c.display_name), c.package_name
		  LIMIT $3`,
		accountID,
		term,
		catalogResponseLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries, err := scanAppCatalogRows(rows)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Store) ListPublicAppCatalog(ctx context.Context, search string) ([]AppCatalogEntry, error) {
	search = strings.TrimSpace(strings.ToLower(search))
	if len(search) > 80 {
		search = search[:80]
	}
	term := "%"
	if search != "" {
		term = "%" + search + "%"
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT c.package_name, c.source, c.display_name, c.summary, c.icon_url,
		        c.version_name, c.version_code, c.apk_url, c.apk_sha256, c.apk_size_bytes,
		        c.min_sdk, c.native_code, c.license, c.categories_json, c.anti_features_json,
		        c.recommended, c.catalog_updated_at, FALSE AS selected
		   FROM app_catalog c
		  WHERE c.enabled = TRUE
		    AND ($1 = '%' OR LOWER(c.display_name) LIKE $1 OR LOWER(c.package_name) LIKE $1 OR LOWER(c.summary) LIKE $1)
		  ORDER BY c.recommended DESC, LOWER(c.display_name), c.package_name
		  LIMIT $2`,
		term,
		catalogResponseLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries, err := scanAppCatalogRows(rows)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Store) ReplaceAccountAppSelections(ctx context.Context, accountID string, packageNames []string) ([]AppCatalogEntry, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, errors.New("account id is required")
	}
	normalized, err := normalizeAppPackageSelections(packageNames)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for _, packageName := range normalized {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (
			    SELECT 1 FROM app_catalog
			     WHERE package_name = $1
			       AND enabled = TRUE
			)`,
			packageName,
		).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("app package is not available: %s", packageName)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM account_app_selections WHERE account_id = $1`, accountID); err != nil {
		return nil, err
	}
	for _, packageName := range normalized {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO account_app_selections (account_id, package_name, selected_at)
			 VALUES ($1, $2, NOW())
			 ON CONFLICT (account_id, package_name) DO NOTHING`,
			accountID,
			packageName,
		); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListAppCatalog(ctx, accountID, "")
}

func (s *Store) UpsertAppCatalogEntries(ctx context.Context, entries []AppCatalogEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	upserted := 0
	packagesBySource := make(map[string][]string)
	for _, entry := range entries {
		packageName := strings.TrimSpace(entry.PackageName)
		displayName := strings.TrimSpace(entry.DisplayName)
		if packageName == "" || displayName == "" || strings.TrimSpace(entry.APKURL) == "" || strings.TrimSpace(entry.APKSHA256) == "" {
			continue
		}
		source := defaultString(entry.Source, "fdroid")
		result, err := tx.ExecContext(ctx,
			`INSERT INTO app_catalog (
			     package_name, source, display_name, summary, icon_url, version_name,
			     version_code, apk_url, apk_sha256, apk_size_bytes, min_sdk, native_code,
			     license, categories_json, anti_features_json, recommended, enabled,
			     catalog_updated_at, updated_at
			 )
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, TRUE, $17, NOW())
			 ON CONFLICT (package_name) DO UPDATE
			 SET source = EXCLUDED.source,
			     display_name = EXCLUDED.display_name,
			     summary = EXCLUDED.summary,
			     icon_url = EXCLUDED.icon_url,
			     version_name = EXCLUDED.version_name,
			     version_code = EXCLUDED.version_code,
			     apk_url = EXCLUDED.apk_url,
			     apk_sha256 = EXCLUDED.apk_sha256,
			     apk_size_bytes = EXCLUDED.apk_size_bytes,
			     min_sdk = EXCLUDED.min_sdk,
			     native_code = EXCLUDED.native_code,
			     license = EXCLUDED.license,
			     categories_json = EXCLUDED.categories_json,
			     anti_features_json = EXCLUDED.anti_features_json,
			     recommended = EXCLUDED.recommended,
			     enabled = TRUE,
			     catalog_updated_at = EXCLUDED.catalog_updated_at,
			     updated_at = NOW()
			 WHERE EXCLUDED.version_code >= app_catalog.version_code`,
			packageName,
			source,
			displayName,
			strings.TrimSpace(entry.Summary),
			strings.TrimSpace(entry.IconURL),
			strings.TrimSpace(entry.VersionName),
			entry.VersionCode,
			strings.TrimSpace(entry.APKURL),
			strings.TrimSpace(entry.APKSHA256),
			entry.APKSizeBytes,
			entry.MinSDK,
			strings.TrimSpace(entry.NativeCode),
			strings.TrimSpace(entry.License),
			defaultString(entry.CategoriesJSON, "[]"),
			defaultString(entry.AntiFeaturesJSON, "[]"),
			entry.Recommended,
			entry.CatalogUpdatedAt,
		)
		if err != nil {
			return 0, err
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		upserted += int(rowsAffected)
		packagesBySource[source] = append(packagesBySource[source], packageName)
	}

	for source, packageNames := range packagesBySource {
		if err := disableMissingCatalogEntriesTX(ctx, tx, source, packageNames); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return upserted, nil
}

func (s *Store) DisableAppCatalogSource(ctx context.Context, source string) (int, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return 0, errors.New("app catalog source is required")
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE app_catalog
		    SET enabled = FALSE,
		        updated_at = NOW()
		  WHERE source = $1
		    AND enabled = TRUE`,
		source,
	)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	return int(rowsAffected), err
}

func disableMissingCatalogEntriesTX(ctx context.Context, tx *sql.Tx, source string, packageNames []string) error {
	source = strings.TrimSpace(source)
	if source == "" || len(packageNames) == 0 {
		return nil
	}

	args := make([]any, 0, len(packageNames)+1)
	args = append(args, source)
	placeholders := make([]string, 0, len(packageNames))
	for _, packageName := range packageNames {
		args = append(args, packageName)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE app_catalog
		    SET enabled = FALSE,
		        updated_at = NOW()
		  WHERE source = $1
		    AND enabled = TRUE
		    AND package_name NOT IN (`+strings.Join(placeholders, ", ")+`)`,
		args...,
	)
	return err
}

func scanAppCatalogRows(rows *sql.Rows) ([]AppCatalogEntry, error) {
	var entries []AppCatalogEntry
	for rows.Next() {
		var entry AppCatalogEntry
		if err := rows.Scan(
			&entry.PackageName,
			&entry.Source,
			&entry.DisplayName,
			&entry.Summary,
			&entry.IconURL,
			&entry.VersionName,
			&entry.VersionCode,
			&entry.APKURL,
			&entry.APKSHA256,
			&entry.APKSizeBytes,
			&entry.MinSDK,
			&entry.NativeCode,
			&entry.License,
			&entry.CategoriesJSON,
			&entry.AntiFeaturesJSON,
			&entry.Recommended,
			&entry.CatalogUpdatedAt,
			&entry.Selected,
		); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Store) GetAccountEntitlementSummary(ctx context.Context, accountID string) (AccountEntitlementSummary, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AccountEntitlementSummary{}, errors.New("account id is required")
	}

	var entitlement AccountEntitlement
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, source, status, runtime_limit, active_runtime_limit,
		        runtime_starts_per_day, storage_bytes_limit, trial_runtime_seconds,
		        expires_at, created_at, updated_at
		 FROM account_entitlements
		 WHERE account_id = $1`,
		accountID,
	).Scan(
		&entitlement.AccountID,
		&entitlement.Source,
		&entitlement.Status,
		&entitlement.RuntimeLimit,
		&entitlement.ActiveRuntimeLimit,
		&entitlement.RuntimeStartsPerDay,
		&entitlement.StorageBytesLimit,
		&entitlement.TrialRuntimeSeconds,
		&entitlement.ExpiresAt,
		&entitlement.CreatedAt,
		&entitlement.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return missingEntitlementSummary(accountID), nil
		}
		return AccountEntitlementSummary{}, err
	}

	summary := AccountEntitlementSummary{
		AccountID:           entitlement.AccountID,
		Source:              entitlement.Source,
		Status:              entitlement.Status,
		RuntimeLimit:        entitlement.RuntimeLimit,
		ActiveRuntimeLimit:  entitlement.ActiveRuntimeLimit,
		RuntimeStartsPerDay: entitlement.RuntimeStartsPerDay,
		StorageBytesLimit:   entitlement.StorageBytesLimit,
		TrialRuntimeSeconds: entitlement.TrialRuntimeSeconds,
		ExpiresAt:           entitlement.ExpiresAt,
		CanCreateRuntime:    true,
		CanStartRuntime:     true,
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT
		    COUNT(*) FILTER (WHERE deleted_at IS NULL AND desired_state <> 'deleted') AS runtime_count,
		    COUNT(*) FILTER (WHERE deleted_at IS NULL AND desired_state = 'running') AS active_runtime_count
		 FROM runtimes
		 WHERE account_id = $1`,
		accountID,
	).Scan(&summary.RuntimeCount, &summary.ActiveRuntimeCount); err != nil {
		return AccountEntitlementSummary{}, err
	}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtime_start_events
		 WHERE account_id = $1
		   AND created_at >= NOW() - INTERVAL '24 hours'`,
		accountID,
	).Scan(&summary.RuntimeStartsUsedToday); err != nil {
		return AccountEntitlementSummary{}, err
	}

	summary.RuntimeRemaining = remaining(summary.RuntimeLimit, summary.RuntimeCount)
	summary.ActiveRuntimeRemaining = remaining(summary.ActiveRuntimeLimit, summary.ActiveRuntimeCount)
	summary.RuntimeStartsRemainingToday = remaining(summary.RuntimeStartsPerDay, summary.RuntimeStartsUsedToday)

	if !entitlementActive(entitlement) {
		summary.blockCreate(RuntimeEntitlementRequiredCode, ErrRuntimeEntitlement.Error())
		summary.blockStart(RuntimeEntitlementRequiredCode, ErrRuntimeEntitlement.Error())
		return summary, nil
	}
	if summary.RuntimeLimit <= 0 || summary.RuntimeCount >= summary.RuntimeLimit {
		summary.blockCreate(RuntimeQuotaExceededCode, ErrRuntimeQuota.Error())
	}
	if summary.ActiveRuntimeLimit <= 0 || summary.ActiveRuntimeCount >= summary.ActiveRuntimeLimit {
		summary.blockStart(ActiveRuntimeQuotaExceededCode, ErrRuntimeActiveQuota.Error())
	}
	if summary.RuntimeStartsPerDay <= 0 || summary.RuntimeStartsUsedToday >= summary.RuntimeStartsPerDay {
		summary.blockStart(RuntimeStartQuotaExceededCode, ErrRuntimeStartQuota.Error())
	}

	return summary, nil
}

func (s *Store) UpdateAccountStorage(ctx context.Context, accountID string, input UpdateAccountStorageInput) (AccountStorage, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return AccountStorage{}, errors.New("account id is required")
	}

	normalized, err := normalizeAccountStorageInput(input)
	if err != nil {
		return AccountStorage{}, err
	}

	var storage AccountStorage
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO account_storage (
		     account_id, provider, funding_model, wallet_address, encrypted_seed_blob,
		     seed_encryption_hint, status, updated_at
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		 ON CONFLICT (account_id) DO UPDATE
		 SET provider = EXCLUDED.provider,
		     funding_model = EXCLUDED.funding_model,
		     wallet_address = EXCLUDED.wallet_address,
		     encrypted_seed_blob = EXCLUDED.encrypted_seed_blob,
		     seed_encryption_hint = EXCLUDED.seed_encryption_hint,
		     status = EXCLUDED.status,
		     updated_at = NOW()
		 RETURNING account_id, provider, funding_model, wallet_address, encrypted_seed_blob,
		           seed_encryption_hint, status, last_preflight_status, last_preflight_json,
		           last_preflight_at, created_at, updated_at`,
		accountID,
		normalized.Provider,
		normalized.FundingModel,
		nullString(normalized.WalletAddress),
		nullString(normalized.EncryptedSeedBlob),
		nullString(normalized.SeedEncryptionHint),
		normalized.Status,
	).Scan(scanAccountStorageDest(&storage)...)
	if err != nil {
		return AccountStorage{}, err
	}
	storage.EncryptedSeedBackedUp = storage.EncryptedSeedBlob != nil && strings.TrimSpace(*storage.EncryptedSeedBlob) != ""
	return storage, nil
}

func (s *Store) DevicePublicKey(ctx context.Context, accountID, deviceID string) (string, error) {
	var publicKey string
	err := s.db.QueryRowContext(ctx,
		`SELECT public_key
		 FROM devices d
		 JOIN accounts a ON a.id = d.account_id
		 WHERE d.account_id = $1
		   AND d.id = $2
		   AND a.deleted_at IS NULL
		   AND d.revoked_at IS NULL`,
		accountID,
		deviceID,
	).Scan(&publicKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrDeviceNotFound
		}
		return "", err
	}
	return strings.TrimSpace(publicKey), nil
}

func (s *Store) RememberDeviceRequestNonce(ctx context.Context, accountID, deviceID, nonce string, expiresAt time.Time) error {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	nonce = strings.TrimSpace(nonce)
	if accountID == "" || deviceID == "" || nonce == "" {
		return ErrDeviceRequestReplay
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM device_request_nonces WHERE expires_at < NOW()`); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx,
		`INSERT INTO device_request_nonces (account_id, device_id, nonce, expires_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		accountID,
		deviceID,
		nonce,
		expiresAt.UTC(),
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrDeviceRequestReplay
	}

	return tx.Commit()
}

func (s *Store) RememberNodeRequestNonce(ctx context.Context, nodeID, nonce string, expiresAt time.Time) error {
	nodeID = strings.TrimSpace(nodeID)
	nonce = strings.TrimSpace(nonce)
	if nodeID == "" || nonce == "" {
		return ErrNodeRequestReplay
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM node_request_nonces WHERE expires_at < NOW()`); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx,
		`INSERT INTO node_request_nonces (node_id, nonce, expires_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		nodeID,
		nonce,
		expiresAt.UTC(),
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrNodeRequestReplay
	}

	return tx.Commit()
}

func (s *Store) UpsertRuntimeCapability(ctx context.Context, accountID, runtimeID, capabilityID, publicKey string) (RuntimeCapability, error) {
	accountID = strings.TrimSpace(accountID)
	runtimeID = strings.TrimSpace(runtimeID)
	capabilityID = strings.TrimSpace(capabilityID)
	publicKey = strings.TrimSpace(publicKey)
	if accountID == "" || runtimeID == "" || capabilityID == "" || publicKey == "" {
		return RuntimeCapability{}, ErrRuntimeCapability
	}

	var capability RuntimeCapability
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO runtime_capabilities (id, account_id, runtime_id, public_key, scopes, expires_at, revoked_at, last_used_at)
		 SELECT $3, r.account_id, r.id, $4, 'runtime', NOW() + $5::interval, NULL, NULL
		   FROM runtimes r
		  WHERE r.account_id = $1
		    AND r.id = $2
		    AND r.deleted_at IS NULL
		    AND r.desired_state <> 'deleted'
		 ON CONFLICT (id) DO UPDATE
		    SET account_id = EXCLUDED.account_id,
		        runtime_id = EXCLUDED.runtime_id,
		        public_key = EXCLUDED.public_key,
		        scopes = EXCLUDED.scopes,
		        expires_at = EXCLUDED.expires_at,
		        revoked_at = NULL,
		        updated_at = NOW()
		 RETURNING id, account_id, runtime_id, public_key, scopes, created_at, updated_at, expires_at, revoked_at, last_used_at`,
		accountID,
		runtimeID,
		capabilityID,
		publicKey,
		postgresInterval(runtimeCapabilityTTL),
	).Scan(
		&capability.ID,
		&capability.AccountID,
		&capability.RuntimeID,
		&capability.PublicKey,
		&capability.Scopes,
		&capability.CreatedAt,
		&capability.UpdatedAt,
		&capability.ExpiresAt,
		&capability.RevokedAt,
		&capability.LastUsedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RuntimeCapability{}, ErrRuntimeNotFound
		}
		return RuntimeCapability{}, err
	}
	return capability, nil
}

func (s *Store) RuntimeCapabilityPublicKey(ctx context.Context, runtimeID, capabilityID string) (string, string, error) {
	runtimeID = strings.TrimSpace(runtimeID)
	capabilityID = strings.TrimSpace(capabilityID)
	if runtimeID == "" || capabilityID == "" {
		return "", "", ErrRuntimeCapability
	}

	var accountID string
	var publicKey string
	err := s.db.QueryRowContext(ctx,
		`SELECT c.account_id, c.public_key
		   FROM runtime_capabilities c
		   JOIN runtimes r ON r.id = c.runtime_id
		  WHERE c.id = $1
		    AND c.runtime_id = $2
		    AND c.account_id = r.account_id
		    AND c.revoked_at IS NULL
		    AND c.expires_at > NOW()
		    AND r.deleted_at IS NULL
		    AND r.desired_state <> 'deleted'`,
		capabilityID,
		runtimeID,
	).Scan(&accountID, &publicKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrRuntimeCapability
		}
		return "", "", err
	}
	return accountID, publicKey, nil
}

func (s *Store) TouchRuntimeCapability(ctx context.Context, capabilityID string) error {
	capabilityID = strings.TrimSpace(capabilityID)
	if capabilityID == "" {
		return ErrRuntimeCapability
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE runtime_capabilities
		    SET last_used_at = NOW(),
		        updated_at = NOW()
		  WHERE id = $1`,
		capabilityID,
	)
	return err
}

func (s *Store) RememberRuntimeCapabilityNonce(ctx context.Context, capabilityID, nonce string, expiresAt time.Time) error {
	capabilityID = strings.TrimSpace(capabilityID)
	nonce = strings.TrimSpace(nonce)
	if capabilityID == "" || nonce == "" {
		return ErrRuntimeCapabilityReplay
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM runtime_capability_nonces WHERE expires_at < NOW()`); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx,
		`INSERT INTO runtime_capability_nonces (capability_id, nonce, expires_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		capabilityID,
		nonce,
		expiresAt.UTC(),
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrRuntimeCapabilityReplay
	}

	return tx.Commit()
}

func (s *Store) ListActiveSessionCapabilities(ctx context.Context, accountID string) ([]ActiveRuntimeSessionSummary, []ActiveRuntimeCapabilitySummary, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil, ErrDeviceNotFound
	}

	sessionRows, err := s.db.QueryContext(ctx,
		`SELECT s.id,
		        s.runtime_id,
		        r.name,
		        s.status,
		        s.created_at,
		        s.updated_at,
		        s.expires_at,
		        s.last_client_heartbeat_at,
		        s.capability_id,
		        c.expires_at,
		        c.last_used_at
		   FROM sessions s
		   JOIN runtimes r ON r.id = s.runtime_id
		   LEFT JOIN runtime_capabilities c ON c.id = s.capability_id
		  WHERE r.account_id = $1
		    AND r.deleted_at IS NULL
		    AND s.status IN ('pending', 'active')
		    AND s.expires_at > NOW()
		  ORDER BY s.updated_at DESC`,
		accountID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer sessionRows.Close()

	var sessions []ActiveRuntimeSessionSummary
	for sessionRows.Next() {
		var session ActiveRuntimeSessionSummary
		var heartbeat sql.NullTime
		var capabilityID sql.NullString
		var capabilityExpiresAt sql.NullTime
		var capabilityLastUsedAt sql.NullTime
		if err := sessionRows.Scan(
			&session.SessionID,
			&session.RuntimeID,
			&session.RuntimeName,
			&session.Status,
			&session.CreatedAt,
			&session.UpdatedAt,
			&session.ExpiresAt,
			&heartbeat,
			&capabilityID,
			&capabilityExpiresAt,
			&capabilityLastUsedAt,
		); err != nil {
			return nil, nil, err
		}
		if heartbeat.Valid {
			session.LastClientHeartbeatAt = &heartbeat.Time
		}
		if capabilityID.Valid {
			session.CapabilityID = &capabilityID.String
		}
		if capabilityExpiresAt.Valid {
			session.CapabilityExpiresAt = &capabilityExpiresAt.Time
		}
		if capabilityLastUsedAt.Valid {
			session.CapabilityLastUsedAt = &capabilityLastUsedAt.Time
		}
		sessions = append(sessions, session)
	}
	if err := sessionRows.Err(); err != nil {
		return nil, nil, err
	}

	capabilityRows, err := s.db.QueryContext(ctx,
		`SELECT c.id,
		        c.runtime_id,
		        r.name,
		        c.scopes,
		        c.created_at,
		        c.updated_at,
		        c.expires_at,
		        c.last_used_at
		   FROM runtime_capabilities c
		   JOIN runtimes r ON r.id = c.runtime_id AND r.account_id = c.account_id
		  WHERE c.account_id = $1
		    AND c.revoked_at IS NULL
		    AND c.expires_at > NOW()
		    AND r.deleted_at IS NULL
		    AND r.desired_state <> 'deleted'
		  ORDER BY c.updated_at DESC`,
		accountID,
	)
	if err != nil {
		return nil, nil, err
	}
	defer capabilityRows.Close()

	var capabilities []ActiveRuntimeCapabilitySummary
	for capabilityRows.Next() {
		var capability ActiveRuntimeCapabilitySummary
		var lastUsedAt sql.NullTime
		if err := capabilityRows.Scan(
			&capability.ID,
			&capability.RuntimeID,
			&capability.RuntimeName,
			&capability.Scopes,
			&capability.CreatedAt,
			&capability.UpdatedAt,
			&capability.ExpiresAt,
			&lastUsedAt,
		); err != nil {
			return nil, nil, err
		}
		if lastUsedAt.Valid {
			capability.LastUsedAt = &lastUsedAt.Time
		}
		capabilities = append(capabilities, capability)
	}
	if err := capabilityRows.Err(); err != nil {
		return nil, nil, err
	}

	return sessions, capabilities, nil
}

func (s *Store) VerifyDeviceBlobKeyVerifier(ctx context.Context, accountID, deviceID, blobKeyVerifier string) error {
	_, err := s.VerifiedDeviceBlobKeyVerifier(ctx, accountID, deviceID, blobKeyVerifier)
	return err
}

func (s *Store) VerifiedDeviceBlobKeyVerifier(ctx context.Context, accountID, deviceID, blobKeyVerifier string) (string, error) {
	var storedVerifier sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT blob_key_verifier
		 FROM devices
		 WHERE account_id = $1 AND id = $2 AND revoked_at IS NULL`,
		accountID,
		deviceID,
	).Scan(&storedVerifier)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrDeviceNotFound
		}
		return "", err
	}
	if !storedVerifier.Valid || strings.TrimSpace(storedVerifier.String) == "" {
		return "", ErrIdentityNotFound
	}
	stored, err := normalizeBlobKeyVerifier(storedVerifier.String)
	if err != nil {
		return "", ErrIdentityAuthFailed
	}
	verifier, err := normalizeBlobKeyVerifier(blobKeyVerifier)
	if err != nil {
		return "", err
	}
	if subtle.ConstantTimeCompare([]byte(verifier), []byte(stored)) != 1 {
		return "", ErrIdentityAuthFailed
	}
	return stored, nil
}

func (s *Store) VerifiedAccountBlobKeyVerifier(ctx context.Context, accountID, blobKeyVerifier string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", ErrIdentityNotFound
	}
	verifier, err := normalizeBlobKeyVerifier(blobKeyVerifier)
	if err != nil {
		return "", err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT blob_key_verifier
		   FROM devices
		  WHERE account_id = $1
		    AND revoked_at IS NULL
		    AND blob_key_verifier IS NOT NULL`,
		accountID,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	foundVerifier := false
	for rows.Next() {
		var storedRaw string
		if err := rows.Scan(&storedRaw); err != nil {
			return "", err
		}
		stored, err := normalizeBlobKeyVerifier(storedRaw)
		if err != nil {
			continue
		}
		foundVerifier = true
		if subtle.ConstantTimeCompare([]byte(verifier), []byte(stored)) == 1 {
			return stored, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if !foundVerifier {
		return "", ErrIdentityNotFound
	}
	return "", ErrIdentityAuthFailed
}

func (s *Store) StartRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	return s.startRuntime(ctx, accountID, runtimeID, "")
}

func (s *Store) StartRuntimeOnHost(ctx context.Context, accountID, runtimeID, hostID string) (Runtime, error) {
	return s.startRuntime(ctx, accountID, runtimeID, strings.TrimSpace(hostID))
}

func (s *Store) startRuntime(ctx context.Context, accountID, runtimeID, requiredHostID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var current Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
		 WHERE account_id = $1
		   AND id = $2
		   AND deleted_at IS NULL
		   AND desired_state <> 'deleted'
		 FOR UPDATE`, runtimeColumns),
		accountID,
		runtimeID,
	).Scan(scanRuntimeDest(&current)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	// The row lock serializes start/stop transitions for this runtime. Once the
	// desired state is running, a repeated request is an observation, not a new
	// billable start: preserve the persona, assignment, viewer, and sessions.
	if strings.EqualFold(strings.TrimSpace(current.DesiredState), "running") {
		if err := tx.Commit(); err != nil {
			return Runtime{}, err
		}
		return current, nil
	}
	if current.CleanupPending {
		return Runtime{}, ErrRuntimeCleanupPending
	}
	if err := ensureRuntimeStartEntitlementTX(ctx, tx, accountID, runtimeID); err != nil {
		return Runtime{}, err
	}

	var hostID string
	if requiredHostID != "" {
		hostID, err = requireReadyHostTX(ctx, tx, requiredHostID)
		if err != nil {
			return Runtime{}, err
		}
	} else {
		hostID, err = pickReadyHostTX(ctx, tx, valueOrEmpty(current.HostID))
		if err != nil {
			return Runtime{}, err
		}
	}

	var viewerPort sql.NullInt32
	if current.ViewerPort != nil {
		viewerPort = sql.NullInt32{Int32: int32(*current.ViewerPort), Valid: true}
	}
	if !viewerPort.Valid {
		allocatedViewerPort, err := allocateViewerPortTX(ctx, tx)
		if err != nil {
			return Runtime{}, err
		}
		viewerPort = sql.NullInt32{Int32: int32(allocatedViewerPort), Valid: true}
	}
	rotatePersona := true
	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
			 SET host_id = $3,
			     status = 'starting',
			     desired_state = 'running',
			     operation_generation = operation_generation + 1,
				     connection_status = 'connecting',
				     viewer_port = $4,
				     wipe_requested = FALSE,
				     last_error = NULL,
				     persona_version = CASE WHEN $5 THEN persona_version + 1 ELSE persona_version END,
				     active_persona_json = CASE WHEN $5 THEN NULL ELSE active_persona_json END,
				     container_name = CASE WHEN $5 THEN NULL ELSE container_name END,
				     adb_port = CASE WHEN $5 THEN NULL ELSE adb_port END,
				     started_at = CASE WHEN $5 THEN NULL ELSE started_at END,
				     deleted_at = NULL,
				     updated_at = NOW()
			 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'
			 RETURNING %s`, runtimeColumns),
		accountID,
		runtimeID,
		hostID,
		viewerPort,
		rotatePersona,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	logMessage := fmt.Sprintf("Runtime start requested. persona_version=%d.", runtime.PersonaVersion)
	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", logMessage); err != nil {
		return Runtime{}, err
	}
	if err := appendRuntimeStartEventTX(ctx, tx, accountID, runtime.ID); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) StopRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var current Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
		 WHERE account_id = $1
		   AND id = $2
		   AND deleted_at IS NULL
		   AND desired_state <> 'deleted'
		 FOR UPDATE`, runtimeColumns),
		accountID,
		runtimeID,
	).Scan(scanRuntimeDest(&current)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}
	if strings.EqualFold(strings.TrimSpace(current.DesiredState), "stopped") {
		if err := tx.Commit(); err != nil {
			return Runtime{}, err
		}
		return current, nil
	}

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET status = 'stopping',
		     desired_state = 'stopped',
		     operation_generation = operation_generation + 1,
		     connection_status = 'disconnecting',
		     started_at = NULL,
		     updated_at = NOW()
		 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'
		 RETURNING %s`, runtimeColumns),
		accountID,
		runtimeID,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	if err := closeRuntimeSessionsTX(ctx, tx, runtime.ID, "runtime stopped"); err != nil {
		return Runtime{}, err
	}
	if _, err := revokeRuntimeCapabilitiesForRuntimeTX(ctx, tx, runtime.ID); err != nil {
		return Runtime{}, err
	}

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", "Runtime stop requested."); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) WipeRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	return s.wipeRuntime(ctx, accountID, runtimeID, "")
}

func (s *Store) WipeRuntimeOnHost(ctx context.Context, accountID, runtimeID, hostID string) (Runtime, error) {
	return s.wipeRuntime(ctx, accountID, runtimeID, strings.TrimSpace(hostID))
}

func (s *Store) wipeRuntime(ctx context.Context, accountID, runtimeID, hostID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET host_id = COALESCE(host_id, NULLIF($3, '')),
		     status = 'wiping',
		     desired_state = 'stopped',
		     operation_generation = CASE
		         WHEN wipe_requested OR status = 'wiping' THEN operation_generation
		         ELSE operation_generation + 1
		     END,
		     connection_status = 'offline',
		     started_at = NULL,
		     wipe_requested = TRUE,
		     last_error = NULL,
		     updated_at = NOW()
		 WHERE account_id = $1
			   AND id = $2
			   AND deleted_at IS NULL
			   AND desired_state <> 'deleted'
			   AND (
			       $3 = ''
			       OR host_id = $3
			       OR (
			           host_id IS NULL
			           AND (
			               blob_host_id = $3
			               OR (
			                   blob_host_id IS NULL
			                   AND (
			                       NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NULL
			                       OR COALESCE(NULLIF(TRIM(blob_store_kind), ''), 'local-disk') <> 'local-disk'
			                   )
			               )
			           )
			       )
			   )
			 RETURNING %s`, runtimeColumns),
		accountID,
		runtimeID,
		hostID,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	if err := closeRuntimeSessionsTX(ctx, tx, runtime.ID, "runtime wiped"); err != nil {
		return Runtime{}, err
	}
	if _, err := revokeRuntimeCapabilitiesForRuntimeTX(ctx, tx, runtime.ID); err != nil {
		return Runtime{}, err
	}

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "warn", "Runtime wipe requested. User data will be removed."); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) ListRuntimeLogs(ctx context.Context, accountID, runtimeID string, limit int) ([]RuntimeLogEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM runtimes
		 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'`,
		accountID,
		runtimeID,
	).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRuntimeNotFound
		}
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, runtime_id, source, level, message, created_at
		 FROM runtime_logs
		 WHERE runtime_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2`,
		runtimeID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RuntimeLogEntry
	for rows.Next() {
		var entry RuntimeLogEntry
		if err := rows.Scan(
			&entry.ID,
			&entry.RuntimeID,
			&entry.Source,
			&entry.Level,
			&entry.Message,
			&entry.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, entry)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if items == nil {
		return []RuntimeLogEntry{}, nil
	}
	return items, nil
}

func (s *Store) AppendRuntimeLog(ctx context.Context, runtimeID, source, level, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO runtime_logs (runtime_id, source, level, message)
		 VALUES ($1, $2, $3, $4)`,
		runtimeID,
		truncateRunes(defaultString(source, "system"), runtimeLogSourceRunes),
		truncateRunes(defaultString(level, "info"), runtimeLogLevelRunes),
		truncateRunes(strings.TrimSpace(message), runtimeLogMessageRunes),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM runtime_logs
		  WHERE id IN (
		      SELECT id
		        FROM runtime_logs
		       WHERE runtime_id = $1
		       ORDER BY created_at DESC, id DESC
		       OFFSET $2
		  )`,
		runtimeID,
		runtimeLogRetentionRows,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AppendSecurityEvent(ctx context.Context, event SecurityEventInput) error {
	return s.AppendSecurityEventLimited(ctx, event, 0, 0)
}

func (s *Store) AppendSecurityEventLimited(ctx context.Context, event SecurityEventInput, maxPerMinute int, retention time.Duration) error {
	tagsJSON, err := json.Marshal(event.Tags)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if retention > 0 {
		cutoff := time.Now().UTC().Add(-retention)
		if _, err := tx.ExecContext(ctx, `DELETE FROM security_events WHERE created_at < $1`, cutoff); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM security_event_ingest_limits WHERE bucket_start < $1`, cutoff); err != nil {
			return err
		}
	}

	nodeID := strings.TrimSpace(event.NodeID)
	if maxPerMinute > 0 {
		bucketStart := time.Now().UTC().Truncate(time.Minute)
		var count int
		err := tx.QueryRowContext(ctx,
			`INSERT INTO security_event_ingest_limits (node_id, bucket_start, event_count)
			 VALUES ($1, $2, 1)
			 ON CONFLICT (node_id, bucket_start) DO UPDATE
			 SET event_count = security_event_ingest_limits.event_count + 1
			 WHERE security_event_ingest_limits.event_count < $3
			 RETURNING event_count`,
			nodeID,
			bucketStart,
			maxPerMinute,
		).Scan(&count)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSecurityEventRateLimit
		}
		if err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO security_events (node_id, source, rule, priority, output, tags_json, event_json, event_time)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		nodeID,
		defaultString(event.Source, "falco"),
		defaultString(event.Rule, "security event"),
		defaultString(event.Priority, "notice"),
		strings.TrimSpace(event.Output),
		string(tagsJSON),
		defaultString(event.EventJSON, "{}"),
		event.EventTime,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, advertise_addr, relay_port, docker_socket, binder, public_key,
		        blob_store_kind, storage_preflight_kind, storage_preflight_status,
		        storage_preflight_json, storage_preflight_at, storage_wallet_address,
		        created_at, updated_at, last_heartbeat_at
		 FROM hosts ORDER BY last_heartbeat_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []Host
	for rows.Next() {
		var host Host
		if err := rows.Scan(
			&host.ID,
			&host.Name,
			&host.AdvertiseAddr,
			&host.RelayPort,
			&host.DockerSocket,
			&host.Binder,
			&host.PublicKey,
			&host.BlobStoreKind,
			&host.StoragePreflightKind,
			&host.StoragePreflightStatus,
			&host.StoragePreflightJSON,
			&host.StoragePreflightAt,
			&host.StorageWalletAddress,
			&host.CreatedAt,
			&host.UpdatedAt,
			&host.LastHeartbeatAt,
		); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}

	return hosts, rows.Err()
}

func (s *Store) GetHost(ctx context.Context, hostID string) (Host, error) {
	var host Host
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, advertise_addr, relay_port, docker_socket, binder, public_key,
		        blob_store_kind, storage_preflight_kind, storage_preflight_status,
		        storage_preflight_json, storage_preflight_at, storage_wallet_address,
		        created_at, updated_at, last_heartbeat_at
		 FROM hosts WHERE id = $1`,
		hostID,
	).Scan(
		&host.ID,
		&host.Name,
		&host.AdvertiseAddr,
		&host.RelayPort,
		&host.DockerSocket,
		&host.Binder,
		&host.PublicKey,
		&host.BlobStoreKind,
		&host.StoragePreflightKind,
		&host.StoragePreflightStatus,
		&host.StoragePreflightJSON,
		&host.StoragePreflightAt,
		&host.StorageWalletAddress,
		&host.CreatedAt,
		&host.UpdatedAt,
		&host.LastHeartbeatAt,
	)
	return host, err
}

func (s *Store) HostPublicKey(ctx context.Context, hostID string) (string, error) {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return "", ErrHostNotFound
	}

	var publicKey string
	err := s.db.QueryRowContext(ctx,
		`SELECT public_key FROM hosts WHERE id = $1`,
		hostID,
	).Scan(&publicKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrHostNotFound
		}
		return "", err
	}
	return strings.TrimSpace(publicKey), nil
}

func (s *Store) RuntimeBlobKeyTarget(ctx context.Context, accountID, runtimeID, operation string) (Runtime, Host, error) {
	accountID = strings.TrimSpace(accountID)
	runtimeID = strings.TrimSpace(runtimeID)
	operation = strings.ToLower(strings.TrimSpace(operation))
	if accountID == "" || runtimeID == "" {
		return Runtime{}, Host{}, ErrRuntimeNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, Host{}, err
	}
	defer tx.Rollback()

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
		 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'`, runtimeColumns),
		accountID,
		runtimeID,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, Host{}, ErrRuntimeNotFound
		}
		return Runtime{}, Host{}, err
	}

	var hostID string
	switch operation {
	case "start":
		hostID, err = runtimeStartHostTX(ctx, tx, runtime)
	case "stop":
		hostID = valueOrEmpty(runtime.HostID)
		if hostID == "" {
			return Runtime{}, Host{}, ErrRuntimeNotReady
		}
	case "wipe":
		hostID, err = runtimeCleanupHostTX(ctx, tx, runtime)
	case "delete":
		hostID, err = runtimeCleanupHostTX(ctx, tx, runtime)
	case "session":
		if runtime.Status != "running" || runtime.DesiredState != "running" || runtime.ConnectionStatus != "online" {
			return Runtime{}, Host{}, ErrRuntimeNotReady
		}
		hostID = valueOrEmpty(runtime.HostID)
		if hostID == "" {
			return Runtime{}, Host{}, ErrRuntimeNotReady
		}
	default:
		return Runtime{}, Host{}, ErrRuntimeNotReady
	}
	if err != nil {
		return Runtime{}, Host{}, err
	}

	host, err := getBlobKeyHostTX(ctx, tx, hostID)
	if err != nil {
		return Runtime{}, Host{}, err
	}
	if strings.TrimSpace(host.PublicKey) == "" {
		return Runtime{}, Host{}, ErrHostNotFound
	}
	if err := tx.Commit(); err != nil {
		return Runtime{}, Host{}, err
	}
	return runtime, host, nil
}

func (s *Store) PutRuntimeBlobKeyLease(ctx context.Context, handoff RuntimeBlobKeyHandoff) error {
	handoff.AccountID = strings.TrimSpace(handoff.AccountID)
	handoff.RuntimeID = strings.TrimSpace(handoff.RuntimeID)
	handoff.HostID = strings.TrimSpace(handoff.HostID)
	handoff.Operation = strings.TrimSpace(handoff.Operation)
	handoff.LeaseID = strings.TrimSpace(handoff.LeaseID)
	if handoff.AccountID == "" || handoff.RuntimeID == "" || handoff.HostID == "" ||
		handoff.Operation == "" || handoff.LeaseID == "" || !handoff.ExpiresAt.After(time.Now().UTC()) {
		return ErrRuntimeBlobKeyHandoff
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO runtime_blob_key_handoffs (
		     runtime_id, account_id, host_id, operation, lease_id, envelope_json,
		     blob_key_verifier, expires_at, activated_at, updated_at
		 )
		 SELECT r.id, r.account_id, $3, $4, $5, NULL, NULL, $6, NULL, NOW()
		   FROM runtimes r
		  WHERE r.id = $2
		    AND r.account_id = $1
		    AND r.deleted_at IS NULL
		    AND r.desired_state <> 'deleted'
		 ON CONFLICT (runtime_id) DO UPDATE
		 SET account_id = EXCLUDED.account_id,
		     host_id = EXCLUDED.host_id,
		     operation = EXCLUDED.operation,
		     lease_id = EXCLUDED.lease_id,
		     envelope_json = NULL,
		     blob_key_verifier = NULL,
		     expires_at = EXCLUDED.expires_at,
		     activated_at = NULL,
		     updated_at = NOW()`,
		handoff.AccountID,
		handoff.RuntimeID,
		handoff.HostID,
		handoff.Operation,
		handoff.LeaseID,
		handoff.ExpiresAt,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrRuntimeBlobKeyHandoff
	}
	return nil
}

func (s *Store) ActivateRuntimeBlobKeyHandoff(ctx context.Context, handoff RuntimeBlobKeyHandoff) (RuntimeBlobKeyHandoff, error) {
	handoff.AccountID = strings.TrimSpace(handoff.AccountID)
	handoff.RuntimeID = strings.TrimSpace(handoff.RuntimeID)
	handoff.HostID = strings.TrimSpace(handoff.HostID)
	handoff.Operation = strings.TrimSpace(handoff.Operation)
	handoff.LeaseID = strings.TrimSpace(handoff.LeaseID)
	handoff.EnvelopeJSON = strings.TrimSpace(handoff.EnvelopeJSON)
	handoff.BlobKeyVerifier = strings.TrimSpace(handoff.BlobKeyVerifier)
	if handoff.AccountID == "" || handoff.RuntimeID == "" || handoff.HostID == "" ||
		handoff.Operation == "" || handoff.LeaseID == "" || handoff.EnvelopeJSON == "" || handoff.BlobKeyVerifier == "" {
		return RuntimeBlobKeyHandoff{}, ErrRuntimeBlobKeyHandoff
	}

	err := s.db.QueryRowContext(ctx,
		`UPDATE runtime_blob_key_handoffs
		    SET envelope_json = $6,
		        blob_key_verifier = $7,
		        activated_at = NOW(),
		        updated_at = NOW()
		  WHERE account_id = $1
		    AND runtime_id = $2
		    AND host_id = $3
		    AND operation = $4
		    AND lease_id = $5
		    AND expires_at > NOW()
		 RETURNING expires_at`,
		handoff.AccountID,
		handoff.RuntimeID,
		handoff.HostID,
		handoff.Operation,
		handoff.LeaseID,
		handoff.EnvelopeJSON,
		handoff.BlobKeyVerifier,
	).Scan(&handoff.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeBlobKeyHandoff{}, ErrRuntimeBlobKeyHandoff
	}
	if err != nil {
		return RuntimeBlobKeyHandoff{}, err
	}
	return handoff, nil
}

func (s *Store) GetRuntimeBlobKeyHandoff(ctx context.Context, runtimeID, hostID string) (RuntimeBlobKeyHandoff, error) {
	var handoff RuntimeBlobKeyHandoff
	err := s.db.QueryRowContext(ctx,
		`SELECT account_id, runtime_id, host_id, operation, lease_id,
		        envelope_json, blob_key_verifier, expires_at
		   FROM runtime_blob_key_handoffs
		  WHERE runtime_id = $1
		    AND host_id = $2
		    AND expires_at > NOW()
		    AND NULLIF(TRIM(COALESCE(envelope_json, '')), '') IS NOT NULL
		    AND NULLIF(TRIM(COALESCE(blob_key_verifier, '')), '') IS NOT NULL`,
		strings.TrimSpace(runtimeID),
		strings.TrimSpace(hostID),
	).Scan(
		&handoff.AccountID,
		&handoff.RuntimeID,
		&handoff.HostID,
		&handoff.Operation,
		&handoff.LeaseID,
		&handoff.EnvelopeJSON,
		&handoff.BlobKeyVerifier,
		&handoff.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeBlobKeyHandoff{}, ErrRuntimeBlobKeyHandoff
	}
	return handoff, err
}

func (s *Store) ClearRuntimeBlobKeyHandoff(ctx context.Context, runtimeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runtime_blob_key_handoffs WHERE runtime_id = $1`, strings.TrimSpace(runtimeID))
	return err
}

func (s *Store) ClearAccountBlobKeyHandoffs(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runtime_blob_key_handoffs WHERE account_id = $1`, strings.TrimSpace(accountID))
	return err
}

func (s *Store) UpsertHostHeartbeat(ctx context.Context, heartbeat HostHeartbeat) (Host, error) {
	var host Host
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO hosts (
		     id, name, advertise_addr, relay_port, docker_socket, binder, public_key,
		     blob_store_kind, storage_preflight_kind, storage_preflight_status,
		     storage_preflight_json, storage_preflight_at, storage_wallet_address
		 )
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 ON CONFLICT (id) DO UPDATE
		 SET name = EXCLUDED.name,
		     advertise_addr = EXCLUDED.advertise_addr,
		     relay_port = EXCLUDED.relay_port,
		     docker_socket = EXCLUDED.docker_socket,
		     binder = EXCLUDED.binder,
		     blob_store_kind = EXCLUDED.blob_store_kind,
		     storage_preflight_kind = EXCLUDED.storage_preflight_kind,
		     storage_preflight_status = EXCLUDED.storage_preflight_status,
		     storage_preflight_json = EXCLUDED.storage_preflight_json,
		     storage_preflight_at = EXCLUDED.storage_preflight_at,
		     storage_wallet_address = EXCLUDED.storage_wallet_address,
		     public_key = CASE
		         WHEN COALESCE(hosts.public_key, '') = '' THEN EXCLUDED.public_key
		         ELSE hosts.public_key
		     END,
		     updated_at = NOW(),
		     last_heartbeat_at = NOW()
		 RETURNING id, name, advertise_addr, relay_port, docker_socket, binder, public_key,
		           blob_store_kind, storage_preflight_kind, storage_preflight_status,
		           storage_preflight_json, storage_preflight_at, storage_wallet_address,
		           created_at, updated_at, last_heartbeat_at`,
		heartbeat.ID,
		heartbeat.Name,
		heartbeat.AdvertiseAddr,
		heartbeat.RelayPort,
		heartbeat.DockerSocket,
		heartbeat.Binder,
		strings.TrimSpace(heartbeat.PublicKey),
		normalizeChoice(heartbeat.BlobStoreKind, "local-disk", "local-disk", "sia-renterd"),
		nullStringValue(heartbeat.StoragePreflightKind),
		nullStringValue(heartbeat.StoragePreflightStatus),
		nullStringValue(heartbeat.StoragePreflightJSON),
		heartbeat.StoragePreflightAt,
		nullStringValue(heartbeat.StorageWalletAddress),
	).Scan(
		&host.ID,
		&host.Name,
		&host.AdvertiseAddr,
		&host.RelayPort,
		&host.DockerSocket,
		&host.Binder,
		&host.PublicKey,
		&host.BlobStoreKind,
		&host.StoragePreflightKind,
		&host.StoragePreflightStatus,
		&host.StoragePreflightJSON,
		&host.StoragePreflightAt,
		&host.StorageWalletAddress,
		&host.CreatedAt,
		&host.UpdatedAt,
		&host.LastHeartbeatAt,
	)

	return host, err
}

// AssignPendingRemoteRuntimeCleanup lets a healthy node reclaim deletion work
// for remote object stores when legacy rows have lost both host pointers. Local
// disk snapshots remain unassigned because only their original node can safely
// locate and erase them.
func (s *Store) AssignPendingRemoteRuntimeCleanup(ctx context.Context, hostID, blobStoreKind string) (int, error) {
	hostID = strings.TrimSpace(hostID)
	blobStoreKind = strings.ToLower(strings.TrimSpace(blobStoreKind))
	if hostID == "" || blobStoreKind == "" || blobStoreKind == "local-disk" {
		return 0, nil
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE runtimes
		    SET host_id = $1,
		        status = 'deleting',
		        connection_status = 'offline',
		        updated_at = NOW()
		  WHERE host_id IS NULL
		    AND blob_host_id IS NULL
		    AND deleted_at IS NULL
		    AND desired_state = 'deleted'
		    AND NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NOT NULL
		    AND LOWER(COALESCE(NULLIF(TRIM(blob_store_kind), ''), 'local-disk')) = $2`,
		hostID,
		blobStoreKind,
	)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	return int(rowsAffected), err
}

func (s *Store) ResolveSessionRelayTarget(ctx context.Context, sessionID, relayToken string) (SessionRelayTarget, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(relayToken) == "" {
		return SessionRelayTarget{}, sql.ErrNoRows
	}
	relayTokenHash := hashRelayToken(relayToken)

	var target SessionRelayTarget
	err := s.db.QueryRowContext(ctx,
		`UPDATE sessions AS s
		 SET status = 'active',
		     updated_at = NOW(),
		     last_client_heartbeat_at = NOW(),
		     expires_at = NOW() + INTERVAL '2 minutes',
		     relay_token_consumed_at = NOW()
		 FROM runtimes AS r
		 WHERE s.id = $1
		   AND s.relay_token = $2
		   AND s.relay_token_consumed_at IS NULL
		   AND s.expires_at > NOW()
		   AND s.status IN ('pending', 'active')
		   AND r.id = s.runtime_id
		   AND r.deleted_at IS NULL
		   AND r.desired_state = 'running'
		   AND r.status = 'running'
		   AND r.connection_status = 'online'
		   AND r.host_id IS NOT NULL
		   AND r.viewer_port IS NOT NULL
		 RETURNING s.id, s.runtime_id, COALESCE(s.device_id::text, ''), r.host_id, r.viewer_port`,
		sessionID,
		relayTokenHash,
	).Scan(
		&target.SessionID,
		&target.RuntimeID,
		&target.DeviceID,
		&target.HostID,
		&target.ViewerPort,
	)
	if err != nil {
		return SessionRelayTarget{}, err
	}

	return target, nil
}

func (s *Store) CloseSession(ctx context.Context, accountID, deviceID, sessionID string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions AS s
		 SET status = 'closed',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = 'client closed'
		 FROM runtimes AS r
		 WHERE s.id = $1
		   AND s.device_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND s.status IN ('pending', 'active')`,
		sessionID,
		deviceID,
		accountID,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (s *Store) CloseSessionWithCapability(ctx context.Context, accountID, capabilityID, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx,
		`UPDATE sessions AS s
		 SET status = 'closed',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = 'client closed'
		 FROM runtimes AS r, runtime_capabilities AS c
		 WHERE s.id = $1
		   AND s.capability_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND c.id = s.capability_id
		   AND c.runtime_id = r.id
		   AND c.account_id = r.account_id
		   AND c.revoked_at IS NULL
		   AND c.expires_at > NOW()
		   AND s.status IN ('pending', 'active')`,
		sessionID,
		capabilityID,
		accountID,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrSessionNotFound
	}
	if _, err := revokeRuntimeCapabilityTX(ctx, tx, accountID, capabilityID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetSessionState(ctx context.Context, accountID, deviceID, sessionID string) (SessionState, error) {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || deviceID == "" || sessionID == "" {
		return SessionState{}, ErrSessionNotFound
	}

	var session Session
	var runtime Runtime
	dest := []any{
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	}
	dest = append(dest, scanRuntimeDest(&runtime)...)

	err := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT s.id, s.runtime_id, s.device_id, s.status, s.created_at, s.updated_at,
		                   s.last_client_heartbeat_at, s.ended_at, s.end_reason, s.expires_at,
		                   %s
		    FROM sessions AS s
		    JOIN runtimes AS r ON r.id = s.runtime_id
		   WHERE s.id = $1
		     AND s.device_id = $2
		     AND r.account_id = $3`,
			runtimeColumnsWithAlias("r"),
		),
		sessionID,
		deviceID,
		accountID,
	).Scan(dest...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionState{}, ErrSessionNotFound
		}
		return SessionState{}, err
	}

	now := time.Now().UTC()
	effectiveStatus, expired := effectiveSessionStatus(session, now)
	runtimeReady := runtimeReadyForSession(runtime)
	canResume := runtimeReady && !expired && (effectiveStatus == "pending" || effectiveStatus == "active")
	return SessionState{
		Session:         session,
		Runtime:         runtime,
		EffectiveStatus: effectiveStatus,
		IsExpired:       expired,
		RuntimeReady:    runtimeReady,
		CanResume:       canResume,
	}, nil
}

func (s *Store) GetSessionStateWithCapability(ctx context.Context, accountID, capabilityID, sessionID string) (SessionState, error) {
	accountID = strings.TrimSpace(accountID)
	capabilityID = strings.TrimSpace(capabilityID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || capabilityID == "" || sessionID == "" {
		return SessionState{}, ErrSessionNotFound
	}

	var session Session
	var runtime Runtime
	var sessionCapabilityID sql.NullString
	dest := []any{
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&sessionCapabilityID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	}
	dest = append(dest, scanRuntimeDest(&runtime)...)

	err := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT s.id, s.runtime_id, COALESCE(s.device_id::text, ''), s.capability_id, s.status, s.created_at, s.updated_at,
		                   s.last_client_heartbeat_at, s.ended_at, s.end_reason, s.expires_at,
		                   %s
		    FROM sessions AS s
		    JOIN runtimes AS r ON r.id = s.runtime_id
		    JOIN runtime_capabilities c ON c.id = s.capability_id AND c.runtime_id = r.id AND c.account_id = r.account_id
		   WHERE s.id = $1
		     AND s.capability_id = $2
		     AND r.account_id = $3
		     AND c.revoked_at IS NULL
		     AND c.expires_at > NOW()`,
			runtimeColumnsWithAlias("r"),
		),
		sessionID,
		capabilityID,
		accountID,
	).Scan(dest...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionState{}, ErrSessionNotFound
		}
		return SessionState{}, err
	}
	if sessionCapabilityID.Valid {
		session.CapabilityID = &sessionCapabilityID.String
	}

	now := time.Now().UTC()
	effectiveStatus, expired := effectiveSessionStatus(session, now)
	runtimeReady := runtimeReadyForSession(runtime)
	canResume := runtimeReady && !expired && (effectiveStatus == "pending" || effectiveStatus == "active")
	return SessionState{
		Session:         session,
		Runtime:         runtime,
		EffectiveStatus: effectiveStatus,
		IsExpired:       expired,
		RuntimeReady:    runtimeReady,
		CanResume:       canResume,
	}, nil
}

func (s *Store) IssueSessionRelayToken(ctx context.Context, accountID, deviceID, sessionID string) (Session, error) {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || deviceID == "" || sessionID == "" {
		return Session{}, ErrSessionNotFound
	}
	relayToken, err := generateRelayToken()
	if err != nil {
		return Session{}, err
	}
	relayTokenHash := hashRelayToken(relayToken)

	var session Session
	err = s.db.QueryRowContext(ctx,
		`UPDATE sessions AS s
		 SET relay_token = $4,
		     relay_token_consumed_at = NULL,
		     updated_at = NOW(),
		     expires_at = NOW() + INTERVAL '2 minutes'
		 FROM runtimes AS r, devices AS d
		 WHERE s.id = $1
		   AND s.device_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND d.id = s.device_id
		   AND d.account_id = r.account_id
		   AND d.revoked_at IS NULL
		   AND s.status IN ('pending', 'active')
		   AND s.expires_at > NOW()
		   AND r.deleted_at IS NULL
		   AND r.desired_state = 'running'
		   AND r.status = 'running'
		   AND r.connection_status = 'online'
		   AND r.host_id IS NOT NULL
		   AND r.viewer_port IS NOT NULL
		 RETURNING s.id, s.runtime_id, s.device_id, s.status, s.created_at, s.updated_at,
		           s.last_client_heartbeat_at, s.ended_at, s.end_reason, s.expires_at`,
		sessionID,
		deviceID,
		accountID,
		relayTokenHash,
	).Scan(
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, err
	}
	session.RelayToken = relayToken
	return session, nil
}

func (s *Store) IssueSessionRelayTokenWithCapability(ctx context.Context, accountID, capabilityID, sessionID string) (Session, error) {
	accountID = strings.TrimSpace(accountID)
	capabilityID = strings.TrimSpace(capabilityID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || capabilityID == "" || sessionID == "" {
		return Session{}, ErrSessionNotFound
	}
	relayToken, err := generateRelayToken()
	if err != nil {
		return Session{}, err
	}
	relayTokenHash := hashRelayToken(relayToken)

	var session Session
	var sessionCapabilityID sql.NullString
	err = s.db.QueryRowContext(ctx,
		`UPDATE sessions AS s
		 SET relay_token = $4,
		     relay_token_consumed_at = NULL,
		     updated_at = NOW(),
		     expires_at = NOW() + INTERVAL '2 minutes'
		 FROM runtimes AS r, runtime_capabilities AS c
		 WHERE s.id = $1
		   AND s.capability_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND c.id = s.capability_id
		   AND c.runtime_id = r.id
		   AND c.account_id = r.account_id
		   AND c.revoked_at IS NULL
		   AND c.expires_at > NOW()
		   AND s.status IN ('pending', 'active')
		   AND s.expires_at > NOW()
		   AND r.deleted_at IS NULL
		   AND r.desired_state = 'running'
		   AND r.status = 'running'
		   AND r.connection_status = 'online'
		   AND r.host_id IS NOT NULL
		   AND r.viewer_port IS NOT NULL
		 RETURNING s.id, s.runtime_id, COALESCE(s.device_id::text, ''), s.capability_id, s.status, s.created_at, s.updated_at,
		           s.last_client_heartbeat_at, s.ended_at, s.end_reason, s.expires_at`,
		sessionID,
		capabilityID,
		accountID,
		relayTokenHash,
	).Scan(
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&sessionCapabilityID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, err
	}
	if sessionCapabilityID.Valid {
		session.CapabilityID = &sessionCapabilityID.String
	}
	session.RelayToken = relayToken
	return session, nil
}

func (s *Store) RequireSessionRuntime(ctx context.Context, accountID, deviceID, sessionID, runtimeID string) error {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	sessionID = strings.TrimSpace(sessionID)
	runtimeID = strings.TrimSpace(runtimeID)
	if accountID == "" || deviceID == "" || sessionID == "" || runtimeID == "" {
		return ErrSessionNotFound
	}

	var found string
	if err := s.db.QueryRowContext(ctx,
		`SELECT s.runtime_id
		   FROM sessions s
		   JOIN runtimes r ON r.id = s.runtime_id
		   JOIN devices d ON d.id = s.device_id AND d.account_id = r.account_id
		  WHERE s.id = $1
		    AND s.runtime_id = $2
		    AND s.device_id = $3
		    AND r.account_id = $4
		    AND d.revoked_at IS NULL
		    AND r.deleted_at IS NULL
		    AND s.status IN ('pending', 'active')`,
		sessionID,
		runtimeID,
		deviceID,
		accountID,
	).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSessionNotFound
		}
		return err
	}
	return nil
}

func (s *Store) RequireSessionRuntimeWithCapability(ctx context.Context, accountID, capabilityID, sessionID, runtimeID string) error {
	accountID = strings.TrimSpace(accountID)
	capabilityID = strings.TrimSpace(capabilityID)
	sessionID = strings.TrimSpace(sessionID)
	runtimeID = strings.TrimSpace(runtimeID)
	if accountID == "" || capabilityID == "" || sessionID == "" || runtimeID == "" {
		return ErrSessionNotFound
	}

	var found string
	if err := s.db.QueryRowContext(ctx,
		`SELECT s.runtime_id
		   FROM sessions s
		   JOIN runtimes r ON r.id = s.runtime_id
		   JOIN runtime_capabilities c ON c.id = s.capability_id
		  WHERE s.id = $1
		    AND s.runtime_id = $2
		    AND s.capability_id = $3
		    AND r.account_id = $4
		    AND c.runtime_id = r.id
		    AND c.account_id = r.account_id
		    AND c.revoked_at IS NULL
		    AND c.expires_at > NOW()
		    AND r.deleted_at IS NULL
		    AND s.status IN ('pending', 'active')`,
		sessionID,
		runtimeID,
		capabilityID,
		accountID,
	).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSessionNotFound
		}
		return err
	}
	return nil
}

func (s *Store) EndSessionAndStopRuntime(ctx context.Context, accountID, deviceID, sessionID, runtimeID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var matchedRuntimeID string
	if err := tx.QueryRowContext(ctx,
		`UPDATE sessions AS s
		 SET status = 'closed',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = 'client closed'
		 FROM runtimes AS r
		 WHERE s.id = $1
		   AND s.device_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND s.runtime_id = $4
		   AND s.status IN ('pending', 'active')
		 RETURNING s.runtime_id`,
		sessionID,
		deviceID,
		accountID,
		runtimeID,
	).Scan(&matchedRuntimeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrSessionNotFound
		}
		return Runtime{}, err
	}

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET status = CASE
		         WHEN host_id IS NULL THEN 'stopped'
		         ELSE 'stopping'
		     END,
		     desired_state = 'stopped',
		     operation_generation = CASE
		         WHEN desired_state = 'stopped' THEN operation_generation
		         ELSE operation_generation + 1
		     END,
		     connection_status = CASE
		         WHEN host_id IS NULL THEN 'offline'
		         ELSE 'disconnecting'
		     END,
		     started_at = NULL,
		     last_error = NULL,
		     updated_at = NOW()
		 WHERE account_id = $1
		   AND id = $2
		   AND deleted_at IS NULL
		   AND desired_state <> 'deleted'
		 RETURNING %s`, runtimeColumns),
		accountID,
		matchedRuntimeID,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", "Session closed and runtime stop queued."); err != nil {
		return Runtime{}, err
	}
	if _, err := revokeRuntimeCapabilitiesForRuntimeTX(ctx, tx, runtime.ID); err != nil {
		return Runtime{}, err
	}
	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}
	return runtime, nil
}

func (s *Store) EndSessionAndStopRuntimeWithCapability(ctx context.Context, accountID, capabilityID, sessionID, runtimeID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var matchedRuntimeID string
	if err := tx.QueryRowContext(ctx,
		`UPDATE sessions AS s
		 SET status = 'closed',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = 'client closed'
		 FROM runtimes AS r, runtime_capabilities AS c
		 WHERE s.id = $1
		   AND s.capability_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND s.runtime_id = $4
		   AND c.id = s.capability_id
		   AND c.runtime_id = r.id
		   AND c.account_id = r.account_id
		   AND c.revoked_at IS NULL
		   AND c.expires_at > NOW()
		   AND s.status IN ('pending', 'active')
		 RETURNING s.runtime_id`,
		sessionID,
		capabilityID,
		accountID,
		runtimeID,
	).Scan(&matchedRuntimeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrSessionNotFound
		}
		return Runtime{}, err
	}

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET status = CASE
		         WHEN host_id IS NULL THEN 'stopped'
		         ELSE 'stopping'
		     END,
		     desired_state = 'stopped',
		     operation_generation = CASE
		         WHEN desired_state = 'stopped' THEN operation_generation
		         ELSE operation_generation + 1
		     END,
		     connection_status = CASE
		         WHEN host_id IS NULL THEN 'offline'
		         ELSE 'disconnecting'
		     END,
		     started_at = NULL,
		     last_error = NULL,
		     updated_at = NOW()
		 WHERE account_id = $1
		   AND id = $2
		   AND deleted_at IS NULL
		   AND desired_state <> 'deleted'
		 RETURNING %s`, runtimeColumns),
		accountID,
		matchedRuntimeID,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", "Session closed and runtime stop queued."); err != nil {
		return Runtime{}, err
	}
	if _, err := revokeRuntimeCapabilitiesForRuntimeTX(ctx, tx, runtime.ID); err != nil {
		return Runtime{}, err
	}
	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}
	return runtime, nil
}

func (s *Store) HeartbeatSession(ctx context.Context, accountID, deviceID, sessionID string) (Session, error) {
	accountID = strings.TrimSpace(accountID)
	deviceID = strings.TrimSpace(deviceID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || deviceID == "" || sessionID == "" {
		return Session{}, ErrSessionNotFound
	}

	var session Session
	err := s.db.QueryRowContext(ctx,
		`UPDATE sessions AS s
		 SET updated_at = NOW(),
		     last_client_heartbeat_at = CASE
		         WHEN s.status = 'active' THEN NOW()
		         ELSE s.last_client_heartbeat_at
		     END,
		     expires_at = CASE
		         WHEN s.status = 'active' THEN NOW() + INTERVAL '2 minutes'
		         ELSE s.expires_at
		     END
		 FROM runtimes AS r
		 WHERE s.id = $1
		   AND s.device_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND s.status IN ('pending', 'active')
		   AND s.expires_at > NOW()
		   AND r.deleted_at IS NULL
		   AND r.desired_state <> 'deleted'
		 RETURNING s.id, s.runtime_id, s.device_id, s.status, s.created_at, s.updated_at,
		           s.last_client_heartbeat_at, s.ended_at, s.end_reason, s.expires_at`,
		sessionID,
		deviceID,
		accountID,
	).Scan(
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, err
	}
	return session, nil
}

func (s *Store) HeartbeatSessionWithCapability(ctx context.Context, accountID, capabilityID, sessionID string) (Session, error) {
	accountID = strings.TrimSpace(accountID)
	capabilityID = strings.TrimSpace(capabilityID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID == "" || capabilityID == "" || sessionID == "" {
		return Session{}, ErrSessionNotFound
	}

	var session Session
	var sessionCapabilityID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`UPDATE sessions AS s
		 SET updated_at = NOW(),
		     last_client_heartbeat_at = CASE
		         WHEN s.status = 'active' THEN NOW()
		         ELSE s.last_client_heartbeat_at
		     END,
		     expires_at = CASE
		         WHEN s.status = 'active' THEN NOW() + INTERVAL '2 minutes'
		         ELSE s.expires_at
		     END
		 FROM runtimes AS r, runtime_capabilities AS c
		 WHERE s.id = $1
		   AND s.capability_id = $2
		   AND r.id = s.runtime_id
		   AND r.account_id = $3
		   AND c.id = s.capability_id
		   AND c.runtime_id = r.id
		   AND c.account_id = r.account_id
		   AND c.revoked_at IS NULL
		   AND c.expires_at > NOW()
		   AND s.status IN ('pending', 'active')
		   AND s.expires_at > NOW()
		   AND r.deleted_at IS NULL
		   AND r.desired_state <> 'deleted'
		 RETURNING s.id, s.runtime_id, COALESCE(s.device_id::text, ''), s.capability_id, s.status, s.created_at, s.updated_at,
		           s.last_client_heartbeat_at, s.ended_at, s.end_reason, s.expires_at`,
		sessionID,
		capabilityID,
		accountID,
	).Scan(
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&sessionCapabilityID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrSessionNotFound
		}
		return Session{}, err
	}
	if sessionCapabilityID.Valid {
		session.CapabilityID = &sessionCapabilityID.String
	}
	return session, nil
}

func (s *Store) ReapStaleSessions(ctx context.Context, activeSessionTimeout, runtimeIdleTimeout time.Duration) (SessionReapResult, error) {
	if activeSessionTimeout <= 0 {
		activeSessionTimeout = 2 * time.Minute
	}
	if runtimeIdleTimeout <= 0 {
		runtimeIdleTimeout = 3 * time.Minute
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionReapResult{}, err
	}
	defer tx.Rollback()

	expiredPending, err := queryStringColumnTX(ctx, tx,
		`UPDATE sessions
		 SET status = 'expired',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = 'session expired before relay attach'
		 WHERE status = 'pending'
		   AND expires_at <= NOW()
		 RETURNING id`,
	)
	if err != nil {
		return SessionReapResult{}, err
	}

	staleActive, err := queryStringColumnTX(ctx, tx,
		`UPDATE sessions
		 SET status = 'stale',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = 'client heartbeat stale'
		 WHERE status = 'active'
		   AND (
		       expires_at <= NOW()
		       OR COALESCE(last_client_heartbeat_at, updated_at, created_at) < NOW() - $1::interval
		   )
		 RETURNING id`,
		postgresInterval(activeSessionTimeout),
	)
	if err != nil {
		return SessionReapResult{}, err
	}

	stoppedRuntimeIDs, err := queryStringColumnTX(ctx, tx,
		`WITH latest_session AS (
		     SELECT runtime_id, MAX(updated_at) AS last_session_at
		       FROM sessions
		      GROUP BY runtime_id
		 ),
		 candidates AS (
		     SELECT r.id
		       FROM runtimes r
		       LEFT JOIN latest_session ls ON ls.runtime_id = r.id
		      WHERE r.deleted_at IS NULL
		        AND r.desired_state = 'running'
		        AND r.status IN ('starting', 'running', 'error')
		        AND COALESCE(ls.last_session_at, r.updated_at) < NOW() - $2::interval
		        AND NOT EXISTS (
		            SELECT 1
		              FROM sessions live
		             WHERE live.runtime_id = r.id
		               AND (
		                    (live.status = 'pending' AND live.expires_at > NOW())
		                    OR (
		                        live.status = 'active'
		                        AND live.expires_at > NOW()
		                        AND COALESCE(live.last_client_heartbeat_at, live.updated_at, live.created_at) >= NOW() - $1::interval
		                    )
		               )
		        )
		      FOR UPDATE
		 ),
		 updated AS (
		     UPDATE runtimes r
		        SET desired_state = 'stopped',
		            operation_generation = r.operation_generation + 1,
		            connection_status = 'offline',
		            started_at = NULL,
		            last_error = NULL,
		            updated_at = NOW()
		       FROM candidates c
		      WHERE r.id = c.id
		      RETURNING r.id
		 )
		 SELECT id FROM updated`,
		postgresInterval(activeSessionTimeout),
		postgresInterval(runtimeIdleTimeout),
	)
	if err != nil {
		return SessionReapResult{}, err
	}

	revokedStoppedRuntimeCapabilities := 0
	for _, runtimeID := range stoppedRuntimeIDs {
		if err := appendRuntimeLogTX(ctx, tx, runtimeID, "system", "warn", "Runtime stop queued because no active client session is heartbeating."); err != nil {
			return SessionReapResult{}, err
		}
		revokedCount, err := revokeRuntimeCapabilitiesForRuntimeTX(ctx, tx, runtimeID)
		if err != nil {
			return SessionReapResult{}, err
		}
		revokedStoppedRuntimeCapabilities += revokedCount
	}

	revokedSessionCapabilities, err := revokeEndedSessionCapabilitiesTX(ctx, tx)
	if err != nil {
		return SessionReapResult{}, err
	}
	revokedExpiredCapabilities, err := revokeExpiredRuntimeCapabilitiesTX(ctx, tx)
	if err != nil {
		return SessionReapResult{}, err
	}
	prunedRuntimeCapabilityNonces, err := pruneExpiredRuntimeCapabilityNoncesTX(ctx, tx)
	if err != nil {
		return SessionReapResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return SessionReapResult{}, err
	}

	return SessionReapResult{
		ExpiredPendingSessions:        len(expiredPending),
		StaleActiveSessions:           len(staleActive),
		RevokedRuntimeCapabilities:    revokedStoppedRuntimeCapabilities + revokedSessionCapabilities + revokedExpiredCapabilities,
		PrunedRuntimeCapabilityNonces: prunedRuntimeCapabilityNonces,
		StoppedRuntimeIDs:             stoppedRuntimeIDs,
	}, nil
}

func (s *Store) ListAssignedRuntimes(ctx context.Context, hostID string) ([]Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := allocateMissingViewerPortsForHostTX(ctx, tx, hostID); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
		 WHERE host_id = $1
		   AND deleted_at IS NULL
		   AND (desired_state IN ('running', 'stopped', 'deleted')
		        OR status IN ('starting', 'running', 'stopping', 'stopped', 'wiping', 'deleting', 'error'))
		 ORDER BY updated_at DESC`, runtimeColumns),
		hostID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runtimes []Runtime
	for rows.Next() {
		var runtime Runtime
		if err := rows.Scan(scanRuntimeDest(&runtime)...); err != nil {
			return nil, err
		}
		runtimes = append(runtimes, runtime)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := attachSelectedAppsToRuntimesTX(ctx, tx, runtimes); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if runtimes == nil {
		return []Runtime{}, nil
	}
	return runtimes, nil
}

func attachSelectedAppsToRuntimesTX(ctx context.Context, tx *sql.Tx, runtimes []Runtime) error {
	if len(runtimes) == 0 {
		return nil
	}
	appsByAccount := make(map[string][]AppCatalogEntry)
	for i := range runtimes {
		accountID := strings.TrimSpace(runtimes[i].AccountID)
		if accountID == "" {
			continue
		}
		apps, ok := appsByAccount[accountID]
		if !ok {
			var err error
			apps, err = selectedAppsForAccountTX(ctx, tx, accountID)
			if err != nil {
				return err
			}
			appsByAccount[accountID] = apps
		}
		runtimes[i].SelectedApps = apps
	}
	return nil
}

func selectedAppsForAccountTX(ctx context.Context, tx *sql.Tx, accountID string) ([]AppCatalogEntry, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT c.package_name, c.source, c.display_name, c.summary, c.icon_url,
		        c.version_name, c.version_code, c.apk_url, c.apk_sha256, c.apk_size_bytes,
		        c.min_sdk, c.native_code, c.license, c.categories_json, c.anti_features_json,
		        c.recommended, c.catalog_updated_at, TRUE AS selected
		   FROM account_app_selections s
		   JOIN app_catalog c ON c.package_name = s.package_name
		  WHERE s.account_id = $1
		    AND c.enabled = TRUE
		  ORDER BY c.recommended DESC, c.display_name ASC
		  LIMIT 32`,
		accountID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAppCatalogRows(rows)
}

func allocateMissingViewerPortsForHostTX(ctx context.Context, tx *sql.Tx, hostID string) error {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM runtimes
		 WHERE host_id = $1
		   AND desired_state = 'running'
		   AND deleted_at IS NULL
		   AND viewer_port IS NULL
		 ORDER BY updated_at
		 FOR UPDATE`,
		hostID,
	)
	if err != nil {
		return err
	}

	var runtimeIDs []string
	for rows.Next() {
		var runtimeID string
		if err := rows.Scan(&runtimeID); err != nil {
			rows.Close()
			return err
		}
		runtimeIDs = append(runtimeIDs, runtimeID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, runtimeID := range runtimeIDs {
		viewerPort, err := allocateViewerPortTX(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE runtimes
			 SET viewer_port = $2,
			     last_error = NULL,
			     updated_at = NOW()
			 WHERE id = $1
			   AND host_id = $3
			   AND desired_state = 'running'
			   AND deleted_at IS NULL
			   AND viewer_port IS NULL`,
			runtimeID,
			viewerPort,
			hostID,
		); err != nil {
			return err
		}
		if err := appendRuntimeLogTX(ctx, tx, runtimeID, "system", "info", fmt.Sprintf("Viewer port %d restored for running runtime assignment.", viewerPort)); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) RequireRuntimeAssignedToHost(ctx context.Context, runtimeID, hostID string) error {
	runtimeID = strings.TrimSpace(runtimeID)
	hostID = strings.TrimSpace(hostID)
	if runtimeID == "" || hostID == "" {
		return ErrRuntimeNotFound
	}

	var found string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM runtimes
		 WHERE id = $1
		   AND host_id = $2
		   AND deleted_at IS NULL
		   AND desired_state <> 'deleted'`,
		runtimeID,
		hostID,
	).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRuntimeNotFound
		}
		return err
	}
	return nil
}

func (s *Store) UpdateRuntimeObservation(ctx context.Context, runtimeID string, observation RuntimeObservation) error {
	if observation.HostID == "" {
		return errors.New("host id is required")
	}
	if observation.OperationGeneration <= 0 {
		return ErrRuntimeObservationStale
	}

	if observation.Deleted {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var accountID string
		err = tx.QueryRowContext(ctx,
			`UPDATE runtimes
			 SET status = 'deleted',
			     desired_state = 'deleted',
			     connection_status = 'offline',
			     host_id = NULL,
				     active_persona_json = NULL,
				     blob_store_kind = NULL,
				     blob_manifest_json = NULL,
				     blob_host_id = NULL,
				     container_name = NULL,
			     adb_port = NULL,
			     viewer_port = NULL,
			     started_at = NULL,
			     wipe_requested = FALSE,
			     last_error = $3,
			     deleted_at = NOW(),
			     updated_at = NOW()
			 WHERE id = $1
			   AND host_id = $2
			   AND operation_generation = $4
			   AND deleted_at IS NULL
			 RETURNING account_id`,
			runtimeID,
			observation.HostID,
			nullString(observation.LastError),
			observation.OperationGeneration,
		).Scan(&accountID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrRuntimeObservationStale
			}
			return err
		}
		if err := deleteAccountIfRuntimeCleanupDoneTX(ctx, tx, accountID); err != nil {
			return err
		}
		return tx.Commit()
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE runtimes
		 SET status = $3,
		     connection_status = $4,
		     host_id = CASE
		         WHEN desired_state <> 'deleted'
		          AND (
		              $3 = 'provisioned'
		              OR ($16 AND desired_state <> 'running' AND $3 IN ('stopped', 'error'))
		          ) THEN NULL
		         ELSE host_id
		     END,
		     container_name = $5,
		     adb_port = $6,
		     viewer_port = CASE
			         WHEN $3 IN ('stopped', 'provisioned') OR ($3 = 'error' AND desired_state <> 'running') THEN NULL
			         ELSE viewer_port
			     END,
			     last_error = $7,
			     blob_last_snapshot_at = COALESCE($8, blob_last_snapshot_at),
			     wipe_requested = CASE WHEN $9 THEN FALSE ELSE wipe_requested END,
			     active_persona_json = CASE WHEN $10 THEN NULL ELSE COALESCE($11, active_persona_json) END,
			     blob_store_kind = CASE WHEN $12 THEN NULL ELSE COALESCE($13, blob_store_kind) END,
			     blob_manifest_json = CASE WHEN $12 THEN NULL ELSE COALESCE($14, blob_manifest_json) END,
			     blob_host_id = CASE
			         WHEN $12 THEN NULL
			         WHEN NULLIF(TRIM(COALESCE($14, '')), '') IS NOT NULL THEN $2
			         ELSE blob_host_id
			     END,
			     started_at = CASE
		         WHEN $3 = 'running' AND $4 = 'online' THEN COALESCE(started_at, NOW())
		         WHEN $3 IN ('stopped', 'provisioned', 'deleted') OR ($3 = 'error' AND desired_state <> 'running') THEN NULL
		         ELSE started_at
		     END,
		     load_average = COALESCE($15, load_average),
		     cleanup_pending = CASE
		         WHEN $16 THEN FALSE
		         WHEN $3 = 'stopped' AND desired_state <> 'running' THEN TRUE
		         ELSE cleanup_pending
		     END,
		     updated_at = NOW()
		 WHERE id = $1
		   AND host_id = $2
		   AND operation_generation = $17
		   AND deleted_at IS NULL`,
		runtimeID,
		observation.HostID,
		defaultString(observation.Status, "stopped"),
		defaultString(observation.ConnectionStatus, "offline"),
		nullString(observation.ContainerName),
		nullInt(observation.ADBPort),
		nullString(observation.LastError),
		nullTime(observation.BlobLastSnapshotAt),
		observation.ClearWipeRequested,
		observation.ClearActivePersona,
		nullString(observation.ActivePersonaJSON),
		observation.ClearBlobManifest,
		nullString(observation.BlobStoreKind),
		nullString(observation.BlobManifestJSON),
		nullFloat64(observation.LoadAverage),
		observation.CleanupComplete,
		observation.OperationGeneration,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrRuntimeObservationStale
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, deviceID, runtimeID string) (Session, error) {
	var status string
	var desiredState string
	var connectionStatus string
	if err := s.db.QueryRowContext(ctx,
		`SELECT r.status, r.desired_state, r.connection_status
		 FROM runtimes r
		 JOIN devices d ON d.account_id = r.account_id
		 WHERE r.id = $1
		   AND d.id = $2
		   AND d.revoked_at IS NULL
		   AND r.deleted_at IS NULL
		   AND r.desired_state <> 'deleted'`,
		runtimeID,
		deviceID,
	).Scan(&status, &desiredState, &connectionStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrRuntimeNotFound
		}
		return Session{}, err
	}

	if desiredState != "running" || status != "running" || connectionStatus != "online" {
		return Session{}, ErrRuntimeNotReady
	}

	sessionID := uuid.NewString()
	relayToken, err := generateRelayToken()
	if err != nil {
		return Session{}, err
	}
	relayTokenHash := hashRelayToken(relayToken)
	expiresAt := time.Now().UTC().Add(sessionAttachTTL)

	var session Session
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO sessions (id, runtime_id, device_id, status, relay_token, expires_at, last_client_heartbeat_at)
		 VALUES ($1, $2, $3, $4, $5, $6, NOW())
		 ON CONFLICT (runtime_id) WHERE status IN ('pending', 'active') DO UPDATE
		 SET device_id = EXCLUDED.device_id,
		     capability_id = NULL,
		     status = CASE WHEN sessions.expires_at <= NOW() THEN EXCLUDED.status ELSE sessions.status END,
		     relay_token = EXCLUDED.relay_token,
		     relay_token_consumed_at = NULL,
		     expires_at = EXCLUDED.expires_at,
		     created_at = CASE WHEN sessions.expires_at <= NOW() THEN NOW() ELSE sessions.created_at END,
		     last_client_heartbeat_at = NOW(),
		     ended_at = NULL,
		     end_reason = NULL,
		     updated_at = NOW()
		 WHERE sessions.expires_at <= NOW()
		 RETURNING id, runtime_id, device_id, status, created_at, updated_at,
		           last_client_heartbeat_at, ended_at, end_reason, expires_at`,
		sessionID, runtimeID, deviceID, "pending", relayTokenHash, expiresAt,
	).Scan(
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionAlreadyActive
	}
	if err != nil {
		return Session{}, err
	}
	session.RelayToken = relayToken

	return session, nil
}

func (s *Store) CreateSessionWithCapability(ctx context.Context, accountID, capabilityID, runtimeID string) (Session, error) {
	accountID = strings.TrimSpace(accountID)
	capabilityID = strings.TrimSpace(capabilityID)
	runtimeID = strings.TrimSpace(runtimeID)
	if accountID == "" || capabilityID == "" || runtimeID == "" {
		return Session{}, ErrRuntimeCapability
	}

	var status string
	var desiredState string
	var connectionStatus string
	if err := s.db.QueryRowContext(ctx,
		`SELECT r.status, r.desired_state, r.connection_status
		   FROM runtimes r
		   JOIN runtime_capabilities c ON c.runtime_id = r.id AND c.account_id = r.account_id
		  WHERE r.id = $1
		    AND r.account_id = $2
		    AND c.id = $3
		    AND c.revoked_at IS NULL
		    AND c.expires_at > NOW()
		    AND r.deleted_at IS NULL
		    AND r.desired_state <> 'deleted'`,
		runtimeID,
		accountID,
		capabilityID,
	).Scan(&status, &desiredState, &connectionStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrRuntimeNotFound
		}
		return Session{}, err
	}

	if desiredState != "running" || status != "running" || connectionStatus != "online" {
		return Session{}, ErrRuntimeNotReady
	}

	sessionID := uuid.NewString()
	relayToken, err := generateRelayToken()
	if err != nil {
		return Session{}, err
	}
	relayTokenHash := hashRelayToken(relayToken)
	expiresAt := time.Now().UTC().Add(sessionAttachTTL)

	var session Session
	var sessionCapabilityID string
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO sessions (id, runtime_id, device_id, capability_id, status, relay_token, expires_at, last_client_heartbeat_at)
		 VALUES ($1, $2, NULL, $3, $4, $5, $6, NOW())
		 ON CONFLICT (runtime_id) WHERE status IN ('pending', 'active') DO UPDATE
		 SET device_id = NULL,
		     capability_id = EXCLUDED.capability_id,
		     status = CASE WHEN sessions.expires_at <= NOW() THEN EXCLUDED.status ELSE sessions.status END,
		     relay_token = EXCLUDED.relay_token,
		     relay_token_consumed_at = NULL,
		     expires_at = EXCLUDED.expires_at,
		     created_at = CASE WHEN sessions.expires_at <= NOW() THEN NOW() ELSE sessions.created_at END,
		     last_client_heartbeat_at = NOW(),
		     ended_at = NULL,
		     end_reason = NULL,
		     updated_at = NOW()
		 WHERE sessions.expires_at <= NOW()
		 RETURNING id, runtime_id, COALESCE(device_id::text, ''), capability_id, status, created_at, updated_at,
		           last_client_heartbeat_at, ended_at, end_reason, expires_at`,
		sessionID, runtimeID, capabilityID, "pending", relayTokenHash, expiresAt,
	).Scan(
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&sessionCapabilityID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.LastClientHeartbeatAt,
		&session.EndedAt,
		&session.EndReason,
		&session.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionAlreadyActive
	}
	if err != nil {
		return Session{}, err
	}
	session.RelayToken = relayToken
	session.CapabilityID = &sessionCapabilityID

	return session, nil
}

// AbortPendingSession closes only the exact, still-pending reservation created
// for the supplied one-time relay token. The token predicate prevents one
// concurrent retry from aborting a newer reservation for the same session.
func (s *Store) AbortPendingSession(ctx context.Context, sessionID, relayToken, reason string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	relayToken = strings.TrimSpace(relayToken)
	if sessionID == "" || relayToken == "" {
		return false, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "session setup aborted"
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions
		 SET status = 'closed',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = $3
		 WHERE id = $1
		   AND relay_token = $2
		   AND status = 'pending'`,
		sessionID,
		hashRelayToken(relayToken),
		reason,
	)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	return rowsAffected > 0, err
}

func pickReadyHostTX(ctx context.Context, tx *sql.Tx, preferredHostID string) (string, error) {
	if strings.TrimSpace(preferredHostID) != "" {
		hostID, err := requireReadyHostTX(ctx, tx, preferredHostID)
		if err == nil {
			return hostID, nil
		}
		if err != ErrNoReadyHost {
			return "", err
		}
	}

	var hostID string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM hosts
		 WHERE docker_socket = TRUE
		   AND binder = TRUE
		   AND last_heartbeat_at >= NOW() - INTERVAL '2 minutes'
		 ORDER BY last_heartbeat_at DESC
		 LIMIT 1`,
	).Scan(&hostID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoReadyHost
		}
		return "", err
	}
	return hostID, nil
}

func runtimeStartHostTX(ctx context.Context, tx *sql.Tx, runtime Runtime) (string, error) {
	currentHostID := valueOrEmpty(runtime.HostID)
	if currentHostID != "" {
		return pickReadyHostTX(ctx, tx, currentHostID)
	}
	if runtimeUsesNodeLocalBlob(runtime) {
		blobHostID := valueOrEmpty(runtime.BlobHostID)
		if blobHostID == "" {
			return "", ErrRuntimeBlobOwner
		}
		return requireReadyHostTX(ctx, tx, blobHostID)
	}
	return pickReadyHostTX(ctx, tx, "")
}

func runtimeCleanupHostTX(ctx context.Context, tx *sql.Tx, runtime Runtime) (string, error) {
	currentHostID := valueOrEmpty(runtime.HostID)
	if currentHostID != "" {
		return currentHostID, nil
	}
	blobHostID := valueOrEmpty(runtime.BlobHostID)
	if blobHostID != "" {
		return blobHostID, nil
	}
	if runtimeUsesNodeLocalBlob(runtime) {
		return "", ErrRuntimeBlobOwner
	}
	return pickReadyHostTX(ctx, tx, "")
}

func runtimeUsesNodeLocalBlob(runtime Runtime) bool {
	if valueOrEmpty(runtime.BlobManifestJSON) == "" {
		return false
	}
	kind := strings.ToLower(valueOrEmpty(runtime.BlobStoreKind))
	return kind == "" || kind == "local-disk"
}

func requireReadyHostTX(ctx context.Context, tx *sql.Tx, hostID string) (string, error) {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return "", ErrNoReadyHost
	}
	var found string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM hosts
		 WHERE id = $1
		   AND docker_socket = TRUE
		   AND binder = TRUE
		   AND last_heartbeat_at >= NOW() - INTERVAL '2 minutes'`,
		hostID,
	).Scan(&found)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNoReadyHost
		}
		return "", err
	}
	return found, nil
}

func getBlobKeyHostTX(ctx context.Context, tx *sql.Tx, hostID string) (Host, error) {
	var host Host
	err := tx.QueryRowContext(ctx,
		`SELECT id, name, advertise_addr, relay_port, docker_socket, binder, public_key,
		        blob_store_kind, storage_preflight_kind, storage_preflight_status,
		        storage_preflight_json, storage_preflight_at, storage_wallet_address,
		        created_at, updated_at, last_heartbeat_at
		 FROM hosts WHERE id = $1`,
		strings.TrimSpace(hostID),
	).Scan(
		&host.ID,
		&host.Name,
		&host.AdvertiseAddr,
		&host.RelayPort,
		&host.DockerSocket,
		&host.Binder,
		&host.PublicKey,
		&host.BlobStoreKind,
		&host.StoragePreflightKind,
		&host.StoragePreflightStatus,
		&host.StoragePreflightJSON,
		&host.StoragePreflightAt,
		&host.StorageWalletAddress,
		&host.CreatedAt,
		&host.UpdatedAt,
		&host.LastHeartbeatAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Host{}, ErrHostNotFound
		}
		return Host{}, err
	}
	return host, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func ensureRuntimeCreateEntitlementTX(ctx context.Context, tx *sql.Tx, accountID string) error {
	entitlement, err := accountEntitlementTX(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if !entitlementActive(entitlement) {
		return ErrRuntimeEntitlement
	}
	if entitlement.RuntimeLimit <= 0 {
		return ErrRuntimeQuota
	}

	var runtimeCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtimes
		 WHERE account_id = $1
		   AND deleted_at IS NULL
		   AND desired_state <> 'deleted'`,
		accountID,
	).Scan(&runtimeCount); err != nil {
		return err
	}
	if runtimeCount >= entitlement.RuntimeLimit {
		return ErrRuntimeQuota
	}
	return nil
}

func ensureRuntimeStartEntitlementTX(ctx context.Context, tx *sql.Tx, accountID, runtimeID string) error {
	entitlement, err := accountEntitlementTX(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if !entitlementActive(entitlement) {
		return ErrRuntimeEntitlement
	}
	if entitlement.ActiveRuntimeLimit <= 0 {
		return ErrRuntimeActiveQuota
	}

	var activeRuntimeCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtimes
		 WHERE account_id = $1
		   AND id <> $2
		   AND deleted_at IS NULL
		   AND desired_state = 'running'`,
		accountID,
		runtimeID,
	).Scan(&activeRuntimeCount); err != nil {
		return err
	}
	if activeRuntimeCount >= entitlement.ActiveRuntimeLimit {
		return ErrRuntimeActiveQuota
	}

	if entitlement.RuntimeStartsPerDay <= 0 {
		return ErrRuntimeStartQuota
	}
	var startsToday int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runtime_start_events
		 WHERE account_id = $1
		   AND created_at >= NOW() - INTERVAL '24 hours'`,
		accountID,
	).Scan(&startsToday); err != nil {
		return err
	}
	if startsToday >= entitlement.RuntimeStartsPerDay {
		return ErrRuntimeStartQuota
	}
	return nil
}

func accountEntitlementTX(ctx context.Context, tx *sql.Tx, accountID string) (AccountEntitlement, error) {
	var entitlement AccountEntitlement
	if err := tx.QueryRowContext(ctx,
		`SELECT account_id, source, status, runtime_limit, active_runtime_limit,
		        runtime_starts_per_day, storage_bytes_limit, trial_runtime_seconds,
		        expires_at, created_at, updated_at
		 FROM account_entitlements
		 WHERE account_id = $1
		 FOR UPDATE`,
		accountID,
	).Scan(
		&entitlement.AccountID,
		&entitlement.Source,
		&entitlement.Status,
		&entitlement.RuntimeLimit,
		&entitlement.ActiveRuntimeLimit,
		&entitlement.RuntimeStartsPerDay,
		&entitlement.StorageBytesLimit,
		&entitlement.TrialRuntimeSeconds,
		&entitlement.ExpiresAt,
		&entitlement.CreatedAt,
		&entitlement.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AccountEntitlement{}, ErrRuntimeEntitlement
		}
		return AccountEntitlement{}, err
	}
	return entitlement, nil
}

func entitlementActive(entitlement AccountEntitlement) bool {
	if strings.TrimSpace(entitlement.Status) != "active" {
		return false
	}
	return entitlement.ExpiresAt == nil || entitlement.ExpiresAt.After(time.Now().UTC())
}

func missingEntitlementSummary(accountID string) AccountEntitlementSummary {
	summary := AccountEntitlementSummary{
		AccountID: accountID,
		Source:    "none",
		Status:    "missing",
	}
	summary.blockCreate(RuntimeEntitlementRequiredCode, ErrRuntimeEntitlement.Error())
	summary.blockStart(RuntimeEntitlementRequiredCode, ErrRuntimeEntitlement.Error())
	return summary
}

func (s *AccountEntitlementSummary) blockCreate(code, reason string) {
	s.CanCreateRuntime = false
	if s.CreateRuntimeBlockedCode == "" {
		s.CreateRuntimeBlockedCode = code
		s.CreateRuntimeBlockedReason = reason
	}
}

func (s *AccountEntitlementSummary) blockStart(code, reason string) {
	s.CanStartRuntime = false
	if s.StartRuntimeBlockedCode == "" {
		s.StartRuntimeBlockedCode = code
		s.StartRuntimeBlockedReason = reason
	}
}

func remaining(limit, used int) int {
	value := limit - used
	if value < 0 {
		return 0
	}
	return value
}

func appendRuntimeStartEventTX(ctx context.Context, tx *sql.Tx, accountID, runtimeID string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO runtime_start_events (account_id, runtime_id)
		 VALUES ($1, $2)`,
		accountID,
		runtimeID,
	)
	return err
}

func appendRuntimeLogTX(ctx context.Context, tx *sql.Tx, runtimeID, source, level, message string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO runtime_logs (runtime_id, source, level, message)
		 VALUES ($1, $2, $3, $4)`,
		runtimeID,
		defaultString(source, "system"),
		defaultString(level, "info"),
		strings.TrimSpace(message),
	)
	return err
}

func closeRuntimeSessionsTX(ctx context.Context, tx *sql.Tx, runtimeID, reason string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE sessions
		 SET status = 'closed',
		     updated_at = NOW(),
		     ended_at = NOW(),
		     end_reason = $2
		 WHERE runtime_id = $1
		   AND status IN ('pending', 'active')`,
		runtimeID,
		strings.TrimSpace(reason),
	)
	return err
}

func revokeRuntimeCapabilityTX(ctx context.Context, tx *sql.Tx, accountID, capabilityID string) (int, error) {
	result, err := tx.ExecContext(ctx,
		`UPDATE runtime_capabilities
		    SET revoked_at = COALESCE(revoked_at, NOW()),
		        updated_at = NOW()
		  WHERE account_id = $1
		    AND id = $2
		    AND revoked_at IS NULL`,
		strings.TrimSpace(accountID),
		strings.TrimSpace(capabilityID),
	)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

func revokeRuntimeCapabilitiesForRuntimeTX(ctx context.Context, tx *sql.Tx, runtimeID string) (int, error) {
	result, err := tx.ExecContext(ctx,
		`UPDATE runtime_capabilities
		    SET revoked_at = COALESCE(revoked_at, NOW()),
		        updated_at = NOW()
		  WHERE runtime_id = $1
		    AND revoked_at IS NULL`,
		strings.TrimSpace(runtimeID),
	)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

func revokeEndedSessionCapabilitiesTX(ctx context.Context, tx *sql.Tx) (int, error) {
	result, err := tx.ExecContext(ctx,
		`UPDATE runtime_capabilities AS c
		    SET revoked_at = COALESCE(c.revoked_at, NOW()),
		        updated_at = NOW()
		  WHERE c.revoked_at IS NULL
		    AND EXISTS (
		        SELECT 1
		          FROM sessions ended
		         WHERE ended.capability_id = c.id
		           AND ended.ended_at IS NOT NULL
		           AND ended.status IN ('closed', 'expired', 'stale', 'revoked')
		    )
		    AND NOT EXISTS (
		        SELECT 1
		          FROM sessions live
		         WHERE live.capability_id = c.id
		           AND live.status IN ('pending', 'active')
		           AND live.expires_at > NOW()
		    )`,
	)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

func revokeExpiredRuntimeCapabilitiesTX(ctx context.Context, tx *sql.Tx) (int, error) {
	result, err := tx.ExecContext(ctx,
		`UPDATE runtime_capabilities
		    SET revoked_at = COALESCE(revoked_at, NOW()),
		        updated_at = NOW()
		  WHERE revoked_at IS NULL
		    AND expires_at <= NOW()`,
	)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

func pruneExpiredRuntimeCapabilityNoncesTX(ctx context.Context, tx *sql.Tx) (int, error) {
	result, err := tx.ExecContext(ctx, `DELETE FROM runtime_capability_nonces WHERE expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

func queryStringColumnTX(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if values == nil {
		return []string{}, nil
	}
	return values, nil
}

func postgresInterval(d time.Duration) string {
	seconds := int64(d.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%d seconds", seconds)
}

func scanRuntimeDest(runtime *Runtime) []any {
	return []any{
		&runtime.ID,
		&runtime.AccountID,
		&runtime.Name,
		&runtime.Status,
		&runtime.DesiredState,
		&runtime.ConnectionStatus,
		&runtime.HostID,
		&runtime.PersonaVersion,
		&runtime.ActivePersonaJSON,
		&runtime.AndroidImage,
		&runtime.AndroidVersion,
		&runtime.WidthPx,
		&runtime.HeightPx,
		&runtime.DensityDpi,
		&runtime.AudioEnabled,
		&runtime.CameraMode,
		&runtime.FileMode,
		&runtime.BlobAutoSnapshot,
		&runtime.BlobRetainDays,
		&runtime.BlobStoreKind,
		&runtime.BlobManifestJSON,
		&runtime.BlobHostID,
		&runtime.BlobLastSnapshotAt,
		&runtime.StartedAt,
		&runtime.LoadAverage,
		&runtime.ContainerName,
		&runtime.ADBPort,
		&runtime.ViewerPort,
		&runtime.WipeRequested,
		&runtime.CleanupPending,
		&runtime.OperationGeneration,
		&runtime.LastError,
		&runtime.DeletedAt,
		&runtime.CreatedAt,
		&runtime.UpdatedAt,
	}
}

func scanAccountStorageDest(storage *AccountStorage) []any {
	return []any{
		&storage.AccountID,
		&storage.Provider,
		&storage.FundingModel,
		&storage.WalletAddress,
		&storage.EncryptedSeedBlob,
		&storage.SeedEncryptionHint,
		&storage.Status,
		&storage.LastPreflightStatus,
		&storage.LastPreflightJSON,
		&storage.LastPreflightAt,
		&storage.CreatedAt,
		&storage.UpdatedAt,
	}
}

func normalizedCreateInput(input CreateRuntimeInput) (CreateRuntimeInput, error) {
	if strings.TrimSpace(input.Name) == "" {
		input.Name = "My runtime"
	}
	androidImage, err := normalizeAndroidImage(input.AndroidImage)
	if err != nil {
		return CreateRuntimeInput{}, err
	}
	input.AndroidImage = androidImage
	if strings.TrimSpace(input.AndroidVersion) == "" {
		input.AndroidVersion = "android-14"
	}
	if input.WidthPx <= 0 {
		input.WidthPx = 720
	}
	if input.HeightPx <= 0 {
		input.HeightPx = 1600
	}
	if input.DensityDpi <= 0 {
		input.DensityDpi = 320
	}
	if !input.AudioEnabled {
		input.AudioEnabled = true
	}
	input.CameraMode = normalizeChoice(input.CameraMode, "disabled", "disabled", "passthrough", "upload-only")
	input.FileMode = normalizeChoice(input.FileMode, "upload-only", "upload-only", "download-only", "bidirectional")
	if !input.BlobAutoSnapshot {
		input.BlobAutoSnapshot = true
	}
	if input.BlobRetainDays <= 0 {
		input.BlobRetainDays = 7
	}
	if err := validateRuntimeProfile(input); err != nil {
		return CreateRuntimeInput{}, err
	}
	return input, nil
}

func normalizeAccountStorageInput(input UpdateAccountStorageInput) (UpdateAccountStorageInput, error) {
	if strings.EqualFold(strings.TrimSpace(input.FundingModel), "user-funded") {
		return UpdateAccountStorageInput{}, errors.New("user-funded storage configuration is disabled")
	}
	input.Provider = normalizeChoice(input.Provider, "local-disk", "local-disk", "sia-renterd")
	input.FundingModel = normalizeChoice(input.FundingModel, "operator", "operator", "operator-pooled")
	input.Status = normalizeChoice(input.Status, "not_configured", "not_configured", "configured", "funding_required", "syncing", "contracts_required", "ready", "error")

	if input.Provider == "sia-renterd" && input.FundingModel != "operator-pooled" {
		return UpdateAccountStorageInput{}, errors.New("sia-renterd storage requires operator-pooled mode")
	}
	if input.FundingModel == "operator-pooled" && input.Provider != "sia-renterd" {
		return UpdateAccountStorageInput{}, errors.New("operator-pooled mode requires sia-renterd provider")
	}
	if input.WalletAddress != nil && strings.TrimSpace(*input.WalletAddress) != "" {
		return UpdateAccountStorageInput{}, errors.New("account wallet configuration is disabled")
	}
	if input.EncryptedSeedBlob != nil && strings.TrimSpace(*input.EncryptedSeedBlob) != "" {
		return UpdateAccountStorageInput{}, errors.New("account storage seed configuration is disabled")
	}
	if input.SeedEncryptionHint != nil && strings.TrimSpace(*input.SeedEncryptionHint) != "" {
		return UpdateAccountStorageInput{}, errors.New("account storage seed configuration is disabled")
	}
	input.WalletAddress = nil
	input.EncryptedSeedBlob = nil
	input.SeedEncryptionHint = nil
	return input, nil
}

func normalizeAppPackageSelections(packageNames []string) ([]string, error) {
	if len(packageNames) > 50 {
		return nil, errors.New("too many app selections")
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(packageNames))
	for _, packageName := range packageNames {
		value := strings.TrimSpace(packageName)
		if value == "" {
			continue
		}
		if len(value) > 200 {
			return nil, errors.New("app package name is too long")
		}
		for _, r := range value {
			if (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') ||
				r == '.' || r == '_' {
				continue
			}
			return nil, fmt.Errorf("app package name contains unsupported characters: %s", value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func normalizeAndroidImage(image string) (string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return defaultAndroidImage, nil
	}
	if _, ok := allowedAndroidImages[image]; ok {
		return image, nil
	}
	return "", ErrRuntimeProfile
}

func validateRuntimeProfile(input CreateRuntimeInput) error {
	if input.AndroidVersion != "android-14" {
		return ErrRuntimeProfile
	}
	if input.WidthPx < 480 || input.WidthPx > 1600 {
		return ErrRuntimeProfile
	}
	if input.HeightPx < 480 || input.HeightPx > 1600 {
		return ErrRuntimeProfile
	}
	if input.WidthPx%8 != 0 || input.HeightPx%8 != 0 {
		return ErrRuntimeProfile
	}
	if input.DensityDpi < 240 || input.DensityDpi > 640 || input.DensityDpi%20 != 0 {
		return ErrRuntimeProfile
	}
	if input.CameraMode != "disabled" {
		return ErrRuntimeProfile
	}
	if input.FileMode != "upload-only" {
		return ErrRuntimeProfile
	}
	return nil
}

func allocateViewerPortTX(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT viewer_port FROM runtimes
		 WHERE viewer_port IS NOT NULL
		   AND deleted_at IS NULL`,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	used := map[int]bool{}
	for rows.Next() {
		var viewerPort int
		if err := rows.Scan(&viewerPort); err != nil {
			return 0, err
		}
		used[viewerPort] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for port := viewerPortStart; port <= viewerPortEnd; port++ {
		if !used[port] {
			return port, nil
		}
	}

	return 0, errors.New("no viewer ports available")
}

func normalizeChoice(value, fallback string, allowed ...string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func defaultString(value, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func nullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: trimmed, Valid: true}
}

func nullStringValue(value string) sql.NullString {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: trimmed, Valid: true}
}

func nullInt(value *int) sql.NullInt32 {
	if value == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*value), Valid: true}
}

func nullTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: value.UTC(), Valid: true}
}

func nullFloat64(value *float64) sql.NullFloat64 {
	if value == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *value, Valid: true}
}

func generateRelayToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashRelayToken(relayToken string) string {
	digest := sha256.Sum256([]byte("virtroid-relay-token-v1:" + strings.TrimSpace(relayToken)))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func effectiveSessionStatus(session Session, now time.Time) (string, bool) {
	status := strings.ToLower(strings.TrimSpace(session.Status))
	if status == "" {
		status = "unknown"
	}
	expired := (status == "pending" || status == "active") && !session.ExpiresAt.After(now.UTC())
	if expired {
		return "expired", true
	}
	return status, false
}

func buildRuntimeState(runtime Runtime, hasActiveSession bool, currentDeviceSessionID sql.NullString) RuntimeState {
	effectiveState := effectiveRuntimeState(runtime)
	runtimeReady := runtimeReadyForSession(runtime)
	isBusy := effectiveState == "starting" ||
		effectiveState == "stopping" ||
		effectiveState == "wiping" ||
		effectiveState == "deleting"
	deleted := effectiveState == "deleted" || runtime.DeletedAt != nil
	hostAssigned := valueOrEmpty(runtime.HostID) != ""

	canConnect := runtimeReady && !deleted
	canStart := !deleted && !runtimeReady && !isBusy
	canStop := !deleted && hostAssigned && !runtimeStoppedForSession(runtime) &&
		effectiveState != "stopping" &&
		effectiveState != "wiping" &&
		effectiveState != "deleting"
	canWipe := !deleted && !isBusy
	canDelete := !deleted && effectiveState != "deleting"

	blockedReason := ""
	switch {
	case deleted:
		blockedReason = "runtime deleted"
	case isBusy:
		blockedReason = "runtime lifecycle operation is already in progress"
	case runtime.Status == "error" && runtime.LastError != nil:
		blockedReason = strings.TrimSpace(*runtime.LastError)
	case !runtimeReady && !canStart:
		blockedReason = "runtime is not ready"
	}

	var sessionID *string
	if currentDeviceSessionID.Valid {
		value := currentDeviceSessionID.String
		sessionID = &value
	}

	return RuntimeState{
		Runtime:                 runtime,
		CurrentDeviceSessionID:  sessionID,
		EffectiveState:          effectiveState,
		RuntimeReady:            runtimeReady,
		HasActiveSession:        hasActiveSession,
		HasCurrentDeviceSession: currentDeviceSessionID.Valid,
		CanConnect:              canConnect,
		CanStart:                canStart,
		CanStop:                 canStop,
		CanWipe:                 canWipe,
		CanDelete:               canDelete,
		IsBusy:                  isBusy,
		BlockedReason:           blockedReason,
	}
}

func effectiveRuntimeState(runtime Runtime) string {
	status := strings.ToLower(strings.TrimSpace(runtime.Status))
	desiredState := strings.ToLower(strings.TrimSpace(runtime.DesiredState))
	connectionStatus := strings.ToLower(strings.TrimSpace(runtime.ConnectionStatus))
	switch {
	case runtime.DeletedAt != nil || status == "deleted":
		return "deleted"
	case desiredState == "deleted" || status == "deleting":
		return "deleting"
	case status == "wiping" || runtime.WipeRequested:
		return "wiping"
	case status == "stopping" ||
		connectionStatus == "disconnecting" ||
		desiredState == "stopped" && connectionStatus == "online":
		return "stopping"
	case runtimeReadyForSession(runtime):
		return "running"
	case status == "error":
		return "error"
	case desiredState == "running" ||
		status == "starting" ||
		status == "provisioning" ||
		connectionStatus == "connecting" ||
		connectionStatus == "preparing":
		return "starting"
	case runtimeStoppedForSession(runtime) || status == "provisioned":
		return "stopped"
	case status != "":
		return status
	default:
		return "unknown"
	}
}

func runtimeReadyForSession(runtime Runtime) bool {
	return runtime.Status == "running" &&
		runtime.DesiredState == "running" &&
		runtime.ConnectionStatus == "online" &&
		runtime.HostID != nil &&
		runtime.ViewerPort != nil &&
		runtime.DeletedAt == nil
}

func runtimeStoppedForSession(runtime Runtime) bool {
	stopped := strings.EqualFold(runtime.Status, "stopped") || strings.EqualFold(runtime.Status, "provisioned")
	desiredStopped := strings.EqualFold(runtime.DesiredState, "stopped")
	connectionStatus := strings.TrimSpace(runtime.ConnectionStatus)
	offline := connectionStatus == "" ||
		strings.EqualFold(connectionStatus, "offline") ||
		strings.EqualFold(connectionStatus, "disconnected")
	return stopped && desiredStopped && offline
}

func runtimeColumnsWithAlias(alias string) string {
	parts := strings.Split(runtimeColumns, ", ")
	for i, column := range parts {
		parts[i] = alias + "." + column
	}
	return strings.Join(parts, ", ")
}

func normalizeBlobKeyVerifier(blobKeyVerifier string) (string, error) {
	trimmed := strings.TrimSpace(blobKeyVerifier)
	if trimmed == "" {
		return "", ErrIdentityKeyRequired
	}
	raw, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return "", ErrIdentityAuthFailed
	}
	if len(raw) != 32 {
		return "", ErrIdentityAuthFailed
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
