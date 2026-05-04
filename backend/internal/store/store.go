package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaSQL string

const runtimeColumns = `id, account_id, name, status, desired_state, connection_status, host_id, persona_version, active_persona_json, android_image, android_version, width_px, height_px, density_dpi, audio_enabled, camera_mode, file_mode, blob_auto_snapshot, blob_retain_days, blob_store_kind, blob_manifest_json, blob_last_snapshot_at, container_name, adb_port, viewer_port, wipe_requested, last_error, deleted_at, created_at, updated_at`

const (
	defaultAndroidImage = "redroid/redroid:12.0.0_64only-latest"
	viewerPortStart     = 46000
	viewerPortEnd       = 46099
)

var allowedAndroidImages = map[string]struct{}{
	defaultAndroidImage: {},
}

type Store struct {
	db *sql.DB
}

var (
	ErrNoReadyHost         = errors.New("no redroid-capable host available")
	ErrDeviceNotFound      = errors.New("device not found")
	ErrRuntimeNotFound     = errors.New("runtime not found")
	ErrRuntimeNotReady     = errors.New("runtime is not ready for sessions")
	ErrSessionNotFound     = errors.New("session not found")
	ErrIdentityNotFound    = errors.New("identity password is not configured for this device")
	ErrIdentityAuthFailed  = errors.New("identity authentication failed")
	ErrIdentityKeyRequired = errors.New("blob access key is required")
	ErrDeviceRequestReplay = errors.New("device request nonce has already been used")
)

