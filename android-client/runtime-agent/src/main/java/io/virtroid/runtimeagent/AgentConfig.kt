package io.virtroid.runtimeagent

import android.content.Context
import org.json.JSONObject
import java.net.URI
import java.util.UUID

internal data class AgentConfig(
    val baseUrl: String,
    val runtimeId: String,
    val token: String,
)

internal class AgentConfigStore(context: Context) {
    private val prefs = SecureAgentPrefs(context)

    fun load(): AgentConfig? = synchronized(CONFIG_LOCK) { loadLocked() }

    fun provision(config: AgentConfig): Boolean = synchronized(CONFIG_LOCK) {
        if (!isValid(config)) return@synchronized false
        val current = loadLocked()
        if (current != null && current.runtimeId != config.runtimeId) return false
        prefs.put(
            KEY_CONFIG,
            JSONObject()
                .put("base_url", config.baseUrl.trimEnd('/'))
                .put("runtime_id", config.runtimeId)
                .put("token", config.token)
                .toString(),
        )
        return true
    }

    private fun loadLocked(): AgentConfig? {
        val raw = prefs.get(KEY_CONFIG) ?: return null
        return runCatching {
            val json = JSONObject(raw)
            AgentConfig(
                baseUrl = json.getString("base_url"),
                runtimeId = json.getString("runtime_id"),
                token = json.getString("token"),
            )
        }.getOrNull()?.takeIf(::isValid)
    }

    private fun isValid(config: AgentConfig): Boolean = runCatching {
        val uri = URI(config.baseUrl)
        require(uri.scheme.equals("https", ignoreCase = true))
        require(!uri.host.isNullOrBlank())
        UUID.fromString(config.runtimeId)
        require(config.token.matches(Regex("^[A-Za-z0-9_-]{43,128}$")))
        true
    }.getOrDefault(false)

    private companion object {
        const val KEY_CONFIG = "agent_config"
        val CONFIG_LOCK = Any()
    }
}
