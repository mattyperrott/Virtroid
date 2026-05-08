package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenPrivacySecurityBinding
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.applyScreenCaptureProtection
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptPinCode
import kotlinx.coroutines.launch
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

class PrivacySecurityActivity : AppCompatActivity() {
    private lateinit var binding: ScreenPrivacySecurityBinding
    private lateinit var appSettings: AppSettingsStore
    private lateinit var appLockStore: AppLockStore
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore
    private var bindingSettings = false
    private val unlockFormatter = DateTimeFormatter.ofPattern("hh:mm a (z)")
        .withZone(ZoneId.systemDefault())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenPrivacySecurityBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        appSettings = AppSettingsStore(this)
        appLockStore = AppLockStore(this)
        sessionStore = SessionStore(this)
        appLogs = AppLogStore.get(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.privacyRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.privacyBackButton.setOnClickListener { finish() }
        binding.autoLockTimerRow.setOnClickListener { chooseAutoLockTimer() }
        binding.failedAttemptsRow.setOnClickListener { chooseFailedAttemptsThreshold() }
        binding.uiInactivityTimeoutRow.setOnClickListener { chooseInactivityTimeout() }
        binding.forceClearCacheButton.setOnClickListener { forceClearCache() }

        binding.requirePinSwitch.setOnCheckedChangeListener { _, checked ->
            if (!bindingSettings) {
                lifecycleScope.launch { setAppLockEnabled(checked) }
            }
        }
        binding.requireUnlockResumeSwitch.setOnCheckedChangeListener { _, checked ->
            if (!bindingSettings) {
                appSettings.requireUnlockOnResume = checked
                appLogs.info("Require unlock on app resume set to $checked", "settings")
            }
        }
        binding.blockScreenCaptureSwitch.setOnCheckedChangeListener { _, checked ->
            if (!bindingSettings) {
                appSettings.blockScreenCapture = checked
                applyScreenCaptureProtection()
                appLogs.info("Screen capture protection set to $checked", "privacy")
                renderTelemetry()
            }
        }
        binding.autoDeleteMetadataSwitch.setOnCheckedChangeListener { _, checked ->
            if (!bindingSettings) {
                appSettings.autoDeleteMetadata = checked
                appLogs.info("Auto-delete metadata set to $checked", "privacy")
            }
        }
        binding.clearPostTransferSwitch.setOnCheckedChangeListener { _, checked ->
            if (!bindingSettings) {
                appSettings.clearPostTransferArtifacts = checked
                appLogs.info("Clear post-transfer artifacts set to $checked", "privacy")
            }
        }
        binding.autoClearClipboardSwitch.setOnCheckedChangeListener { _, checked ->
            if (!bindingSettings) {
                appSettings.autoClearClipboard = checked
                appLogs.info("Auto-clear clipboard set to $checked", "privacy")
            }
        }

        renderSettings()
        appLogs.info("Privacy settings screen opened", "settings")
    }

    override fun onResume() {
        super.onResume()
        renderTelemetry()
    }

    private fun renderSettings() {
        bindingSettings = true
        binding.requirePinSwitch.isChecked = appLockStore.isEnabled() && appLockStore.hasCredential()
        binding.requireUnlockResumeSwitch.isChecked = appSettings.requireUnlockOnResume
        binding.blockScreenCaptureSwitch.isChecked = appSettings.blockScreenCapture
        binding.autoDeleteMetadataSwitch.isChecked = appSettings.autoDeleteMetadata
        binding.clearPostTransferSwitch.isChecked = appSettings.clearPostTransferArtifacts
        binding.autoClearClipboardSwitch.isChecked = appSettings.autoClearClipboard
        binding.autoLockTimerValue.text = appSettings.autoLockLabel()
        binding.failedAttemptsValue.text = getString(
            R.string.privacy_failed_attempts_value,
            appSettings.failedAttemptsThreshold,
        )
        binding.uiInactivityTimeoutValue.text = appSettings.uiInactivityLabel()
        bindingSettings = false
        renderTelemetry()
    }

    private fun renderTelemetry() {
        val lockConfigured = appLockStore.hasCredential()
        val lockEnabled = appLockStore.isEnabled() && lockConfigured
        binding.appLockStatusValue.text = when {
            lockEnabled -> getString(R.string.privacy_telemetry_secured)
            lockConfigured -> getString(R.string.privacy_telemetry_disabled)
            else -> getString(R.string.privacy_telemetry_setup_needed)
        }
        binding.screenshotProtectionValue.text = if (appSettings.blockScreenCapture) {
            getString(R.string.privacy_telemetry_active)
        } else {
            getString(R.string.privacy_telemetry_off)
        }
        binding.trustedDeviceValue.text = if (sessionStore.hasAccess()) {
            getString(R.string.privacy_telemetry_verified)
        } else {
            getString(R.string.privacy_telemetry_unlinked)
        }
        val lastUnlock = appLockStore.lastUnlockAtMs()
        binding.lastUnlockValue.text = if (lastUnlock > 0L) {
            unlockFormatter.format(Instant.ofEpochMilli(lastUnlock))
        } else {
            getString(R.string.privacy_telemetry_never)
        }
        binding.lastSessionEndValue.text = appSettings.lastSessionEndReason
    }