type BootstrapResult struct {
	Account Account `json:"account"`
	Device  Device  `json:"device"`
	Runtime Runtime `json:"runtime"`
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

type Device struct {
	ID              string     `json:"id"`
	AccountID       string     `json:"account_id"`
	Name            string     `json:"name"`
	PublicKey       string     `json:"public_key"`
	BlobKeyVerifier *string    `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
}

type Runtime struct {
	ID                 string     `json:"id"`
	AccountID          string     `json:"account_id"`
	Name               string     `json:"name"`
	Status             string     `json:"status"`
	DesiredState       string     `json:"desired_state"`
	ConnectionStatus   string     `json:"connection_status"`
	HostID             *string    `json:"host_id,omitempty"`
	PersonaVersion     int        `json:"persona_version"`
	ActivePersonaJSON  *string    `json:"active_persona_json,omitempty"`
	AndroidImage       string     `json:"android_image"`
	AndroidVersion     string     `json:"android_version"`
	WidthPx            int        `json:"width_px"`
	HeightPx           int        `json:"height_px"`
	DensityDpi         int        `json:"density_dpi"`
	AudioEnabled       bool       `json:"audio_enabled"`
	CameraMode         string     `json:"camera_mode"`
	FileMode           string     `json:"file_mode"`
	BlobAutoSnapshot   bool       `json:"blob_auto_snapshot"`
	BlobRetainDays     int        `json:"blob_retain_days"`
	BlobStoreKind      *string    `json:"blob_store_kind,omitempty"`
	BlobManifestJSON   *string    `json:"blob_manifest_json,omitempty"`
	BlobLastSnapshotAt *time.Time `json:"blob_last_snapshot_at,omitempty"`
	ContainerName      *string    `json:"container_name,omitempty"`
	ADBPort            *int       `json:"adb_port,omitempty"`
	ViewerPort         *int       `json:"viewer_port,omitempty"`
	WipeRequested      bool       `json:"wipe_requested"`
	LastError          *string    `json:"last_error,omitempty"`
	DeletedAt          *time.Time `json:"deleted_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type RuntimeLogEntry struct {
	ID        int64     `json:"id"`
	RuntimeID string    `json:"runtime_id"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
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
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	AdvertiseAddr   string    `json:"advertise_addr"`
	RelayPort       int       `json:"relay_port"`
	DockerSocket    bool      `json:"docker_socket"`
	Binder          bool      `json:"binder"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
}

type HostHeartbeat struct {
	ID            string
	Name          string
	AdvertiseAddr string
	RelayPort     int
	DockerSocket  bool
	Binder        bool
}

type RuntimeObservation struct {
	HostID             string
	Status             string
	ConnectionStatus   string
	ContainerName      *string
	ADBPort            *int
	LastError          *string
	BlobStoreKind      *string
	BlobManifestJSON   *string
	BlobLastSnapshotAt *time.Time
	ClearWipeRequested bool
	ActivePersonaJSON  *string
	ClearActivePersona bool
	ClearBlobManifest  bool
	Deleted            bool
}

type Session struct {
	ID         string    `json:"id"`
	RuntimeID  string    `json:"runtime_id"`
	DeviceID   string    `json:"device_id"`
	Status     string    `json:"status"`
	RelayToken string    `json:"relay_token"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type SessionRelayTarget struct {
	SessionID  string `json:"session_id"`
	RuntimeID  string `json:"runtime_id"`
	DeviceID   string `json:"device_id"`
	HostID     string `json:"host_id"`
	ViewerPort int    `json:"viewer_port"`
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
	_, err := s.db.ExecContext(ctx, schemaSQL)
	return err
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
	defaults CreateRuntimeInput,
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
	runtimeID := uuid.NewString()
	defaults.Name = strings.TrimSpace(defaults.Name)
	if defaults.Name == "" {
		defaults.Name = "Primary runtime"
	}
	defaults.AudioEnabled = true
	defaults.BlobAutoSnapshot = true
	defaults = normalizedCreateInput(defaults)

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

	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`INSERT INTO runtimes (
			id, account_id, name, status, desired_state, connection_status, android_image, android_version,
			width_px, height_px, density_dpi, audio_enabled, camera_mode, file_mode, blob_auto_snapshot, blob_retain_days
		) VALUES ($1, $2, $3, 'stopped', 'stopped', 'offline', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING %s`, runtimeColumns),
		runtimeID,
		accountID,
		defaults.Name,
		defaults.AndroidImage,
		defaults.AndroidVersion,
		defaults.WidthPx,
		defaults.HeightPx,
		defaults.DensityDpi,
		defaults.AudioEnabled,
		defaults.CameraMode,
		defaults.FileMode,
		defaults.BlobAutoSnapshot,
		defaults.BlobRetainDays,
	).Scan(scanRuntimeDest(&result.Runtime)...); err != nil {
		return BootstrapResult{}, err
	}

	if err := appendRuntimeLogTX(ctx, tx, result.Runtime.ID, "system", "info", "Primary runtime created for new account."); err != nil {
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

func (s *Store) CreateRuntime(ctx context.Context, accountID string, input CreateRuntimeInput) (Runtime, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Runtime{}, errors.New("account id is required")
	}

	input = normalizedCreateInput(input)
	runtimeID := uuid.NewString()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM accounts WHERE id = $1`, accountID); err != nil {
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

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", "Runtime created."); err != nil {
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
		   AND desired_state <> 'deleted'
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

func (s *Store) GetRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	var runtime Runtime
	err := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM runtimes
		 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'`, runtimeColumns),
		accountID,
		runtimeID,
	).Scan(scanRuntimeDest(&runtime)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Runtime{}, ErrRuntimeNotFound
	}
	return runtime, err
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
		current.AndroidImage = normalizeAndroidImage(defaultString(*input.AndroidImage, current.AndroidImage))
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET status = 'deleting',
		     desired_state = 'deleted',
		     connection_status = 'offline',
		     updated_at = NOW()
		 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL
		 RETURNING %s`, runtimeColumns),
		accountID,
		runtimeID,
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
			     updated_at = NOW()
			 WHERE id = $1
			 RETURNING %s`, runtimeColumns),
			runtime.ID,
		).Scan(scanRuntimeDest(&runtime)...); err != nil {
			return Runtime{}, err
		}
	}

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "warn", "Runtime scheduled for deletion."); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) RegisterDeviceBlobKeyVerifier(ctx context.Context, accountID, deviceID, blobKeyVerifier string) error {
	verifier := strings.TrimSpace(blobKeyVerifier)
	if verifier == "" {
		return ErrIdentityKeyRequired
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE devices
		 SET blob_key_verifier = $3,
		     last_seen_at = NOW()
		 WHERE account_id = $1 AND id = $2`,
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
	return nil
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
	return AccountStorage{
		AccountID:       accountID,
		Provider:        "local-disk",
		FundingModel:    "operator",
		Status:          "not_configured",
		CreatedAt:       createdAt,
		UpdatedAt:       now,
		LastPreflightAt: nil,
	}, nil
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
		 FROM devices
		 WHERE account_id = $1 AND id = $2`,
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

func (s *Store) VerifyDeviceBlobAccessKey(ctx context.Context, accountID, deviceID, blobAccessKeyB64 string) (string, error) {
	canonicalKey, rawKey, err := normalizeBlobAccessKey(blobAccessKeyB64)
	if err != nil {
		return "", err
	}

	var storedVerifier sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT blob_key_verifier
		 FROM devices
		 WHERE account_id = $1 AND id = $2`,
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

	expected := blobKeyVerifier(rawKey)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(storedVerifier.String))) != 1 {
		return "", ErrIdentityAuthFailed
	}
	return canonicalKey, nil
}

func (s *Store) StartRuntimeWithBlobAccessKey(
	ctx context.Context,
	accountID string,
	deviceID string,
	runtimeID string,
	blobAccessKeyB64 string,
) (Runtime, error) {
	if _, err := s.VerifyDeviceBlobAccessKey(ctx, accountID, deviceID, blobAccessKeyB64); err != nil {
		return Runtime{}, err
	}
	return s.StartRuntime(ctx, accountID, runtimeID)
}

func (s *Store) StopRuntimeWithBlobAccessKey(
	ctx context.Context,
	accountID string,
	deviceID string,
	runtimeID string,
	blobAccessKeyB64 string,
) (Runtime, error) {
	if _, err := s.VerifyDeviceBlobAccessKey(ctx, accountID, deviceID, blobAccessKeyB64); err != nil {
		return Runtime{}, err
	}
	return s.StopRuntime(ctx, accountID, runtimeID)
}

func (s *Store) WipeRuntimeWithBlobAccessKey(
	ctx context.Context,
	accountID string,
	deviceID string,
	runtimeID string,
	blobAccessKeyB64 string,
) (Runtime, error) {
	if _, err := s.VerifyDeviceBlobAccessKey(ctx, accountID, deviceID, blobAccessKeyB64); err != nil {
		return Runtime{}, err
	}
	return s.WipeRuntime(ctx, accountID, runtimeID)
}

func (s *Store) StartRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var currentHost sql.NullString
	var currentViewerPort sql.NullInt32
	var currentDesiredState string
	if err := tx.QueryRowContext(ctx,
		`SELECT host_id, viewer_port, desired_state FROM runtimes
		 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'`,
		accountID,
		runtimeID,
	).Scan(&currentHost, &currentViewerPort, &currentDesiredState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	hostID, err := pickReadyHostTX(ctx, tx, currentHost.String)
	if err != nil {
		return Runtime{}, err
	}

	viewerPort := currentViewerPort
	if !viewerPort.Valid {
		allocatedViewerPort, err := allocateViewerPortTX(ctx, tx)
		if err != nil {
			return Runtime{}, err
		}
		viewerPort = sql.NullInt32{Int32: int32(allocatedViewerPort), Valid: true}
	}
	bumpPersona := currentDesiredState != "running"

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
			 SET host_id = $3,
			     status = 'starting',
			     desired_state = 'running',
			     connection_status = 'connecting',
			     viewer_port = $4,
			     persona_version = CASE WHEN $5 THEN persona_version + 1 ELSE persona_version END,
			     wipe_requested = FALSE,
			     active_persona_json = NULL,
			     container_name = NULL,
			     adb_port = NULL,
			     last_error = NULL,
			     deleted_at = NULL,
			     updated_at = NOW()
			 WHERE account_id = $1 AND id = $2 AND deleted_at IS NULL AND desired_state <> 'deleted'
			 RETURNING %s`, runtimeColumns),
		accountID,
		runtimeID,
		hostID,
		viewerPort,
		bumpPersona,
	).Scan(scanRuntimeDest(&runtime)...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Runtime{}, ErrRuntimeNotFound
		}
		return Runtime{}, err
	}

	logMessage := fmt.Sprintf("Runtime start requested on host %s.", hostID)
	if bumpPersona {
		logMessage = fmt.Sprintf("New session requested on host %s. persona_version=%d.", hostID, runtime.PersonaVersion)
	}
	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", logMessage); err != nil {
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

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET status = 'stopping',
		     desired_state = 'stopped',
		     connection_status = 'disconnecting',
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

	if err := appendRuntimeLogTX(ctx, tx, runtime.ID, "user", "info", "Runtime stop requested."); err != nil {
		return Runtime{}, err
	}

	if err := tx.Commit(); err != nil {
		return Runtime{}, err
	}

	return runtime, nil
}

func (s *Store) WipeRuntime(ctx context.Context, accountID, runtimeID string) (Runtime, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()

	var runtime Runtime
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`UPDATE runtimes
		 SET status = 'wiping',
		     desired_state = 'stopped',
		     connection_status = 'offline',
		     wipe_requested = TRUE,
		     last_error = NULL,
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runtime_logs (runtime_id, source, level, message)
		 VALUES ($1, $2, $3, $4)`,
		runtimeID,
		defaultString(source, "system"),
		defaultString(level, "info"),
		strings.TrimSpace(message),
	)
	return err
}

func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, advertise_addr, relay_port, docker_socket, binder, created_at, updated_at, last_heartbeat_at
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
		`SELECT id, name, advertise_addr, relay_port, docker_socket, binder, created_at, updated_at, last_heartbeat_at
		 FROM hosts WHERE id = $1`,
		hostID,
	).Scan(
		&host.ID,
		&host.Name,
		&host.AdvertiseAddr,
		&host.RelayPort,
		&host.DockerSocket,
		&host.Binder,
		&host.CreatedAt,
		&host.UpdatedAt,
		&host.LastHeartbeatAt,
	)
	return host, err
}

func (s *Store) UpsertHostHeartbeat(ctx context.Context, heartbeat HostHeartbeat) (Host, error) {
	var host Host
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO hosts (id, name, advertise_addr, relay_port, docker_socket, binder)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (id) DO UPDATE
		 SET name = EXCLUDED.name,
		     advertise_addr = EXCLUDED.advertise_addr,
		     relay_port = EXCLUDED.relay_port,
		     docker_socket = EXCLUDED.docker_socket,
		     binder = EXCLUDED.binder,
		     updated_at = NOW(),
		     last_heartbeat_at = NOW()
		 RETURNING id, name, advertise_addr, relay_port, docker_socket, binder, created_at, updated_at, last_heartbeat_at`,
		heartbeat.ID,
		heartbeat.Name,
		heartbeat.AdvertiseAddr,
		heartbeat.RelayPort,
		heartbeat.DockerSocket,
		heartbeat.Binder,
	).Scan(
		&host.ID,
		&host.Name,
		&host.AdvertiseAddr,
		&host.RelayPort,
		&host.DockerSocket,
		&host.Binder,
		&host.CreatedAt,
		&host.UpdatedAt,
		&host.LastHeartbeatAt,
	)

	return host, err
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
		     updated_at = NOW()
		 FROM runtimes AS r
		 WHERE s.id = $1
		   AND s.relay_token = $2
		   AND s.expires_at > NOW()
		   AND s.status = 'pending'
		   AND r.id = s.runtime_id
		   AND r.deleted_at IS NULL
		   AND r.desired_state = 'running'
		   AND r.status = 'running'
		   AND r.host_id IS NOT NULL
		   AND r.viewer_port IS NOT NULL
		 RETURNING s.id, s.runtime_id, s.device_id, r.host_id, r.viewer_port`,
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
		     updated_at = NOW()
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

func (s *Store) ListAssignedRuntimes(ctx context.Context, hostID string) ([]Runtime, error) {
	rows, err := s.db.QueryContext(ctx,
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
	if runtimes == nil {
		return []Runtime{}, nil
	}
	return runtimes, nil
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

	if observation.Deleted {
		_, err := s.db.ExecContext(ctx,
			`UPDATE runtimes
			 SET status = 'deleted',
			     desired_state = 'deleted',
			     connection_status = 'offline',
			     host_id = NULL,
			     active_persona_json = NULL,
			     blob_store_kind = NULL,
			     blob_manifest_json = NULL,
			     container_name = NULL,
			     adb_port = NULL,
			     viewer_port = NULL,
			     wipe_requested = FALSE,
			     last_error = $3,
			     deleted_at = NOW(),
			     updated_at = NOW()
			 WHERE id = $1 AND host_id = $2 AND deleted_at IS NULL`,
			runtimeID,
			observation.HostID,
			nullString(observation.LastError),
		)
		return err
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE runtimes
		 SET status = $3,
		     connection_status = $4,
		     container_name = $5,
		     adb_port = $6,
			     viewer_port = CASE
			         WHEN $3 = 'stopped' OR ($3 = 'error' AND desired_state <> 'running') THEN NULL
			         ELSE viewer_port
			     END,
			     last_error = $7,
			     blob_last_snapshot_at = COALESCE($8, blob_last_snapshot_at),
			     wipe_requested = CASE WHEN $9 THEN FALSE ELSE wipe_requested END,
		     active_persona_json = CASE WHEN $10 THEN NULL ELSE COALESCE($11, active_persona_json) END,
		     blob_store_kind = CASE WHEN $12 THEN NULL ELSE COALESCE($13, blob_store_kind) END,
		     blob_manifest_json = CASE WHEN $12 THEN NULL ELSE COALESCE($14, blob_manifest_json) END,
		     updated_at = NOW()
		 WHERE id = $1 AND host_id = $2 AND deleted_at IS NULL`,
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
	)
	return err
}

