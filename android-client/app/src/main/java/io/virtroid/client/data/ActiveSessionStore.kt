package io.virtroid.client.data

import android.content.Context

class ActiveSessionStore(context: Context) {
    private val prefs = context.applicationContext
        .getSharedPreferences("virtroid-active-session", Context.MODE_PRIVATE)

    fun save(session: ActiveSession) {
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
        if (sessionId.isBlank() || prefs.getString(KEY_SESSION_ID, null) != sessionId) {
            return
        }
        prefs.edit().putLong(KEY_SAVED_AT, System.currentTimeMillis()).apply()
    }

    fun clear() {
        prefs.edit().clear().apply()
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
