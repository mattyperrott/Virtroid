package io.virtroid.client

import android.app.AlertDialog
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
import io.virtroid.client.api.TrustedDeviceSummary
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenAccountIdentityBinding
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.DeviceIdentityStore
import io.virtroid.client.push.NotificationRelayManager
import io.virtroid.client.security.IdentityKeyManager
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.copySensitiveToClipboard
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPasswordSetup
import io.virtroid.client.security.showTypedConfirmation
import kotlinx.coroutines.launch
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.util.Locale

class AccountIdentityActivity : AppCompatActivity() {
    private lateinit var binding: ScreenAccountIdentityBinding
    private lateinit var sessionStore: SessionStore
    private lateinit var activeSessionStore: ActiveSessionStore
    private lateinit var appSettings: AppSettingsStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private lateinit var identityKeyManager: IdentityKeyManager
    private lateinit var appLockStore: AppLockStore
    private lateinit var appLogs: AppLogStore
    private val deviceIdentityStore = DeviceIdentityStore()
    private val api = VirtroidApi()
    private val timestampFormatter = DateTimeFormatter.ofPattern("MMM d, yyyy", Locale.US)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenAccountIdentityBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        activeSessionStore = ActiveSessionStore(this)
        appSettings = AppSettingsStore(this)
        identityPasswordStore = IdentityPasswordStore(this)
        identityKeyManager = IdentityKeyManager(this, api)
        appLockStore = AppLockStore(this)
        appLogs = AppLogStore.get(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.accountIdentityRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.buttonBack.setOnClickListener { finish() }
        binding.itemAccountId.setOnClickListener {
            copy("account_id", sessionStore.accountId.orEmpty(), getString(R.string.onboarding_account_copied))
        }
        binding.itemDeviceFingerprint.setOnClickListener {
            val deviceId = linkedDeviceFingerprint()
            copy("device_id", deviceId, getString(R.string.account_fingerprint_copied))
        }
        binding.itemIdentityPassword.setOnClickListener {
            showIdentityPasswordActions()
        }
        binding.itemDeviceSigningKey.setOnClickListener {
            copy("device_public_key", deviceIdentityStore.publicKeyMaterial(), getString(R.string.account_device_key_copied))
        }
        binding.itemLinkedDevices.setOnClickListener {
            showLinkedDevices()
        }
        binding.itemInstallApps.setOnClickListener {
            openAppInstaller()
        }
        binding.itemWipeUserData.setOnClickListener {
            confirmLocalDataWipe()
        }
        binding.itemDeleteIdentity.setOnClickListener {
            confirmLocalIdentityReset()
        }

        renderIdentity()
        refreshAppSelections()
    }

    override fun onResume() {
        super.onResume()
        if (::binding.isInitialized) {
            refreshAppSelections()
        }
    }

    private fun renderIdentity() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        val hasLinkedIdentity = accountId.isNotBlank() && deviceId.isNotBlank()