func (s *Store) CreateSession(ctx context.Context, deviceID, runtimeID string) (Session, error) {
	var status string
	var desiredState string
	if err := s.db.QueryRowContext(ctx,
		`SELECT r.status, r.desired_state
		 FROM runtimes r
		 JOIN devices d ON d.account_id = r.account_id
		 WHERE r.id = $1
		   AND d.id = $2
		   AND r.deleted_at IS NULL
		   AND r.desired_state <> 'deleted'`,
		runtimeID,
		deviceID,
	).Scan(&status, &desiredState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrRuntimeNotFound
		}
		return Session{}, err
	}

	if desiredState != "running" || status != "running" {
		return Session{}, ErrRuntimeNotReady
	}

	sessionID := uuid.NewString()
	relayToken, err := generateRelayToken()
	if err != nil {
		return Session{}, err
	}
	relayTokenHash := hashRelayToken(relayToken)
	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	var session Session
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO sessions (id, runtime_id, device_id, status, relay_token, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, runtime_id, device_id, status, created_at, updated_at, expires_at`,
		sessionID, runtimeID, deviceID, "pending", relayTokenHash, expiresAt,
	).Scan(
		&session.ID,
		&session.RuntimeID,
		&session.DeviceID,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.ExpiresAt,
	)
	session.RelayToken = relayToken

	return session, err
}

func pickReadyHostTX(ctx context.Context, tx *sql.Tx, preferredHostID string) (string, error) {
	if strings.TrimSpace(preferredHostID) != "" {
		var hostID string
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM hosts
			 WHERE id = $1
			   AND docker_socket = TRUE
			   AND binder = TRUE
			   AND last_heartbeat_at >= NOW() - INTERVAL '2 minutes'`,
			preferredHostID,
		).Scan(&hostID)
		if err == nil {
			return hostID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
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
		&runtime.BlobLastSnapshotAt,
		&runtime.ContainerName,
		&runtime.ADBPort,
		&runtime.ViewerPort,
		&runtime.WipeRequested,
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

func normalizedCreateInput(input CreateRuntimeInput) CreateRuntimeInput {
	if strings.TrimSpace(input.Name) == "" {
		input.Name = "My runtime"
	}
	input.AndroidImage = normalizeAndroidImage(input.AndroidImage)
	if strings.TrimSpace(input.AndroidVersion) == "" {
		input.AndroidVersion = "android-12"
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
	return input
}

func normalizeAccountStorageInput(input UpdateAccountStorageInput) (UpdateAccountStorageInput, error) {
	input.Provider = normalizeChoice(input.Provider, "local-disk", "local-disk", "sia-renterd")
	input.FundingModel = normalizeChoice(input.FundingModel, "operator", "operator", "user-funded")
	input.Status = normalizeChoice(input.Status, "not_configured", "not_configured", "configured", "funding_required", "syncing", "contracts_required", "ready", "error")

	if input.Provider == "sia-renterd" && input.FundingModel != "user-funded" {
		return UpdateAccountStorageInput{}, errors.New("sia-renterd storage requires user-funded mode")
	}
	if input.FundingModel == "user-funded" && input.Provider != "sia-renterd" {
		return UpdateAccountStorageInput{}, errors.New("user-funded storage requires sia-renterd provider")
	}
	if input.WalletAddress != nil && len(strings.TrimSpace(*input.WalletAddress)) > 256 {
		return UpdateAccountStorageInput{}, errors.New("wallet address is too long")
	}
	if input.EncryptedSeedBlob != nil && len(strings.TrimSpace(*input.EncryptedSeedBlob)) > 65536 {
		return UpdateAccountStorageInput{}, errors.New("encrypted seed blob is too large")
	}
	if input.SeedEncryptionHint != nil && len(strings.TrimSpace(*input.SeedEncryptionHint)) > 256 {
		return UpdateAccountStorageInput{}, errors.New("seed encryption hint is too long")
	}
	if input.Provider == "sia-renterd" && input.Status == "ready" && (input.WalletAddress == nil || strings.TrimSpace(*input.WalletAddress) == "") {
		return UpdateAccountStorageInput{}, errors.New("ready sia-renterd storage requires a wallet address")
	}
	return input, nil
}

func normalizeAndroidImage(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return defaultAndroidImage
	}
	if _, ok := allowedAndroidImages[image]; ok {
		return image
	}
	return defaultAndroidImage
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

func generateRelayToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashRelayToken(relayToken string) string {
	digest := sha256.Sum256([]byte("virtdroid-relay-token-v1:" + strings.TrimSpace(relayToken)))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func normalizeBlobAccessKey(blobAccessKeyB64 string) (string, []byte, error) {
	trimmed := strings.TrimSpace(blobAccessKeyB64)
	if trimmed == "" {
		return "", nil, ErrIdentityKeyRequired
	}
	raw, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return "", nil, ErrIdentityAuthFailed
	}
	if len(raw) != 32 {
		return "", nil, ErrIdentityAuthFailed
	}
	return base64.RawURLEncoding.EncodeToString(raw), raw, nil
}

func blobKeyVerifier(rawKey []byte) string {
	sum := sha256.Sum256(append([]byte("virtdroid-blob-verifier-v1:"), rawKey...))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
