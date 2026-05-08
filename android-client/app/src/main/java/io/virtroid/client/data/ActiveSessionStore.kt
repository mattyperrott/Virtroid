package io.virtroid.client.data

import android.content.Context
import io.virtroid.client.security.SecureLocalVault

class ActiveSessionStore(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext
        .getSharedPreferences("virtroid-active-session", Context.MODE_PRIVATE)
    private val vault = SecureLocalVault.get(appContext)

    fun save(session: ActiveSession) {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            vault.putString(NAMESPACE, KEY_ACCOUNT_ID, session.accountId)
            vault.putString(NAMESPACE, KEY_DEVICE_ID, session.deviceId)
            vault.putString(NAMESPACE, KEY_BASE_URL, session.baseUrl)
            vault.putString(NAMESPACE, KEY_RUNTIME_ID, session.runtimeId)
            vault.putString(NAMESPACE, KEY_RUNTIME_NAME, session.runtimeName)
            vault.putString(NAMESPACE, KEY_VIEWER_ADDRESS, session.viewerAddress)
            vault.putString(NAMESPACE, KEY_RELAY_HOST, session.relayHost)
            vault.putInt(NAMESPACE, KEY_RELAY_PORT, session.relayPort)
            vault.putBoolean(NAMESPACE, KEY_RELAY_TLS, session.relayTls)
            vault.putString(NAMESPACE, KEY_RELAY_PATH, session.relayPath)
            vault.putString(NAMESPACE, KEY_RELAY_TOKEN, session.relayToken)
            vault.putString(NAMESPACE, KEY_SESSION_ID, session.sessionId)
            vault.putLong(NAMESPACE, KEY_SAVED_AT, session.savedAtMs)
            prefs.edit().clear().apply()
            return
        }

        prefs.edit()
            .putString(KEY_ACCOUNT_ID, session.accountId)
            .putString(KEY_DEVICE_ID, session.deviceId)
            .putString(KEY_BASE_URL, session.baseUrl)
            .putString(KEY_RUNTIME_ID, session.runtimeId)
            .putString(KEY_RUNTIME_NAME, session.runtimeName)
            .putString(KEY_VIEWER_ADDRESS, session.viewerAddress)
            .putString(KEY_RELAY_HOST, session.relayHost)
            .putInt(KEY_RELAY_PORT, session.relayPort)
            .putBoolean(KEY_RELAY_TLS, session.relayTls)
            .putString(KEY_RELAY_PATH, session.relayPath)
            .putString(KEY_RELAY_TOKEN, session.relayToken)
            .putString(KEY_SESSION_ID, session.sessionId)
            .putLong(KEY_SAVED_AT, session.savedAtMs)
            .apply()
    }

    fun load(): ActiveSession? {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            val runtimeId = vault.getString(NAMESPACE, KEY_RUNTIME_ID)?.takeIf { it.isNotBlank() } ?: return null
            val relayToken = vault.getString(NAMESPACE, KEY_RELAY_TOKEN)?.takeIf { it.isNotBlank() } ?: return null
            return ActiveSession(
                accountId = vault.getString(NAMESPACE, KEY_ACCOUNT_ID, "").orEmpty(),
                deviceId = vault.getString(NAMESPACE, KEY_DEVICE_ID, "").orEmpty(),
                baseUrl = vault.getString(NAMESPACE, KEY_BASE_URL, "").orEmpty(),
                runtimeId = runtimeId,
                runtimeName = vault.getString(NAMESPACE, KEY_RUNTIME_NAME, "").orEmpty(),
                viewerAddress = vault.getString(NAMESPACE, KEY_VIEWER_ADDRESS, "").orEmpty(),
                relayHost = vault.getString(NAMESPACE, KEY_RELAY_HOST, "").orEmpty(),
                relayPort = vault.getInt(NAMESPACE, KEY_RELAY_PORT, 0),
                relayTls = vault.getBoolean(NAMESPACE, KEY_RELAY_TLS, false),
                relayPath = vault.getString(NAMESPACE, KEY_RELAY_PATH, "").orEmpty(),
                relayToken = relayToken,
                sessionId = vault.getString(NAMESPACE, KEY_SESSION_ID, "").orEmpty(),
                savedAtMs = vault.getLong(NAMESPACE, KEY_SAVED_AT, 0L),
            )
        }
        if (vault.exists) {
            return null
        }

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
            savedAtMs = prefs.getLong(KEY_SAVED_AT, 0L),
        )
    }

    fun loadForRuntime(runtimeId: String): ActiveSession? {
        return load()?.takeIf { it.runtimeId == runtimeId && it.hasEndpoint() }
    }

    fun touch(sessionId: String) {
        if (sessionId.isBlank()) {
            return
        }
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            if (vault.getString(NAMESPACE, KEY_SESSION_ID) == sessionId) {
                vault.putLong(NAMESPACE, KEY_SAVED_AT, System.currentTimeMillis())
            }
            return
        }
        if (vault.exists || prefs.getString(KEY_SESSION_ID, null) != sessionId) {
            return
        }
        prefs.edit().putLong(KEY_SAVED_AT, System.currentTimeMillis()).apply()
    }

    fun clear() {
        if (vault.isUnlocked) {
            vault.clearNamespace(NAMESPACE)
        }
        prefs.edit().clear().apply()
    }

    fun migrateToVaultIfUnlocked() {
        if (!vault.isUnlocked || !prefs.contains(KEY_RUNTIME_ID)) {
            return
        }
        vault.putString(NAMESPACE, KEY_ACCOUNT_ID, prefs.getString(KEY_ACCOUNT_ID, null))
        vault.putString(NAMESPACE, KEY_DEVICE_ID, prefs.getString(KEY_DEVICE_ID, null))
        vault.putString(NAMESPACE, KEY_BASE_URL, prefs.getString(KEY_BASE_URL, null))
        vault.putString(NAMESPACE, KEY_RUNTIME_ID, prefs.getString(KEY_RUNTIME_ID, null))
        vault.putString(NAMESPACE, KEY_RUNTIME_NAME, prefs.getString(KEY_RUNTIME_NAME, null))
        vault.putString(NAMESPACE, KEY_VIEWER_ADDRESS, prefs.getString(KEY_VIEWER_ADDRESS, null))
        vault.putString(NAMESPACE, KEY_RELAY_HOST, prefs.getString(KEY_RELAY_HOST, null))
        vault.putInt(NAMESPACE, KEY_RELAY_PORT, prefs.getInt(KEY_RELAY_PORT, 0))
        vault.putBoolean(NAMESPACE, KEY_RELAY_TLS, prefs.getBoolean(KEY_RELAY_TLS, false))
        vault.putString(NAMESPACE, KEY_RELAY_PATH, prefs.getString(KEY_RELAY_PATH, null))
        vault.putString(NAMESPACE, KEY_RELAY_TOKEN, prefs.getString(KEY_RELAY_TOKEN, null))
        vault.putString(NAMESPACE, KEY_SESSION_ID, prefs.getString(KEY_SESSION_ID, null))
        vault.putLong(NAMESPACE, KEY_SAVED_AT, prefs.getLong(KEY_SAVED_AT, 0L))
        prefs.edit().clear().apply()
    }

    fun exportVaultToLegacyIfUnlocked() {
        val session = load() ?: return
        prefs.edit()
            .putString(KEY_ACCOUNT_ID, session.accountId)
            .putString(KEY_DEVICE_ID, session.deviceId)
            .putString(KEY_BASE_URL, session.baseUrl)
            .putString(KEY_RUNTIME_ID, session.runtimeId)
            .putString(KEY_RUNTIME_NAME, session.runtimeName)
            .putString(KEY_VIEWER_ADDRESS, session.viewerAddress)
            .putString(KEY_RELAY_HOST, session.relayHost)
            .putInt(KEY_RELAY_PORT, session.relayPort)
            .putBoolean(KEY_RELAY_TLS, session.relayTls)
            .putString(KEY_RELAY_PATH, session.relayPath)
            .putString(KEY_RELAY_TOKEN, session.relayToken)
            .putString(KEY_SESSION_ID, session.sessionId)
            .putLong(KEY_SAVED_AT, session.savedAtMs)
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
        val savedAtMs: Long = System.currentTimeMillis(),
    ) {
        fun hasEndpoint(): Boolean {
            return relayHost.isNotBlank() && relayPort > 0 && relayPath.isNotBlank() && relayToken.isNotBlank()
        }
    }

    private companion object {
        const val NAMESPACE = "active_session"
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
        const val KEY_SAVED_AT = "saved_at"
    }
}
