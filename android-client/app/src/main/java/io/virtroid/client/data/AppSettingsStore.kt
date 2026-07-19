package io.virtroid.client.data

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import io.virtroid.client.security.SecureLocalVault
import java.io.File

class AppSettingsStore(private val context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext
        .getSharedPreferences("virtroid-app-settings", Context.MODE_PRIVATE)
    private val vault = SecureLocalVault.get(appContext)

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
        get() = prefs.getBoolean(KEY_BLOCK_SCREEN_CAPTURE, true)
        set(value) = prefs.edit().putBoolean(KEY_BLOCK_SCREEN_CAPTURE, value).apply()

    var uiInactivityTimeoutMs: Long
        get() = prefs.getLong(KEY_UI_INACTIVITY_TIMEOUT_MS, DEFAULT_UI_INACTIVITY_TIMEOUT_MS)
        set(value) = prefs.edit().putLong(KEY_UI_INACTIVITY_TIMEOUT_MS, value.coerceAtLeast(30_000L)).apply()

    var autoClearClipboard: Boolean
        get() = protectedBoolean(KEY_AUTO_CLEAR_CLIPBOARD, true)
        set(value) = putProtectedBoolean(KEY_AUTO_CLEAR_CLIPBOARD, value)

    var lastSessionEndReason: String
        get() = protectedString(KEY_LAST_SESSION_END_REASON, SESSION_END_NEVER).orEmpty()
        set(value) = putProtectedString(KEY_LAST_SESSION_END_REASON, value.ifBlank { SESSION_END_NEVER })

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

    fun migrateToVaultIfUnlocked() {
        if (!vault.isUnlocked) {
            return
        }
        vault.remove(NAMESPACE, LEGACY_KEY_AUTO_DELETE_METADATA)
        vault.remove(NAMESPACE, LEGACY_KEY_CLEAR_POST_TRANSFER_ARTIFACTS)
        if (prefs.contains(KEY_AUTO_CLEAR_CLIPBOARD)) {
            vault.putBoolean(NAMESPACE, KEY_AUTO_CLEAR_CLIPBOARD, prefs.getBoolean(KEY_AUTO_CLEAR_CLIPBOARD, true))
        }
        if (prefs.contains(KEY_LAST_SESSION_END_REASON)) {
            vault.putString(NAMESPACE, KEY_LAST_SESSION_END_REASON, prefs.getString(KEY_LAST_SESSION_END_REASON, null))
        }
        prefs.edit()
            .remove(LEGACY_KEY_AUTO_DELETE_METADATA)
            .remove(LEGACY_KEY_CLEAR_POST_TRANSFER_ARTIFACTS)
            .remove(KEY_AUTO_CLEAR_CLIPBOARD)
            .remove(KEY_LAST_SESSION_END_REASON)
            .apply()
    }

    fun exportVaultToLegacyIfUnlocked() {
        if (!vault.isUnlocked) {
            return
        }
        prefs.edit()
            .putBoolean(KEY_AUTO_CLEAR_CLIPBOARD, vault.getBoolean(NAMESPACE, KEY_AUTO_CLEAR_CLIPBOARD, true))
            .putString(
                KEY_LAST_SESSION_END_REASON,
                vault.getString(NAMESPACE, KEY_LAST_SESSION_END_REASON, SESSION_END_NEVER),
            )
            .apply()
    }

    private fun protectedBoolean(key: String, defaultValue: Boolean): Boolean {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            return vault.getBoolean(NAMESPACE, key, defaultValue)
        }
        return if (vault.exists) defaultValue else prefs.getBoolean(key, defaultValue)
    }

    private fun putProtectedBoolean(key: String, value: Boolean) {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            vault.putBoolean(NAMESPACE, key, value)
            prefs.edit().remove(key).apply()
        } else {
            prefs.edit().putBoolean(key, value).apply()
        }
    }

    private fun protectedString(key: String, defaultValue: String): String? {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            return vault.getString(NAMESPACE, key, defaultValue)
        }
        return if (vault.exists) defaultValue else prefs.getString(key, defaultValue)
    }

    private fun putProtectedString(key: String, value: String) {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            vault.putString(NAMESPACE, key, value)
            prefs.edit().remove(key).apply()
        } else {
            prefs.edit().putString(key, value).apply()
        }
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
        private const val LEGACY_KEY_AUTO_DELETE_METADATA = "auto_delete_metadata"
        private const val LEGACY_KEY_CLEAR_POST_TRANSFER_ARTIFACTS = "clear_post_transfer_artifacts"
        private const val KEY_AUTO_CLEAR_CLIPBOARD = "auto_clear_clipboard"
        private const val KEY_LAST_SESSION_END_REASON = "last_session_end_reason"
        private const val NAMESPACE = "app_settings"
    }
}
