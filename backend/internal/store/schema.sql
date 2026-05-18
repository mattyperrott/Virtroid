CREATE TABLE IF NOT EXISTS accounts (
    id UUID PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS hosts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    advertise_addr TEXT NOT NULL,
    relay_port INTEGER NOT NULL DEFAULT 8090,
    docker_socket BOOLEAN NOT NULL DEFAULT FALSE,
    binder BOOLEAN NOT NULL DEFAULT FALSE,
    public_key TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE hosts ADD COLUMN IF NOT EXISTS relay_port INTEGER NOT NULL DEFAULT 8090;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS public_key TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS devices (
    id UUID PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    public_key TEXT NOT NULL,
    blob_key_verifier TEXT,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ
);

ALTER TABLE devices ADD COLUMN IF NOT EXISTS blob_key_verifier TEXT;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS account_storage (
    account_id UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    provider TEXT NOT NULL DEFAULT 'local-disk',
    funding_model TEXT NOT NULL DEFAULT 'operator',
    wallet_address TEXT,
    encrypted_seed_blob TEXT,
    seed_encryption_hint TEXT,
    status TEXT NOT NULL DEFAULT 'not_configured',
    last_preflight_status TEXT,
    last_preflight_json TEXT,
    last_preflight_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'local-disk';
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS funding_model TEXT NOT NULL DEFAULT 'operator';
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS wallet_address TEXT;
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS encrypted_seed_blob TEXT;
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS seed_encryption_hint TEXT;
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'not_configured';
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS last_preflight_status TEXT;
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS last_preflight_json TEXT;
ALTER TABLE account_storage ADD COLUMN IF NOT EXISTS last_preflight_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS account_entitlements (
    account_id UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    source TEXT NOT NULL DEFAULT 'trial',
    status TEXT NOT NULL DEFAULT 'active',
    runtime_limit INTEGER NOT NULL DEFAULT 3,
    active_runtime_limit INTEGER NOT NULL DEFAULT 1,
    runtime_starts_per_day INTEGER NOT NULL DEFAULT 10,
    storage_bytes_limit BIGINT NOT NULL DEFAULT 1073741824,
    trial_runtime_seconds INTEGER NOT NULL DEFAULT 3600,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'trial';
ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active';
ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS runtime_limit INTEGER NOT NULL DEFAULT 3;
ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS active_runtime_limit INTEGER NOT NULL DEFAULT 1;
ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS runtime_starts_per_day INTEGER NOT NULL DEFAULT 10;
ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS storage_bytes_limit BIGINT NOT NULL DEFAULT 1073741824;
ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS trial_runtime_seconds INTEGER NOT NULL DEFAULT 3600;
ALTER TABLE account_entitlements ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE account_entitlements ALTER COLUMN runtime_limit SET DEFAULT 3;
ALTER TABLE account_entitlements ALTER COLUMN runtime_starts_per_day SET DEFAULT 10;

UPDATE account_entitlements
SET runtime_limit = 3,
    updated_at = NOW()
WHERE source = 'trial'
  AND runtime_limit = 1;

UPDATE account_entitlements
SET runtime_starts_per_day = 10,
    updated_at = NOW()
WHERE source = 'trial'
  AND runtime_starts_per_day = 5;

CREATE TABLE IF NOT EXISTS runtimes (
    id UUID PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT 'Primary runtime',
    status TEXT NOT NULL,
    desired_state TEXT NOT NULL DEFAULT 'stopped',
    connection_status TEXT NOT NULL DEFAULT 'offline',
    host_id TEXT,
    persona_version INTEGER NOT NULL DEFAULT 1,
    active_persona_json TEXT,
    android_image TEXT NOT NULL DEFAULT 'redroid/redroid:14.0.0_64only-latest',
    android_version TEXT NOT NULL DEFAULT 'android-14',
    width_px INTEGER NOT NULL DEFAULT 720,
    height_px INTEGER NOT NULL DEFAULT 1600,
    density_dpi INTEGER NOT NULL DEFAULT 320,
    audio_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    camera_mode TEXT NOT NULL DEFAULT 'disabled',
    file_mode TEXT NOT NULL DEFAULT 'upload-only',
    blob_auto_snapshot BOOLEAN NOT NULL DEFAULT TRUE,
    blob_retain_days INTEGER NOT NULL DEFAULT 7,
    blob_store_kind TEXT,
    blob_manifest_json TEXT,
    blob_host_id TEXT,
    blob_last_snapshot_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    load_average DOUBLE PRECISION,
    container_name TEXT,
    adb_port INTEGER,
    viewer_port INTEGER,
    wipe_requested BOOLEAN NOT NULL DEFAULT FALSE,
    last_error TEXT,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT 'Primary runtime';
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS desired_state TEXT NOT NULL DEFAULT 'stopped';
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS connection_status TEXT NOT NULL DEFAULT 'offline';
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS android_image TEXT NOT NULL DEFAULT 'redroid/redroid:14.0.0_64only-latest';
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS android_version TEXT NOT NULL DEFAULT 'android-14';
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS active_persona_json TEXT;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS width_px INTEGER NOT NULL DEFAULT 720;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS height_px INTEGER NOT NULL DEFAULT 1600;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS density_dpi INTEGER NOT NULL DEFAULT 320;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS audio_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS camera_mode TEXT NOT NULL DEFAULT 'disabled';
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS file_mode TEXT NOT NULL DEFAULT 'upload-only';
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS blob_auto_snapshot BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS blob_retain_days INTEGER NOT NULL DEFAULT 7;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS blob_store_kind TEXT;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS blob_manifest_json TEXT;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS blob_host_id TEXT;
UPDATE runtimes
SET blob_host_id = host_id
WHERE blob_host_id IS NULL
  AND host_id IS NOT NULL
  AND NULLIF(TRIM(COALESCE(blob_manifest_json, '')), '') IS NOT NULL
  AND COALESCE(NULLIF(TRIM(blob_store_kind), ''), 'local-disk') = 'local-disk';
ALTER TABLE runtimes DROP COLUMN IF EXISTS active_blob_key_b64;
ALTER TABLE runtimes DROP COLUMN IF EXISTS active_blob_key_expires_at;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS blob_last_snapshot_at TIMESTAMPTZ;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS load_average DOUBLE PRECISION;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS container_name TEXT;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS adb_port INTEGER;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS viewer_port INTEGER;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS wipe_requested BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE runtimes ALTER COLUMN android_image SET DEFAULT 'redroid/redroid:14.0.0_64only-latest';
ALTER TABLE runtimes ALTER COLUMN android_version SET DEFAULT 'android-14';
ALTER TABLE runtimes ALTER COLUMN width_px SET DEFAULT 720;
ALTER TABLE runtimes ALTER COLUMN height_px SET DEFAULT 1600;
ALTER TABLE runtimes ALTER COLUMN density_dpi SET DEFAULT 320;

CREATE TABLE IF NOT EXISTS runtime_capabilities (
    id TEXT PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    runtime_id UUID NOT NULL REFERENCES runtimes(id) ON DELETE CASCADE,
    public_key TEXT NOT NULL,
    scopes TEXT NOT NULL DEFAULT 'runtime',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);

ALTER TABLE runtime_capabilities ADD COLUMN IF NOT EXISTS scopes TEXT NOT NULL DEFAULT 'runtime';
ALTER TABLE runtime_capabilities ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE runtime_capabilities ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE runtime_capabilities ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;
ALTER TABLE runtime_capabilities ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS runtime_capability_nonces (
    capability_id TEXT NOT NULL REFERENCES runtime_capabilities(id) ON DELETE CASCADE,
    nonce TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (capability_id, nonce)
);

CREATE TABLE IF NOT EXISTS sessions (
    id UUID PRIMARY KEY,
    runtime_id UUID NOT NULL REFERENCES runtimes(id) ON DELETE CASCADE,
    device_id UUID REFERENCES devices(id) ON DELETE CASCADE,
    capability_id TEXT REFERENCES runtime_capabilities(id) ON DELETE SET NULL,
    status TEXT NOT NULL,
    relay_token TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_client_heartbeat_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    end_reason TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    relay_token_consumed_at TIMESTAMPTZ
);

ALTER TABLE sessions ALTER COLUMN device_id DROP NOT NULL;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS capability_id TEXT REFERENCES runtime_capabilities(id) ON DELETE SET NULL;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS last_client_heartbeat_at TIMESTAMPTZ;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS ended_at TIMESTAMPTZ;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS end_reason TEXT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS relay_token_consumed_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS device_request_nonces (
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    nonce TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, device_id, nonce)
);

CREATE TABLE IF NOT EXISTS node_request_nonces (
    node_id TEXT NOT NULL,
    nonce TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (node_id, nonce)
);

CREATE INDEX IF NOT EXISTS idx_node_request_nonces_expires_at ON node_request_nonces (expires_at);

CREATE TABLE IF NOT EXISTS runtime_logs (
    id BIGSERIAL PRIMARY KEY,
    runtime_id UUID NOT NULL REFERENCES runtimes(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS security_events (
    id BIGSERIAL PRIMARY KEY,
    node_id TEXT NOT NULL,
    source TEXT NOT NULL,
    rule TEXT NOT NULL,
    priority TEXT NOT NULL,
    output TEXT NOT NULL,
    tags_json TEXT NOT NULL DEFAULT '[]',
    event_json TEXT NOT NULL,
    event_time TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS security_event_ingest_limits (
    node_id TEXT NOT NULL,
    bucket_start TIMESTAMPTZ NOT NULL,
    event_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, bucket_start)
);

CREATE TABLE IF NOT EXISTS runtime_start_events (
    id BIGSERIAL PRIMARY KEY,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    runtime_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE runtime_start_events DROP CONSTRAINT IF EXISTS runtime_start_events_runtime_id_fkey;

INSERT INTO account_entitlements (account_id)
SELECT id FROM accounts
ON CONFLICT (account_id) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_devices_account_id ON devices (account_id);
CREATE INDEX IF NOT EXISTS idx_account_storage_provider ON account_storage (provider);
CREATE INDEX IF NOT EXISTS idx_account_entitlements_status ON account_entitlements (status);
CREATE INDEX IF NOT EXISTS idx_hosts_last_heartbeat_at ON hosts (last_heartbeat_at DESC);
CREATE INDEX IF NOT EXISTS idx_runtimes_account_id ON runtimes (account_id);
CREATE INDEX IF NOT EXISTS idx_runtimes_host_id ON runtimes (host_id);
CREATE INDEX IF NOT EXISTS idx_runtimes_blob_host_id ON runtimes (blob_host_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_runtimes_viewer_port_unique ON runtimes (viewer_port) WHERE viewer_port IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_runtime_capabilities_account_runtime ON runtime_capabilities (account_id, runtime_id);
CREATE INDEX IF NOT EXISTS idx_runtime_capabilities_expires_at ON runtime_capabilities (expires_at);
CREATE INDEX IF NOT EXISTS idx_runtime_capability_nonces_expires_at ON runtime_capability_nonces (expires_at);
CREATE INDEX IF NOT EXISTS idx_device_request_nonces_expires_at ON device_request_nonces (expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_runtime_id ON sessions (runtime_id);
CREATE INDEX IF NOT EXISTS idx_sessions_capability_id ON sessions (capability_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status_expires_at ON sessions (status, expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_last_client_heartbeat_at ON sessions (last_client_heartbeat_at);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_runtime_created_at ON runtime_logs (runtime_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_events_node_created_at ON security_events (node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_events_priority_created_at ON security_events (priority, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_event_ingest_limits_bucket_start ON security_event_ingest_limits (bucket_start);
CREATE INDEX IF NOT EXISTS idx_runtime_start_events_account_created_at ON runtime_start_events (account_id, created_at DESC);
