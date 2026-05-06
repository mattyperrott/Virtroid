package io.virtdroid.client.data

import android.content.Context
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.util.UUID

class AppLogStore private constructor(context: Context) {
    private val prefs = context.applicationContext
        .getSharedPreferences("virtdroid-app-logs", Context.MODE_PRIVATE)
    private val _entries = MutableStateFlow(loadEntries())

    val entries: StateFlow<List<AppLogEntry>> = _entries.asStateFlow()

    fun log(level: AppLogLevel, message: String, source: String = "app") {
        val entry = AppLogEntry(
            id = UUID.randomUUID().toString(),
            timestampMs = System.currentTimeMillis(),
            level = level,
            source = source,
            message = message,
            criticalResolved = false,
        )
        val next = (_entries.value + entry).takeLast(MAX_ENTRIES)
        persist(next)
    }

    fun info(message: String, source: String = "app") = log(AppLogLevel.INFO, message, source)

    fun warn(message: String, source: String = "app") = log(AppLogLevel.WARN, message, source)

    fun error(message: String, source: String = "app") = log(AppLogLevel.ERROR, message, source)

    fun critical(message: String, source: String = "app") = log(AppLogLevel.CRITICAL, message, source)

    fun unresolvedCriticalCount(): Int {
        return _entries.value.count { it.level.countsForBadge && !it.criticalResolved }
    }

    fun markCriticalResolved() {
        val next = _entries.value.map {
            if (it.level.countsForBadge) it.copy(criticalResolved = true) else it
        }
        persist(next)
    }

    fun clearAll() {
        persist(emptyList())
    }

    fun exportText(filter: AppLogFilter = AppLogFilter.ALL): String {
        return _entries.value
            .filter { filter.matches(it.level) }
            .joinToString("\n") { entry ->
                "${Instant.ofEpochMilli(entry.timestampMs)} ${entry.level.name}/${entry.source}: ${entry.message}"
            }
    }

    private fun persist(next: List<AppLogEntry>) {
        _entries.value = next
        val array = JSONArray()
        next.forEach { entry ->
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
        prefs.edit().putString(KEY_ENTRIES, array.toString()).apply()
    }

    private fun loadEntries(): List<AppLogEntry> {
        val encoded = prefs.getString(KEY_ENTRIES, null) ?: return emptyList()
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
                            message = item.optString("message"),
                            criticalResolved = item.optBoolean("critical_resolved", false),
                        ),
                    )
                }
            }
        }.getOrDefault(emptyList())
    }

    companion object {
        private const val KEY_ENTRIES = "entries_json"
        private const val MAX_ENTRIES = 400

        @Volatile
        private var instance: AppLogStore? = null

        fun get(context: Context): AppLogStore {
            return instance ?: synchronized(this) {
                instance ?: AppLogStore(context.applicationContext).also { instance = it }
            }
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

enum class AppLogLevel {
    INFO,
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
    ERRORS,
    WARN;

    fun matches(level: AppLogLevel): Boolean {
        return when (this) {
            ALL -> true
            ERRORS -> level == AppLogLevel.ERROR || level == AppLogLevel.CRITICAL
            WARN -> level == AppLogLevel.WARN
        }
    }
}
