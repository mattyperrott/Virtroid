package io.virtdroid.client.data

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import java.io.File

class AppSettingsStore(private val context: Context) {
    private val prefs = context.applicationContext
        .getSharedPreferences("virtdroid-app-settings", Context.MODE_PRIVATE)

    var biometricUnlockEnabled: Boolean
        get() = prefs.getBoolean(KEY_BIOMETRIC_UNLOCK_ENABLED, true)
        set(value) = prefs.edit().putBoolean(KEY_BIOMETRIC_UNLOCK_ENABLED, value).apply()

    var autoLockTimeoutMs: Long
        get() = prefs.getLong(KEY_AUTO_LOCK_TIMEOUT_MS, AUTO_LOCK_IMMEDIATE_MS)
        set(value) = prefs.edit().putLong(KEY_AUTO_LOCK_TIMEOUT_MS, value).apply()

    var requireUnlockOnResume: Boolean
        get() = prefs.getBoolean(KEY_REQUIRE_UNLOCK_ON_RESUME, true)
        set(value) = prefs.edit().putBoolean(KEY_REQUIRE_UNLOCK_ON_RESUME, value).apply()

    var failedAttemptsThreshold: Int
        get() = prefs.getInt(KEY_FAILED_ATTEMPTS_THRESHOLD, 5).coerceAtLeast(1)
        set(value) = prefs.edit().putInt(KEY_FAILED_ATTEMPTS_THRESHOLD, value.coerceAtLeast(1)).apply()

    var blockScreenCapture: Boolean
        get() = prefs.getBoolean(KEY_BLOCK_SCREEN_CAPTURE, false)
        set(value) = prefs.edit().putBoolean(KEY_BLOCK_SCREEN_CAPTURE, value).apply()

    var uiInactivityTimeoutMs: Long
        get() = prefs.getLong(KEY_UI_INACTIVITY_TIMEOUT_MS, DEFAULT_UI_INACTIVITY_TIMEOUT_MS)
        set(value) = prefs.edit().putLong(KEY_UI_INACTIVITY_TIMEOUT_MS, value.coerceAtLeast(30_000L)).apply()

    var autoDeleteMetadata: Boolean
        get() = prefs.getBoolean(KEY_AUTO_DELETE_METADATA, true)
        set(value) = prefs.edit().putBoolean(KEY_AUTO_DELETE_METADATA, value).apply()

    var clearPostTransferArtifacts: Boolean
        get() = prefs.getBoolean(KEY_CLEAR_POST_TRANSFER_ARTIFACTS, true)
        set(value) = prefs.edit().putBoolean(KEY_CLEAR_POST_TRANSFER_ARTIFACTS, value).apply()

    var autoClearClipboard: Boolean
        get() = prefs.getBoolean(KEY_AUTO_CLEAR_CLIPBOARD, true)
        set(value) = prefs.edit().putBoolean(KEY_AUTO_CLEAR_CLIPBOARD, value).apply()

    var lastSessionEndReason: String
        get() = prefs.getString(KEY_LAST_SESSION_END_REASON, SESSION_END_NEVER).orEmpty()
        set(value) = prefs.edit().putString(KEY_LAST_SESSION_END_REASON, value.ifBlank { SESSION_END_NEVER }).apply()

    fun autoLockLabel(): String = when (autoLockTimeoutMs) {
        AUTO_LOCK_IMMEDIATE_MS -> "Immediately"
        30_000L -> "30 seconds"
        60_000L -> "1 minute"
        5 * 60_000L -> "5 minutes"
        15 * 60_000L -> "15 minutes"
        else -> "${(autoLockTimeoutMs / 60_000L).coerceAtLeast(1)} minutes"
    }

    fun uiInactivityLabel(): String = when (val minutes = uiInactivityTimeoutMs / 60_000L) {
        1L -> "1 minute"
        else -> "$minutes minutes"
    }

    fun clearSafeLocalCache(): CacheClearResult {
        val targets = buildList {
            context.cacheDir?.let(::add)
            context.codeCacheDir?.let(::add)
            context.externalCacheDir?.let(::add)
            add(File(context.filesDir, "tmp"))
            add(File(context.filesDir, "transfers"))
            add(File(context.filesDir, "post-transfer"))
            add(File(context.filesDir, "metadata-cache"))
        }.distinctBy { it.absolutePath }

        var deleted = 0
        val failures = mutableListOf<String>()
        targets.forEach { target ->
            if (!target.exists()) {
                return@forEach
            }
            runCatching {
                deleted += deleteContents(target)
            }.onFailure {
                failures += target.name.ifBlank { target.absolutePath }
            }
        }

        return CacheClearResult(deletedItems = deleted, failedTargets = failures)
    }

    fun clearClipboard() {
        val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
        clipboard?.setPrimaryClip(ClipData.newPlainText("", ""))
    }

    private fun deleteContents(target: File): Int {
        if (!target.exists()) {
            return 0
        }

        if (target.isFile) {
            return if (target.delete()) 1 else 0
        }

        var deleted = 0
        target.listFiles().orEmpty().forEach { child ->
            deleted += if (child.isDirectory) {
                val nested = deleteContents(child)
                if (child.delete()) nested + 1 else nested
            } else if (child.delete()) {
                1
            } else {
                0
            }
        }
        return deleted
    }

    data class CacheClearResult(
        val deletedItems: Int,
        val failedTargets: List<String>,
    )

    companion object {
        const val AUTO_LOCK_IMMEDIATE_MS = 0L
        const val DEFAULT_UI_INACTIVITY_TIMEOUT_MS = 15 * 60_000L
        const val SESSION_END_NEVER = "Never"
        const val SESSION_END_USER = "User Shutdown"
        const val SESSION_END_BACKGROUND = "Backgrounded"
        const val SESSION_END_INACTIVITY = "Inactivity Timeout"

        val AUTO_LOCK_OPTIONS = listOf(
            "Immediately" to AUTO_LOCK_IMMEDIATE_MS,
            "30 seconds" to 30_000L,
            "1 minute" to 60_000L,
            "5 minutes" to 5 * 60_000L,
            "15 minutes" to 15 * 60_000L,
        )

        val INACTIVITY_OPTIONS = listOf(
            "5 minutes" to 5 * 60_000L,
            "15 minutes" to 15 * 60_000L,
            "30 minutes" to 30 * 60_000L,
            "60 minutes" to 60 * 60_000L,
        )

        private const val KEY_BIOMETRIC_UNLOCK_ENABLED = "biometric_unlock_enabled"
        private const val KEY_AUTO_LOCK_TIMEOUT_MS = "auto_lock_timeout_ms"
        private const val KEY_REQUIRE_UNLOCK_ON_RESUME = "require_unlock_on_resume"
        private const val KEY_FAILED_ATTEMPTS_THRESHOLD = "failed_attempts_threshold"
        private const val KEY_BLOCK_SCREEN_CAPTURE = "block_screen_capture"
        private const val KEY_UI_INACTIVITY_TIMEOUT_MS = "ui_inactivity_timeout_ms"
        private const val KEY_AUTO_DELETE_METADATA = "auto_delete_metadata"
        private const val KEY_CLEAR_POST_TRANSFER_ARTIFACTS = "clear_post_transfer_artifacts"
        private const val KEY_AUTO_CLEAR_CLIPBOARD = "auto_clear_clipboard"
        private const val KEY_LAST_SESSION_END_REASON = "last_session_end_reason"
    }
}