        binding.itemAccountIdSubtitle.text = accountId.ifBlank { getString(R.string.account_missing) }
        binding.itemDeviceFingerprintSubtitle.text = if (deviceId.isBlank()) {
            getString(R.string.device_missing)
        } else {
            "${shortId(linkedDeviceFingerprint())} / ${getString(R.string.account_identity_verified)}"
        }
        binding.textActiveSession.text = if (hasLinkedIdentity) {
            getString(R.string.account_active_local_session)
        } else {
            getString(R.string.account_identity_not_linked)
        }
        val identityPasswordConfigured = identityPasswordStore.isConfigured(accountId, deviceId)
        val identityPasswordCached = identityPasswordStore.isUnlockedFor(accountId, deviceId)
        binding.identityEncryptionValue.text = when {
            !hasLinkedIdentity -> getString(R.string.account_identity_not_linked)
            identityPasswordConfigured -> getString(R.string.account_identity_blob_configured)
            else -> getString(R.string.account_identity_encryption_missing)
        }
        binding.itemIdentityPasswordSubtitle.text = when {
            !hasLinkedIdentity -> getString(R.string.account_identity_not_linked)
            identityPasswordCached -> getString(R.string.account_identity_password_cached)
            identityPasswordConfigured -> getString(R.string.account_identity_password_required_later)
            else -> getString(R.string.account_identity_encryption_missing)
        }
        binding.itemLinkedDevicesSubtitle.text = if (hasLinkedIdentity) {
            getString(R.string.account_linked_devices_subtitle)
        } else {
            getString(R.string.account_identity_not_linked)
        }
        binding.itemInstallAppsSubtitle.text = if (hasLinkedIdentity) {
            getString(R.string.account_apps_subtitle)
        } else {
            getString(R.string.account_identity_not_linked)
        }
        binding.identityCreatedValue.text = if (hasLinkedIdentity) {
            getString(R.string.account_identity_created)
        } else {
            getString(R.string.account_identity_not_linked)
        }
        binding.identityLastSyncValue.text = getString(R.string.account_storage_not_synced)

