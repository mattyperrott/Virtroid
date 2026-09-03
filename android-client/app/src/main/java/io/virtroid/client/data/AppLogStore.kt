package io.virtroid.client.data

import android.content.Context
import io.virtroid.client.security.SecureLocalVault
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

class AppLogStore private constructor(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext
        .getSharedPreferences("virtroid-app-logs", Context.MODE_PRIVATE)
    private val securePrefs = KeystorePrefs(prefs, KEYSTORE_ALIAS)
    private val vault = SecureLocalVault.get(appContext)
    private val _entries = MutableStateFlow(loadEntries())

    val entries: StateFlow<List<AppLogEntry>> = _entries.asStateFlow()

    fun log(
        level: AppLogLevel,
        message: String,
        source: String = "app",
        timestampMs: Long = System.currentTimeMillis(),
    ) {
        val entry = AppLogEntry(
            id = UUID.randomUUID().toString(),
            timestampMs = timestampMs,
            level = level,
            source = source,
            message = sanitizeMessage(message),
            criticalResolved = false,
        )
        val next = AppLogState.append(_entries.value, entry, MAX_ENTRIES)
        persist(next)
    }

    fun info(message: String, source: String = "app") = log(AppLogLevel.INFO, message, source)

    fun security(message: String, source: String = "security") = log(AppLogLevel.SECURITY, message, source)

    fun warn(message: String, source: String = "app") = log(AppLogLevel.WARN, message, source)

    fun error(message: String, source: String = "app") = log(AppLogLevel.ERROR, message, source)

    fun critical(message: String, source: String = "app") = log(AppLogLevel.CRITICAL, message, source)

    fun unresolvedCriticalCount(): Int {
        return AppLogState.unresolvedCriticalCount(_entries.value)
    }

    fun clearAll() {
        _entries.value = AppLogState.clear()
        securePrefs.clear(KEY_ENTRIES)
        prefs.edit().remove(KEY_ENTRIES).apply()
        if (vault.isUnlocked) {
            vault.clearNamespace(NAMESPACE)
        }
    }

    private fun persist(next: List<AppLogEntry>) {
        val bounded = next.map { it.copy(message = sanitizeMessage(it.message)) }.takeLast(MAX_ENTRIES)
        _entries.value = bounded
        migrateToEncryptedIfNeeded()
        securePrefs.putString(KEY_ENTRIES, encodeEntries(bounded))
        prefs.edit().remove(KEY_ENTRIES).apply()
        if (vault.isUnlocked) {
            vault.clearNamespace(NAMESPACE)
        }
    }

    private fun loadEntries(): List<AppLogEntry> {
        migrateToEncryptedIfNeeded()?.let { return it }
        val encoded = securePrefs.getString(KEY_ENTRIES, null) ?: return emptyList()
        return decodeEntries(encoded)
    }

    fun migrateToVaultIfUnlocked() {
        migrateToEncryptedIfNeeded()?.let { _entries.value = it }
    }

    fun exportVaultToLegacyIfUnlocked() {
        migrateToEncryptedIfNeeded()?.let { _entries.value = it }
        prefs.edit().remove(KEY_ENTRIES).apply()
    }

    private fun migrateToEncryptedIfNeeded(): List<AppLogEntry>? {
        val encryptedEntries = securePrefs.getString(KEY_ENTRIES, null)?.let(::decodeEntries).orEmpty()
        val legacyEntries = prefs.getString(KEY_ENTRIES, null)?.let(::decodeEntries).orEmpty()
        val vaultEntries = if (vault.isUnlocked) {
            vault.getString(NAMESPACE, KEY_ENTRIES, null)?.let(::decodeEntries).orEmpty()
        } else {
            emptyList()
        }
        if (encryptedEntries.isEmpty() && legacyEntries.isEmpty() && vaultEntries.isEmpty()) {
            return null
        }
        val merged = (encryptedEntries + legacyEntries + vaultEntries)
            .distinctBy { it.id }
            .sortedBy { it.timestampMs }
            .map { it.copy(message = sanitizeMessage(it.message)) }
            .takeLast(MAX_ENTRIES)
        securePrefs.putString(KEY_ENTRIES, encodeEntries(merged))
        prefs.edit().remove(KEY_ENTRIES).apply()
        if (vault.isUnlocked) {
            vault.clearNamespace(NAMESPACE)
        }
        return merged
    }

    private fun encodeEntries(entries: List<AppLogEntry>): String {
        val array = JSONArray()
        entries.forEach { entry ->
            array.put(
                JSONObject()
                    .put("id", entry.id)
                    .put("timestamp_ms", entry.timestampMs)
                    .put("level", entry.level.name)
                    .put("source", entry.source)
                    .put("message", entry.message)
                    .put("critical_resolved", entry.criticalResolved),
            )
        }
        return array.toString()
    }

    private fun decodeEntries(encoded: String): List<AppLogEntry> {
        return runCatching {
            val array = JSONArray(encoded)
            buildList {
                for (index in 0 until array.length()) {
                    val item = array.getJSONObject(index)
                    add(
                        AppLogEntry(
                            id = item.optString("id"),
                            timestampMs = item.optLong("timestamp_ms"),
                            level = AppLogLevel.fromName(item.optString("level")),
                            source = item.optString("source", "app"),
                            message = sanitizeMessage(item.optString("message")),
                            criticalResolved = item.optBoolean("critical_resolved", false),
                        ),
                    )
                }
            }
        }.getOrDefault(emptyList())
    }

    companion object {
        private const val NAMESPACE = "app_logs"
        private const val KEY_ENTRIES = "entries_json"
        private const val KEYSTORE_ALIAS = "virtroid_app_logs_prefs_v1"
        private const val MAX_ENTRIES = 200
        private val UUID_PATTERN = Regex(
            "\\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\\b",
        )
        private val SECRET_KV_PATTERN = Regex(
            "(?i)\\b(relay[_ -]?token|blob[_ -]?access[_ -]?key|public[_ -]?key|signature|nonce)=\\S+",
        )
        private val LONG_TOKEN_PATTERN = Regex("\\b[A-Za-z0-9_+/=-]{48,}\\b")

        @Volatile
        private var instance: AppLogStore? = null

        fun get(context: Context): AppLogStore {
            return instance ?: synchronized(this) {
                instance ?: AppLogStore(context.applicationContext).also { instance = it }
            }
        }

        private fun sanitizeMessage(message: String): String {
            return message
                .replace(SECRET_KV_PATTERN) { match -> "${match.groupValues[1]}=[redacted]" }
                .replace(UUID_PATTERN, "[id]")
                .replace(LONG_TOKEN_PATTERN, "[redacted]")
                .take(500)
        }
    }
}

