CREATE TABLE IF NOT EXISTS accounts (
    id UUID PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- Bootstrap invitations authorize exactly one new account/device transaction.
-- Only the SHA-256 digest is retained; the plaintext token is returned once by
-- the operator CLI and is never stored by the control plane.
CREATE TABLE IF NOT EXISTS bootstrap_invitations (
    id UUID PRIMARY KEY,
    token_sha256 BYTEA NOT NULL UNIQUE CHECK (OCTET_LENGTH(token_sha256) = 32),
    label TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL CHECK (BTRIM(created_by) <> ''),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    -- Retain the consumed account identifier as audit metadata even if the
    -- account is later deleted.
    consumed_account_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (expires_at > created_at),
    CHECK (
        (consumed_at IS NULL AND consumed_account_id IS NULL)
        OR (consumed_at IS NOT NULL AND consumed_account_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_bootstrap_invitations_active_expiry
    ON bootstrap_invitations (expires_at) WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS hosts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    advertise_addr TEXT NOT NULL,
    relay_port INTEGER NOT NULL DEFAULT 8090,
    docker_socket BOOLEAN NOT NULL DEFAULT FALSE,
    binder BOOLEAN NOT NULL DEFAULT FALSE,
    audio_streaming BOOLEAN NOT NULL DEFAULT FALSE,
    file_import BOOLEAN NOT NULL DEFAULT FALSE,
    camera_passthrough BOOLEAN NOT NULL DEFAULT FALSE,
    camera_slots INTEGER NOT NULL DEFAULT 0 CHECK (camera_slots >= 0),
    public_key TEXT NOT NULL DEFAULT '',
    blob_store_kind TEXT NOT NULL DEFAULT 'local-disk',
    storage_preflight_kind TEXT,
    storage_preflight_status TEXT,
    storage_preflight_json TEXT,
    storage_preflight_at TIMESTAMPTZ,
    storage_wallet_address TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE hosts ADD COLUMN IF NOT EXISTS relay_port INTEGER NOT NULL DEFAULT 8090;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS audio_streaming BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS file_import BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS camera_passthrough BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS camera_slots INTEGER NOT NULL DEFAULT 0;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS public_key TEXT NOT NULL DEFAULT '';
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS blob_store_kind TEXT NOT NULL DEFAULT 'local-disk';
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS storage_preflight_kind TEXT;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS storage_preflight_status TEXT;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS storage_preflight_json TEXT;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS storage_preflight_at TIMESTAMPTZ;
ALTER TABLE hosts ADD COLUMN IF NOT EXISTS storage_wallet_address TEXT;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'hosts_camera_slots_nonnegative'
    ) THEN
        ALTER TABLE hosts ADD CONSTRAINT hosts_camera_slots_nonnegative CHECK (camera_slots >= 0);
    END IF;
END $$;

-- Heartbeats describe current host capabilities, but they are not a trust
-- source. Approved operators, nodes, and key history live in this separate
-- registry so an observed host can never approve itself.
CREATE TABLE IF NOT EXISTS node_operators (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'approved' CHECK (status IN ('approved', 'revoked')),
    approved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS approved_nodes (
    node_id TEXT PRIMARY KEY,
    operator_id TEXT NOT NULL REFERENCES node_operators(id) ON DELETE RESTRICT,
    status TEXT NOT NULL DEFAULT 'approved' CHECK (status IN ('approved', 'revoked')),
    active_key_version BIGINT NOT NULL CHECK (active_key_version > 0),
    approved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS approved_node_keys (
    node_id TEXT NOT NULL REFERENCES approved_nodes(node_id) ON DELETE CASCADE,
    key_version BIGINT NOT NULL CHECK (key_version > 0),
    public_key TEXT NOT NULL,
    fingerprint_sha256 TEXT NOT NULL CHECK (fingerprint_sha256 ~ '^[0-9a-f]{64}$'),
    state TEXT NOT NULL CHECK (state IN ('active', 'overlap', 'retired', 'revoked')),
    valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_until TIMESTAMPTZ,
    retired_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (node_id, key_version),
    UNIQUE (fingerprint_sha256)
);

CREATE TABLE IF NOT EXISTS node_registry_audit (
    id BIGSERIAL PRIMARY KEY,
    node_id TEXT NOT NULL,
    operator_id TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('approve', 'rotate', 'revoke')),
    actor TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    key_version BIGINT,
    fingerprint_sha256 TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS operator_registry_audit (
    id BIGSERIAL PRIMARY KEY,
    operator_id TEXT NOT NULL REFERENCES node_operators(id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action IN ('approve', 'reactivate', 'revoke')),
    actor TEXT NOT NULL CHECK (BTRIM(actor) <> ''),
    reason TEXT NOT NULL CHECK (BTRIM(reason) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_approved_node_keys_one_active
    ON approved_node_keys (node_id) WHERE state = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS idx_approved_node_keys_one_overlap
    ON approved_node_keys (node_id) WHERE state = 'overlap';
CREATE INDEX IF NOT EXISTS idx_approved_nodes_operator_status
    ON approved_nodes (operator_id, status);
CREATE INDEX IF NOT EXISTS idx_node_registry_audit_node_created_at
    ON node_registry_audit (node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_operator_registry_audit_operator_created_at
    ON operator_registry_audit (operator_id, created_at DESC);

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

-- The former enrollment-invitation experiment used a different trust model.
-- Keep removing that obsolete table; bootstrap_invitations above is the
-- audited, hashed, single-use replacement.
DROP TABLE IF EXISTS enrollment_invitations;

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

CREATE TABLE IF NOT EXISTS app_catalog (
    package_name TEXT PRIMARY KEY,
    source TEXT NOT NULL DEFAULT 'fdroid',
    display_name TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    icon_url TEXT NOT NULL DEFAULT '',
    version_name TEXT NOT NULL DEFAULT '',
    version_code BIGINT NOT NULL DEFAULT 0,
    apk_url TEXT NOT NULL DEFAULT '',
    apk_sha256 TEXT NOT NULL DEFAULT '',
    apk_size_bytes BIGINT NOT NULL DEFAULT 0,
    min_sdk INTEGER NOT NULL DEFAULT 0,
    native_code TEXT NOT NULL DEFAULT '',
    license TEXT NOT NULL DEFAULT '',
    categories_json TEXT NOT NULL DEFAULT '[]',
    anti_features_json TEXT NOT NULL DEFAULT '[]',
    recommended BOOLEAN NOT NULL DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    catalog_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'fdroid';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS summary TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS icon_url TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS version_name TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS version_code BIGINT NOT NULL DEFAULT 0;
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS apk_url TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS apk_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS apk_size_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS min_sdk INTEGER NOT NULL DEFAULT 0;
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS native_code TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS license TEXT NOT NULL DEFAULT '';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS categories_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS anti_features_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS recommended BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE app_catalog ADD COLUMN IF NOT EXISTS catalog_updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE TABLE IF NOT EXISTS account_app_selections (
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    package_name TEXT NOT NULL REFERENCES app_catalog(package_name) ON DELETE RESTRICT,
    selected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, package_name)
);

INSERT INTO app_catalog (
    package_name, source, display_name, summary, icon_url, version_name, version_code,
    apk_url, apk_sha256, apk_size_bytes, min_sdk, native_code, recommended,
    catalog_updated_at, updated_at
) VALUES
    ('org.videolan.vlc', 'fdroid', 'VLC', 'Video and music player for local media files.', 'https://f-droid.org/repo/org.videolan.vlc/en-US/icon_yAfSvPRJukZzMMfUzvbYqwaD1XmHXNtiPBtuPVHW-6s=.png', '3.7.1', 13070108, 'https://f-droid.org/repo/org.videolan.vlc_13070108.apk', '4a9144fadfd8606cc5c0e9db892fd24846b7b2efeb1630db5377955d1612b119', 49444910, 17, 'x86_64', TRUE, NOW(), NOW()),
    ('org.mozilla.fennec_fdroid', 'fdroid', 'Fennec F-Droid', 'Firefox-derived browser built from open source Mozilla code.', 'https://f-droid.org/repo/org.mozilla.fennec_fdroid/en-US/icon_RCxqbONQXgoTeWvoYAQjC0nmDVTlBoCXvzZ1Rx27gRM=.png', '150.0.3', 1500310, 'https://f-droid.org/repo/org.mozilla.fennec_fdroid_1500310.apk', 'b75aad4f87bc5722ef6a6088e5b942277fc35249939dcf95a181f37438601a48', 124262555, 26, 'x86_64', TRUE, NOW(), NOW()),
    ('com.beemdevelopment.aegis', 'fdroid', 'Aegis Authenticator', 'Encrypted two-factor authenticator for local token storage.', 'https://f-droid.org/repo/com.beemdevelopment.aegis/en-US/icon_C951ZFTL5UuK5VK6KaIOnVy5NNb0Wqe8asl4v1fSXLI=.png', '3.4.2', 81, 'https://f-droid.org/repo/com.beemdevelopment.aegis_81.apk', 'ab633d31cc0fa49e3ae29a3b816a7fcbe04033a3227f42bd30cf86fcd3be4159', 6604352, 23, 'arm64-v8a,armeabi-v7a,x86,x86_64', TRUE, NOW(), NOW()),
    ('org.schabi.newpipe', 'fdroid', 'NewPipe', 'Lightweight video frontend without Google Play Services.', 'https://f-droid.org/repo/org.schabi.newpipe/en-US/icon_OHy4y1W-fJCNhHHOBCM9V_cxZNJJgbcNkB-x7UDTY9Q=.png', '0.28.5', 1010, 'https://f-droid.org/repo/org.schabi.newpipe_1010.apk', 'dbc8a1bb7a3db16f1d2fa9f96157171cc80453d1ff6e51108e73c007ea05cf87', 10881466, 21, 'arm64-v8a,armeabi-v7a,x86,x86_64', TRUE, NOW(), NOW()),
    ('app.organicmaps', 'fdroid', 'Organic Maps', 'Offline maps and GPS navigation for travel.', 'https://f-droid.org/repo/app.organicmaps/en-US/icon_dE7f4P95-uKZwu7cI89Q0xSi_-gvU4DD-XnLoDG9RLg=.png', '2026.05.08-4-FDroid', 26050804, 'https://f-droid.org/repo/app.organicmaps_26050804.apk', '113a61d2c6b7e6a23cb88fcd85687a85558df9410d53fb05d8e39fb9309801f1', 71549340, 21, 'arm64-v8a,armeabi-v7a,x86,x86_64', TRUE, NOW(), NOW()),
    ('com.nononsenseapps.feeder', 'fdroid', 'Feeder', 'Libre RSS feed reader for private news tracking.', 'https://f-droid.org/repo/com.nononsenseapps.feeder/en-US/icon_Ab31f6rFiG70NRqjyOH87znJd2y38yiEg2Tz_lY791w=.png', '2.20.0', 3978, 'https://f-droid.org/repo/com.nononsenseapps.feeder_3978.apk', '215961d5b44081930f616500aa92942d2779186212b665ec890dc163c82439a9', 62880903, 29, 'arm64-v8a,armeabi-v7a,x86,x86_64', FALSE, NOW(), NOW()),
    ('org.kde.kdeconnect_tp', 'fdroid', 'KDE Connect', 'Device integration between Android and desktop systems.', 'https://f-droid.org/repo/org.kde.kdeconnect_tp/en-US/icon_XJXXjUP0BrUH-hGNaCgoKnwUsJ0c2QNgTdISfc_KivI=.png', '1.35.5', 13505, 'https://f-droid.org/repo/org.kde.kdeconnect_tp_13505.apk', '77f61f9ba58d5f6402d5b7ad6a2b2f8904e82ccdd038dbb86b6ab693bb470c4e', 6474356, 23, 'arm64-v8a,armeabi-v7a,x86,x86_64', FALSE, NOW(), NOW()),
    ('net.osmand.plus', 'fdroid', 'OsmAnd~', 'Offline OpenStreetMap viewer and navigation app.', 'https://f-droid.org/repo/net.osmand.plus/en-US/icon_R5cswkpVpZLwTsPD855ukLX_czuOczFwZLn7pCxaB-k=.png', '5.3.10', 531002, 'https://f-droid.org/repo/net.osmand.plus_531002.apk', 'fdac84dead42e92096e368309ce3ab005b00afc4ee30f5f624dfc4861a714669', 184224089, 24, 'x86,x86_64', FALSE, NOW(), NOW()),
    ('eu.faircode.email', 'fdroid', 'FairEmail', 'Privacy-focused email client with broad provider support.', 'https://f-droid.org/repo/eu.faircode.email/en-US/icon_0a2E8tt03J6IsBb7HvvI88FYOaaWLuP1Ea73naLIvxg=.png', '1.2315', 2315, 'https://f-droid.org/repo/eu.faircode.email_2315.apk', '6154673f6282c4d884d7dacb6ae5b264cc3dc4271bec449721bb2f19bfd4e602', 29257296, 21, 'arm64-v8a,armeabi-v7a,x86,x86_64', FALSE, NOW(), NOW()),
    ('com.termux', 'fdroid', 'Termux', 'Terminal emulator with Linux package support.', 'https://f-droid.org/repo/com.termux/en-US/icon_7jMZ7XD80oeucmGEaTwktIRZexLtGWvJfKdVD6Wu2SI=.png', '0.119.0-beta.3', 1022, 'https://f-droid.org/repo/com.termux_1022.apk', 'fdd476982cd74f2f00aac12d3683b1fa260a0b2d146411b94e09d773be3a7b56', 114920926, 24, 'arm64-v8a,armeabi-v7a,x86,x86_64', FALSE, NOW(), NOW()),
    ('com.kunzisoft.keepass.libre', 'fdroid', 'KeePassDX', 'Local password and passkey vault.', 'https://f-droid.org/repo/com.kunzisoft.keepass.libre/en-US/icon_eLwXEQD9l2URrUS3t8esDXnsKGBaH02E-ddEYhV_i7Q=.png', '4.4.2', 44200, 'https://f-droid.org/repo/com.kunzisoft.keepass.libre_44200.apk', '4a0400557acdc8e039d4721d7a0ec3a3dc202d34ba31cea29750b5ea22097934', 16549009, 19, 'arm64-v8a,armeabi-v7a,x86,x86_64', FALSE, NOW(), NOW()),
    ('org.fdroid.basic', 'fdroid', 'F-Droid Basic', 'Minimal F-Droid client for open source app management.', 'https://f-droid.org/repo/org.fdroid.basic/en-US/icon_CPdcoTY7kZ3ERIZkij9504KbM1eEY05XaLvVQwHkqHI=.png', '2.0-alpha9', 2000009, 'https://f-droid.org/repo/org.fdroid.basic_2000009.apk', '1aa1931bf61e11382c2b225581d4adcd2d9803697a144a84c6eb4db04f67cafb', 11391212, 24, 'arm64-v8a,armeabi-v7a,x86,x86_64', FALSE, NOW(), NOW())
ON CONFLICT (package_name) DO NOTHING;

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
    camera_mode TEXT NOT NULL DEFAULT 'photo-import',
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
    cleanup_pending BOOLEAN NOT NULL DEFAULT FALSE,
    operation_generation BIGINT NOT NULL DEFAULT 1,
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
ALTER TABLE runtimes ALTER COLUMN camera_mode SET DEFAULT 'photo-import';
UPDATE runtimes SET camera_mode = 'photo-import' WHERE camera_mode = 'passthrough';
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
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS cleanup_pending BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS operation_generation BIGINT NOT NULL DEFAULT 1;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE runtimes ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
ALTER TABLE runtimes ALTER COLUMN android_image SET DEFAULT 'redroid/redroid:14.0.0_64only-latest';
ALTER TABLE runtimes ALTER COLUMN android_version SET DEFAULT 'android-14';
ALTER TABLE runtimes ALTER COLUMN width_px SET DEFAULT 720;
ALTER TABLE runtimes ALTER COLUMN height_px SET DEFAULT 1600;
ALTER TABLE runtimes ALTER COLUMN density_dpi SET DEFAULT 320;

-- A stopped runtime can remain assigned while the node removes plaintext,
-- networks, and stale blob generations. Preserve that retry obligation across
-- node reports instead of treating an intermediate "stopped" observation as a
-- completed cleanup.
UPDATE runtimes
SET cleanup_pending = TRUE
WHERE status = 'stopped'
  AND desired_state <> 'running'
  AND host_id IS NOT NULL;

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

-- Reconcile legacy duplicate live sessions before installing the invariant.
-- Keep the most recently active row so this migration is safe on existing data.
WITH ranked_live_sessions AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY runtime_id
               ORDER BY COALESCE(last_client_heartbeat_at, updated_at, created_at) DESC,
                        created_at DESC,
                        id DESC
           ) AS live_rank
      FROM sessions
     WHERE status IN ('pending', 'active')
)
UPDATE sessions
   SET status = 'closed',
       updated_at = NOW(),
       ended_at = COALESCE(ended_at, NOW()),
       end_reason = 'superseded while enforcing one live session per runtime'
 WHERE id IN (
     SELECT id FROM ranked_live_sessions WHERE live_rank > 1
 );

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

CREATE TABLE IF NOT EXISTS runtime_blob_key_handoffs (
    runtime_id UUID PRIMARY KEY REFERENCES runtimes(id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    host_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    lease_id TEXT NOT NULL,
    envelope_json TEXT,
    blob_key_verifier TEXT,
    expires_at TIMESTAMPTZ NOT NULL,
    activated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
CREATE INDEX IF NOT EXISTS idx_app_catalog_enabled_name ON app_catalog (enabled, display_name);
CREATE INDEX IF NOT EXISTS idx_account_app_selections_account ON account_app_selections (account_id, selected_at DESC);
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
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_one_live_per_runtime
    ON sessions (runtime_id)
    WHERE status IN ('pending', 'active');
CREATE INDEX IF NOT EXISTS idx_sessions_capability_id ON sessions (capability_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status_expires_at ON sessions (status, expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_last_client_heartbeat_at ON sessions (last_client_heartbeat_at);
CREATE INDEX IF NOT EXISTS idx_runtime_logs_runtime_created_at ON runtime_logs (runtime_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_runtime_blob_key_handoffs_expires_at ON runtime_blob_key_handoffs (expires_at);
CREATE INDEX IF NOT EXISTS idx_security_events_node_created_at ON security_events (node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_events_priority_created_at ON security_events (priority, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_event_ingest_limits_bucket_start ON security_event_ingest_limits (bucket_start);
CREATE INDEX IF NOT EXISTS idx_runtime_start_events_account_created_at ON runtime_start_events (account_id, created_at DESC);