        binding.itemAccountId.contentDescription = accessibilityLabel(
            binding.itemAccountIdTitle.text,
            binding.itemAccountIdSubtitle.text,
        )
        binding.itemDeviceFingerprint.contentDescription = accessibilityLabel(
            binding.itemDeviceFingerprintTitle.text,
            binding.itemDeviceFingerprintSubtitle.text,
        )
        binding.itemIdentityPassword.contentDescription = accessibilityLabel(
            binding.itemIdentityPasswordTitle.text,
            binding.itemIdentityPasswordSubtitle.text,
        )
        binding.itemDeviceSigningKey.contentDescription = accessibilityLabel(
            binding.itemDeviceSigningKeyTitle.text,
            binding.itemDeviceSigningKeySubtitle.text,
        )
        binding.itemLinkedDevices.contentDescription = accessibilityLabel(
            binding.itemLinkedDevicesTitle.text,
            binding.itemLinkedDevicesSubtitle.text,
        )
        updateAppInstallerAccessibility()
        binding.itemWipeUserData.contentDescription = accessibilityLabel(
            binding.wipeUserDataTitle.text,
            binding.wipeUserDataDescription.text,
        )
        binding.itemDeleteIdentity.contentDescription = accessibilityLabel(
            binding.deleteIdentityTitle.text,
            binding.deleteIdentityDescription.text,
        )
    }

    private fun refreshStorage() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            return
        }

        lifecycleScope.launch {
            runCatching {
                api.getStorage(sessionStore.baseUrl, accountId, deviceId) to
                    api.getEntitlement(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess { (storage, entitlement) ->
                binding.storageBackendValue.text = when (storage.provider.trim().lowercase(Locale.US)) {
                    "sia-renterd" -> getString(R.string.account_storage_backend_sia)
                    "local-disk" -> getString(R.string.account_storage_backend_local)
                    else -> storage.provider.ifBlank { getString(R.string.account_storage_unavailable) }
                }
                val readiness = storage.lastPreflightStatus?.ifBlank { null } ?: storage.status
                binding.storageReadyChip.text = when (readiness.trim().lowercase(Locale.US)) {
                    "ready" -> getString(R.string.account_storage_status_ready)
                    "degraded" -> getString(R.string.account_storage_status_degraded)
                    "syncing" -> getString(R.string.account_storage_status_syncing)
                    "funding_required" -> getString(R.string.account_storage_status_operator_funding)
                    "contracts_required" -> getString(R.string.account_storage_status_contracts)
                    "error" -> getString(R.string.account_storage_status_error)
                    else -> readiness.ifBlank { getString(R.string.account_storage_unavailable) }
                }
                binding.storageCreditValue.text = getString(R.string.account_storage_operator_managed)
                val mib = 1024L * 1024L
                val usedMiB = entitlement.storageBytesUsed.coerceAtLeast(0L) / mib
                val limitMiB = entitlement.storageBytesLimit.coerceAtLeast(0L) / mib
                val remainingMiB = entitlement.storageBytesRemaining.coerceAtLeast(0L) / mib
                val remainingPercent = if (entitlement.storageBytesLimit > 0L) {
                    ((entitlement.storageBytesRemaining.coerceAtLeast(0L) * 100L) / entitlement.storageBytesLimit)
                        .coerceIn(0L, 100L)
                } else {
                    0L
                }
                binding.storageUsageValue.text = usedMiB.toString()
                binding.storageUsageUnit.text = getString(R.string.account_storage_mib_unit)
                binding.storageUsageSubtitle.text = getString(R.string.account_storage_usage_of_limit, usedMiB, limitMiB)
                binding.storageSnapshots.text = getString(R.string.account_storage_snapshots_unreported)
                binding.storageRunwayIcon.text = getString(R.string.account_storage_remaining_percent, remainingPercent)
                binding.storageRunwayValue.text = getString(R.string.account_storage_remaining_mib, remainingMiB)
                binding.identityLastSyncValue.text = OffsetDateTime.now().format(timestampFormatter)
            }.onFailure {
                binding.storageReadyChip.text = getString(R.string.account_storage_unavailable)
                binding.storageUsageValue.text = "--"
                binding.storageUsageUnit.text = ""
                binding.storageUsageSubtitle.text = getString(R.string.account_storage_sync_failed)
                binding.storageSnapshots.text = getString(R.string.account_storage_snapshots_unreported)
                binding.storageRunwayValue.text = getString(R.string.account_storage_runway_unreported)
            }
        }
    }

    private fun refreshAppSelections() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            binding.itemInstallAppsSubtitle.text = getString(R.string.account_identity_not_linked)
            return
        }

        lifecycleScope.launch {
            runCatching {
                api.listAppCatalog(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess { apps ->
                val selectedCount = apps.count { it.selected }
                binding.itemInstallAppsSubtitle.text = if (selectedCount > 0) {
                    resources.getQuantityString(
                        R.plurals.account_apps_selected_count,
                        selectedCount,
                        selectedCount,
                    )
                } else {
                    getString(R.string.account_apps_selected_none)
                }
                updateAppInstallerAccessibility()
            }.onFailure {
                binding.itemInstallAppsSubtitle.text = getString(R.string.account_apps_catalog_unavailable)
                updateAppInstallerAccessibility()
            }
        }
    }

    private fun updateAppInstallerAccessibility() {
        binding.itemInstallApps.contentDescription = accessibilityLabel(
            binding.itemInstallAppsTitle.text,
            binding.itemInstallAppsSubtitle.text,
        )
    }

    private fun accessibilityLabel(title: CharSequence, detail: CharSequence): String {
        return getString(R.string.accessibility_setting_value, title, detail)
    }

    private fun openAppInstaller() {
        if (sessionStore.accountId.isNullOrBlank() || sessionStore.deviceId.isNullOrBlank()) {
            toast(getString(R.string.account_identity_not_linked))
            return
        }
        startActivity(AppInstallActivity.createIntent(this))
    }

    private fun showLinkedDevices() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            toast(getString(R.string.account_identity_not_linked))
            return
        }

        binding.itemLinkedDevices.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                api.listDevices(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess { devices ->
                binding.itemLinkedDevices.isEnabled = true
                if (devices.isEmpty()) {
                    toast(getString(R.string.account_linked_devices_empty))
                    return@onSuccess
                }
                val labels = devices.map { device -> linkedDeviceLabel(device, deviceId) }.toTypedArray()
                AlertDialog.Builder(this@AccountIdentityActivity)
                    .setTitle(getString(R.string.account_linked_devices_title))
                    .setItems(labels) { _, index ->
                        confirmDeviceRevoke(
                            device = devices[index],
                            activeDeviceCount = devices.count { it.revokedAt == null },
                        )
                    }
                    .setNegativeButton(getString(R.string.controls_cancel), null)
                    .show()
            }.onFailure { error ->
                binding.itemLinkedDevices.isEnabled = true
                appLogs.error("Linked device list failed: ${error.message}", "auth")
                toast(error.virtroidDisplayMessage(this@AccountIdentityActivity))
            }
        }
    }

    private fun linkedDeviceLabel(device: TrustedDeviceSummary, currentDeviceId: String): String {
        val currentMarker = if (device.id == currentDeviceId) {
            " - ${getString(R.string.account_linked_devices_current)}"
        } else {
            ""
        }
        val activity = device.lastSeenAt
            ?.takeIf { it.isNotBlank() }
            ?.let { getString(R.string.account_linked_devices_last_seen, formatRemoteTimestamp(it)) }
            ?: getString(R.string.account_linked_devices_never_seen)
        return "${device.name.ifBlank { shortId(device.id) }}$currentMarker\n${shortId(device.id)} - $activity"
    }

    private fun confirmDeviceRevoke(device: TrustedDeviceSummary, activeDeviceCount: Int) {
        if (activeDeviceCount <= 1) {
            toast(getString(R.string.account_revoke_last_device_blocked))
            return
        }
        val currentDeviceId = sessionStore.deviceId.orEmpty()
        val message = if (device.id == currentDeviceId) {
            getString(R.string.account_revoke_current_device_body)
        } else {
            getString(R.string.account_revoke_device_body)
        }
        showTypedConfirmation(
            title = getString(R.string.account_revoke_device_title),
            message = message,
            confirmationPhrase = "REVOKE",
            confirmLabel = getString(R.string.account_revoke_device_confirm),
            onConfirmed = { revokeDevice(device) },
        )
    }

    private fun revokeDevice(device: TrustedDeviceSummary) {
        val accountId = sessionStore.accountId.orEmpty()
        val signingDeviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || signingDeviceId.isBlank()) {
            toast(getString(R.string.account_identity_not_linked))
            return
        }

        binding.itemLinkedDevices.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                api.revokeDevice(
                    baseUrl = sessionStore.baseUrl,
                    accountId = accountId,
                    signingDeviceId = signingDeviceId,
                    targetDeviceId = device.id,
                )
            }.onSuccess {
                appLogs.critical("Trusted device revoked: ${shortId(device.id)}", "auth")
                toast(getString(R.string.account_revoke_device_done))
                if (device.id == signingDeviceId) {
                    resetLocalIdentity()
                } else {
                    activeSessionStore.clear()
                    binding.itemLinkedDevices.isEnabled = true
                    renderIdentity()
                }
            }.onFailure { error ->
                binding.itemLinkedDevices.isEnabled = true
                appLogs.error("Trusted device revoke failed: ${error.message}", "auth")
                toast(error.virtroidDisplayMessage(this@AccountIdentityActivity))
            }
        }
    }

    private fun confirmLocalDataWipe() {
        showTypedConfirmation(
            title = getString(R.string.account_wipe_local_title),
            message = getString(R.string.account_wipe_local_body),
            confirmationPhrase = "CLEAR",
            confirmLabel = getString(R.string.privacy_force_clear_cache),
            onConfirmed = {
                activeSessionStore.clear()
                identityPasswordStore.clearUnlocked()
                val result = appSettings.clearSafeLocalCache()
                appLogs.warn("Local user data cache wipe completed: ${result.deletedItems} items", "privacy")
                toast(getString(R.string.account_wipe_local_done, result.deletedItems))
            },
        )
    }

    private fun showIdentityPasswordActions() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            toast(getString(R.string.account_identity_not_linked))
            return
        }

        val configured = identityPasswordStore.isConfigured(accountId, deviceId)
        val dialog = AlertDialog.Builder(this)
            .setTitle(getString(R.string.identity_password_title))
            .setMessage(
                if (configured) {
                    getString(R.string.identity_password_change_body)
                } else {
                    getString(R.string.identity_password_setup_body)
                },
            )
            .setNegativeButton(
                getString(if (configured) R.string.identity_password_close else R.string.controls_cancel),
                null,
            )
        if (configured) {
            dialog.setNeutralButton(getString(R.string.identity_password_clear_cache)) { _, _ ->
                identityPasswordStore.clearUnlocked()
                renderIdentity()
                toast(getString(R.string.identity_password_cache_cleared))
            }
        } else {
            dialog.setPositiveButton(getString(R.string.identity_password_set_action)) { _, _ ->
                configureIdentityPassword(accountId, deviceId)
            }
        }
        dialog.show()
    }

    private fun configureIdentityPassword(accountId: String, deviceId: String) {
        lifecycleScope.launch {
            if (identityPasswordStore.isConfigured(accountId, deviceId)) {
                toast(getString(R.string.identity_password_change_action))
                return@launch
            }

            val password = promptIdentityPasswordSetup() ?: return@launch

            runCatching {
                identityKeyManager.unlockOrMigrate(
                    sessionStore.baseUrl,
                    accountId,
                    deviceId,
                    password,
                )
            }.onSuccess {
                appLogs.info("Identity blob password configured", "auth")
                renderIdentity()
                toast(getString(R.string.identity_password_updated))
            }.onFailure { error ->
                appLogs.error("Identity blob password setup failed: ${error.message}", "auth")
                toast(error.virtroidDisplayMessage(this@AccountIdentityActivity))
            }
        }
    }

    private fun confirmLocalIdentityReset() {
        showTypedConfirmation(
            title = getString(R.string.account_delete_local_identity_title),
            message = getString(R.string.account_delete_local_identity_body),
            confirmationPhrase = "DELETE ACCOUNT",
            confirmLabel = getString(R.string.account_delete_local_identity_confirm),
            onConfirmed = ::deleteServerAccountAndResetLocal,
        )
    }

    private fun deleteServerAccountAndResetLocal() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            resetLocalIdentity()
            return
        }

        binding.itemDeleteIdentity.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                api.deleteAccount(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess {
                appLogs.critical("Server-side account erasure accepted", "auth")
                resetLocalIdentity()
            }.onFailure { error ->
                binding.itemDeleteIdentity.isEnabled = true
                appLogs.error("Server-side account erasure failed: ${error.message}", "auth")
                toast(error.message ?: getString(R.string.status_error))
            }
        }
    }

    private fun resetLocalIdentity() {
        activeSessionStore.clear()
        runCatching { api.clearLocalRuntimeCapabilities(this) }
            .onFailure { error ->
                appLogs.error("Runtime capability key cleanup failed: ${error.message}", "auth")
            }
        identityPasswordStore.clearConfigured()
        appLockStore.clearCredential()
        appSettings.clearSafeLocalCache()
        NotificationRelayManager(this).clearLocalRegistration()
        deviceIdentityStore.deleteDeviceKey()
        deviceIdentityStore.resetInstallDeviceId(this)
        sessionStore.clearLinkedIdentity()
        appLogs.critical("Local identity was reset from Account Identity", "auth")
        startActivity(
            Intent(this, OnboardingActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
        )
    }

    private fun copy(label: String, value: String, message: String) {
        if (value.isBlank()) {
            return
        }
        copySensitiveToClipboard(label, value)
        toast(message)
    }

    private fun shortId(value: String): String {
        return if (value.length <= 18) {
            value
        } else {
            value.take(8) + "..." + value.takeLast(6)
        }
    }

    private fun formatRemoteTimestamp(value: String): String {
        return runCatching {
            OffsetDateTime.parse(value).format(timestampFormatter)
        }.getOrDefault(shortId(value))
    }

    private fun linkedDeviceFingerprint(): String {
        return sessionStore.deviceId.orEmpty()
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        fun createIntent(context: Context): Intent = Intent(context, AccountIdentityActivity::class.java)
    }
}