data class AppLogEntry(
    val id: String,
    val timestampMs: Long,
    val level: AppLogLevel,
    val source: String,
    val message: String,
    val criticalResolved: Boolean,
)

internal object AppLogState {
    fun append(entries: List<AppLogEntry>, entry: AppLogEntry, maxEntries: Int): List<AppLogEntry> {
        return (entries + entry).takeLast(maxEntries)
    }

    fun clear(): List<AppLogEntry> = emptyList()

    fun unresolvedCriticalCount(entries: List<AppLogEntry>): Int {
        return entries.count { it.level.countsForBadge && !it.criticalResolved }
    }

    fun visibleEntries(entries: List<AppLogEntry>, filter: AppLogFilter): List<AppLogEntry> {
        return entries.filter { filter.matches(it.level) }
    }
}

enum class AppLogLevel {
    INFO,
    SECURITY,
    WARN,
    ERROR,
    CRITICAL;

    val countsForBadge: Boolean
        get() = this == ERROR || this == CRITICAL

    companion object {
        fun fromName(name: String): AppLogLevel {
            return entries.firstOrNull { it.name.equals(name, ignoreCase = true) } ?: INFO
        }
    }
}

enum class AppLogFilter {
    ALL,
    SECURITY,
    ERRORS,
    WARN;

    fun matches(level: AppLogLevel): Boolean {
        return when (this) {
            ALL -> true
            SECURITY -> level == AppLogLevel.SECURITY
            ERRORS -> level == AppLogLevel.ERROR || level == AppLogLevel.CRITICAL
            WARN -> level == AppLogLevel.WARN
        }
    }
}
