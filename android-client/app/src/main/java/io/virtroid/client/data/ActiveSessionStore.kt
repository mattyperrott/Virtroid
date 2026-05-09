package io.virtroid.client.data

import android.content.Context
import io.virtroid.client.security.SecureLocalVault
import org.json.JSONObject

class ActiveSessionStore(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext
        .getSharedPreferences("virtroid-active-session", Context.MODE_PRIVATE)
    private val securePrefs = KeystorePrefs(prefs, KEYSTORE_ALIAS)
    private val vault = SecureLocalVault.get(appContext)

    fun save(session: ActiveSession) {
        migrateVaultToEncryptedIfUnlocked()
        securePrefs.putString(KEY_SESSION_JSON, encodeSession(session))
        clearLegacyPlaintext()
    }

    fun load(): ActiveSession? {
        migrateVaultToEncryptedIfUnlocked()
        migrateLegacyPlaintextToEncrypted()
        return securePrefs.getString(KEY_SESSION_JSON, null)?.let(::decodeSession)
    }

    fun loadForRuntime(runtimeId: String): ActiveSession? {
        return load()?.takeIf { it.runtimeId == runtimeId && it.hasEndpoint() }
    }

    fun touch(sessionId: String) {
        if (sessionId.isBlank()) {
            return
        }
        val session = load()?.takeIf { it.sessionId == sessionId } ?: return
        save(session.copy(savedAtMs = System.currentTimeMillis()))
    }

    fun clear() {
        if (vault.isUnlocked) {
            vault.clearNamespace(NAMESPACE)
        }
        securePrefs.clear(KEY_SESSION_JSON)
        clearLegacyPlaintext()
    }

    fun migrateToVaultIfUnlocked() {
        migrateVaultToEncryptedIfUnlocked()
        migrateLegacyPlaintextToEncrypted()
    }

    fun exportVaultToLegacyIfUnlocked() {
        migrateVaultToEncryptedIfUnlocked()
        clearLegacyPlaintext()
    }

    private fun migrateLegacyPlaintextToEncrypted() {
        if (!prefs.contains(KEY_RUNTIME_ID)) {
            return
        }
        val session = legacyPlaintextSession() ?: run {
            clearLegacyPlaintext()
            return
        }
        securePrefs.putString(KEY_SESSION_JSON, encodeSession(session))
        clearLegacyPlaintext()
    }

    private fun migrateVaultToEncryptedIfUnlocked() {
        if (!vault.isUnlocked || !vault.contains(NAMESPACE, KEY_RUNTIME_ID)) {
            return
        }
        val session = ActiveSession(
            accountId = vault.getString(NAMESPACE, KEY_ACCOUNT_ID, "").orEmpty(),
            deviceId = vault.getString(NAMESPACE, KEY_DEVICE_ID, "").orEmpty(),
            baseUrl = vault.getString(NAMESPACE, KEY_BASE_URL, "").orEmpty(),
            runtimeId = vault.getString(NAMESPACE, KEY_RUNTIME_ID, "").orEmpty(),
            runtimeName = vault.getString(NAMESPACE, KEY_RUNTIME_NAME, "").orEmpty(),
            viewerAddress = vault.getString(NAMESPACE, KEY_VIEWER_ADDRESS, "").orEmpty(),
            relayHost = vault.getString(NAMESPACE, KEY_RELAY_HOST, "").orEmpty(),
            relayPort = vault.getInt(NAMESPACE, KEY_RELAY_PORT, 0),
            relayTls = vault.getBoolean(NAMESPACE, KEY_RELAY_TLS, false),
            relayPath = vault.getString(NAMESPACE, KEY_RELAY_PATH, "").orEmpty(),
            relayToken = vault.getString(NAMESPACE, KEY_RELAY_TOKEN, "").orEmpty(),
            sessionId = vault.getString(NAMESPACE, KEY_SESSION_ID, "").orEmpty(),
            viewerPublicKey = vault.getString(NAMESPACE, KEY_VIEWER_PUBLIC_KEY, "").orEmpty(),
            savedAtMs = vault.getLong(NAMESPACE, KEY_SAVED_AT, 0L),
        )
        if (session.hasEndpoint()) {
            securePrefs.putString(KEY_SESSION_JSON, encodeSession(session))
        }
        vault.clearNamespace(NAMESPACE)
    }

    private fun legacyPlaintextSession(): ActiveSession? {
        val runtimeId = prefs.getString(KEY_RUNTIME_ID, null)?.takeIf { it.isNotBlank() } ?: return null
        val relayToken = prefs.getString(KEY_RELAY_TOKEN, null)?.takeIf { it.isNotBlank() } ?: return null
        return ActiveSession(
            accountId = prefs.getString(KEY_ACCOUNT_ID, "").orEmpty(),
            deviceId = prefs.getString(KEY_DEVICE_ID, "").orEmpty(),
            baseUrl = prefs.getString(KEY_BASE_URL, "").orEmpty(),
            runtimeId = runtimeId,
            runtimeName = prefs.getString(KEY_RUNTIME_NAME, "").orEmpty(),
            viewerAddress = prefs.getString(KEY_VIEWER_ADDRESS, "").orEmpty(),
            relayHost = prefs.getString(KEY_RELAY_HOST, "").orEmpty(),
            relayPort = prefs.getInt(KEY_RELAY_PORT, 0),
            relayTls = prefs.getBoolean(KEY_RELAY_TLS, false),
            relayPath = prefs.getString(KEY_RELAY_PATH, "").orEmpty(),
            relayToken = relayToken,
            sessionId = prefs.getString(KEY_SESSION_ID, "").orEmpty(),
            viewerPublicKey = prefs.getString(KEY_VIEWER_PUBLIC_KEY, "").orEmpty(),
            savedAtMs = prefs.getLong(KEY_SAVED_AT, 0L),
        )
    }

    private fun encodeSession(session: ActiveSession): String {
        return JSONObject()
            .put(KEY_ACCOUNT_ID, session.accountId)
            .put(KEY_DEVICE_ID, session.deviceId)
            .put(KEY_BASE_URL, session.baseUrl)
            .put(KEY_RUNTIME_ID, session.runtimeId)
            .put(KEY_RUNTIME_NAME, session.runtimeName)
            .put(KEY_VIEWER_ADDRESS, session.viewerAddress)
            .put(KEY_RELAY_HOST, session.relayHost)
            .put(KEY_RELAY_PORT, session.relayPort)
            .put(KEY_RELAY_TLS, session.relayTls)
            .put(KEY_RELAY_PATH, session.relayPath)
            .put(KEY_RELAY_TOKEN, session.relayToken)
            .put(KEY_SESSION_ID, session.sessionId)
            .put(KEY_VIEWER_PUBLIC_KEY, session.viewerPublicKey)
            .put(KEY_SAVED_AT, session.savedAtMs)
            .toString()
    }

    private fun decodeSession(encoded: String): ActiveSession? {
        return runCatching {
            val payload = JSONObject(encoded)
            ActiveSession(
                accountId = payload.optString(KEY_ACCOUNT_ID),
                deviceId = payload.optString(KEY_DEVICE_ID),
                baseUrl = payload.optString(KEY_BASE_URL),
                runtimeId = payload.optString(KEY_RUNTIME_ID),
                runtimeName = payload.optString(KEY_RUNTIME_NAME),
                viewerAddress = payload.optString(KEY_VIEWER_ADDRESS),
                relayHost = payload.optString(KEY_RELAY_HOST),
                relayPort = payload.optInt(KEY_RELAY_PORT),
                relayTls = payload.optBoolean(KEY_RELAY_TLS),
                relayPath = payload.optString(KEY_RELAY_PATH),
                relayToken = payload.optString(KEY_RELAY_TOKEN),
                sessionId = payload.optString(KEY_SESSION_ID),
                viewerPublicKey = payload.optString(KEY_VIEWER_PUBLIC_KEY),
                savedAtMs = payload.optLong(KEY_SAVED_AT),
            )
        }.getOrNull()?.takeIf { it.hasEndpoint() }
    }

    private fun clearLegacyPlaintext() {
        prefs.edit()
            .remove(KEY_ACCOUNT_ID)
            .remove(KEY_DEVICE_ID)
            .remove(KEY_BASE_URL)
            .remove(KEY_RUNTIME_ID)
            .remove(KEY_RUNTIME_NAME)
            .remove(KEY_VIEWER_ADDRESS)
            .remove(KEY_RELAY_HOST)
            .remove(KEY_RELAY_PORT)
            .remove(KEY_RELAY_TLS)
            .remove(KEY_RELAY_PATH)
            .remove(KEY_RELAY_TOKEN)
            .remove(KEY_SESSION_ID)
            .remove(KEY_VIEWER_PUBLIC_KEY)
            .remove(KEY_SAVED_AT)
            .apply()
    }

    data class ActiveSession(
        val accountId: String,
        val deviceId: String,
        val baseUrl: String,
        val runtimeId: String,
        val runtimeName: String,
        val viewerAddress: String,
        val relayHost: String,
        val relayPort: Int,
        val relayTls: Boolean,
        val relayPath: String,
        val relayToken: String,
        val sessionId: String,
        val viewerPublicKey: String,
        val savedAtMs: Long = System.currentTimeMillis(),
    ) {
        fun hasEndpoint(): Boolean {
            return relayHost.isNotBlank() &&
                relayPort > 0 &&
                relayTls &&
                relayPath.isNotBlank() &&
                relayToken.isNotBlank() &&
                viewerPublicKey.isNotBlank()
        }
    }

    private companion object {
        const val NAMESPACE = "active_session"
        const val KEYSTORE_ALIAS = "virtroid_active_session_prefs_v1"
        const val KEY_SESSION_JSON = "active_session_json"
        const val KEY_ACCOUNT_ID = "account_id"
        const val KEY_DEVICE_ID = "device_id"
        const val KEY_BASE_URL = "base_url"
        const val KEY_RUNTIME_ID = "runtime_id"
        const val KEY_RUNTIME_NAME = "runtime_name"
        const val KEY_VIEWER_ADDRESS = "viewer_address"
        const val KEY_RELAY_HOST = "relay_host"
        const val KEY_RELAY_PORT = "relay_port"
        const val KEY_RELAY_TLS = "relay_tls"
        const val KEY_RELAY_PATH = "relay_path"
        const val KEY_RELAY_TOKEN = "relay_token"
        const val KEY_SESSION_ID = "session_id"
        const val KEY_VIEWER_PUBLIC_KEY = "viewer_public_key"
        const val KEY_SAVED_AT = "saved_at"
    }
}