    private suspend fun setAppLockEnabled(enabled: Boolean) {
        if (!enabled) {
            appLockStore.setEnabled(false)
            appLogs.warn("App lock disabled", "auth")
            renderSettings()
            return
        }

        if (!appLockStore.hasCredential()) {
            val pin = collectPinSetup()
            if (pin.isNullOrBlank()) {
                renderSettings()
                return
            }
            appLockStore.saveCredential(AppLockStore.LockMode.PIN, pin)
        }
        appLockStore.setEnabled(true)
        appSettings.biometricUnlockEnabled = false
        appLogs.info("App lock enabled", "auth")
        renderSettings()
    }

    private suspend fun collectPinSetup(): String? {
        val first = promptPinCode(
            title = getString(R.string.lock_pin_prompt),
            hint = getString(R.string.privacy_pin_setup_body),
        )?.trim().orEmpty()
        if (!first.matches(Regex("\\d{6}"))) {
            toast(getString(R.string.lock_pin_invalid_format))
            return null
        }
        val second = promptPinCode(
            title = getString(R.string.lock_pin_confirm_prompt),
            hint = getString(R.string.lock_pin_confirm_prompt),
        )?.trim().orEmpty()
        if (first != second) {
            toast(getString(R.string.lock_pin_mismatch))
            return null
        }
        return first
    }

    private fun chooseAutoLockTimer() {
        val labels = AppSettingsStore.AUTO_LOCK_OPTIONS.map { it.first }.toTypedArray()
        val checked = AppSettingsStore.AUTO_LOCK_OPTIONS.indexOfFirst { it.second == appSettings.autoLockTimeoutMs }
            .coerceAtLeast(0)
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.privacy_auto_lock_timer))
            .setSingleChoiceItems(labels, checked) { dialog, which ->
                appSettings.autoLockTimeoutMs = AppSettingsStore.AUTO_LOCK_OPTIONS[which].second
                appLogs.info("Auto-lock timer set to ${AppSettingsStore.AUTO_LOCK_OPTIONS[which].first}", "settings")
                dialog.dismiss()
                renderSettings()
            }
            .show()
    }

    private fun chooseFailedAttemptsThreshold() {
        val values = listOf(3, 5, 10)
        val labels = values.map { getString(R.string.privacy_failed_attempts_value, it) }.toTypedArray()
        val checked = values.indexOf(appSettings.failedAttemptsThreshold).coerceAtLeast(1)
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.privacy_failed_attempts_backoff))
            .setSingleChoiceItems(labels, checked) { dialog, which ->
                appSettings.failedAttemptsThreshold = values[which]
                appLogs.info("Failed attempts threshold set to ${values[which]}", "settings")
                dialog.dismiss()
                renderSettings()
            }
            .show()
    }

    private fun chooseInactivityTimeout() {
        val labels = AppSettingsStore.INACTIVITY_OPTIONS.map { it.first }.toTypedArray()
        val checked = AppSettingsStore.INACTIVITY_OPTIONS.indexOfFirst { it.second == appSettings.uiInactivityTimeoutMs }
            .coerceAtLeast(1)
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.privacy_ui_inactivity_timeout))
            .setSingleChoiceItems(labels, checked) { dialog, which ->
                appSettings.uiInactivityTimeoutMs = AppSettingsStore.INACTIVITY_OPTIONS[which].second
                appLogs.info("UI inactivity timeout set to ${AppSettingsStore.INACTIVITY_OPTIONS[which].first}", "settings")
                dialog.dismiss()
                renderSettings()
            }
            .show()
    }

    private fun forceClearCache() {
        val result = appSettings.clearSafeLocalCache()
        if (appSettings.autoClearClipboard) {
            appSettings.clearClipboard()
        }
        if (result.failedTargets.isEmpty()) {
            appLogs.info("Local cache cleared (${result.deletedItems} items)", "privacy")
            toast(getString(R.string.privacy_cache_cleared))
        } else {
            appLogs.error("Cache clear partial failure: ${result.failedTargets.joinToString()}", "privacy")
            toast(getString(R.string.privacy_cache_clear_failed))
        }
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        fun createIntent(context: Context): Intent {
            return Intent(context, PrivacySecurityActivity::class.java)
        }
    }
}
