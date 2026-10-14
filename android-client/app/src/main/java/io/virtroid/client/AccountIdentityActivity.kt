package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.content.res.ColorStateList
import android.os.Bundle
import android.view.View
import android.widget.ImageButton
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
import io.virtroid.client.security.promptIdentityPassword
import io.virtroid.client.security.promptIdentityPasswordSetup
import io.virtroid.client.security.showConfirmation
import kotlinx.coroutines.launch
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.util.Locale
import io.virtroid.client.ui.Notice
import io.virtroid.client.ui.VirtroidSheet

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
    private val dateFormatter = DateTimeFormatter.ofPattern("d MMM", Locale.US)
    // Linked phones still active, once the server has said.
    private var linkedPhoneCount: Int? = null

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
        // The row and its clipboard button both copy.
        val copyAccountId = View.OnClickListener {
            copy("account_id", sessionStore.accountId.orEmpty(), getString(R.string.onboarding_account_copied), binding.itemAccountIdCopy)
        }
        binding.itemAccountId.setOnClickListener(copyAccountId)
        binding.itemAccountIdCopy.setOnClickListener(copyAccountId)
        val copyDeviceId = View.OnClickListener {
            copy("device_id", linkedDeviceFingerprint(), getString(R.string.account_fingerprint_copied), binding.itemDeviceFingerprintCopy)
        }
        binding.itemDeviceFingerprint.setOnClickListener(copyDeviceId)
        binding.itemDeviceFingerprintCopy.setOnClickListener(copyDeviceId)
        binding.itemIdentityPassword.setOnClickListener {
            showIdentityPasswordActions()
        }
        val copySigningKey = View.OnClickListener {
            copy(
                "device_public_key",
                deviceIdentityStore.publicKeyMaterial(),
                getString(R.string.account_device_key_copied),
                binding.itemDeviceSigningKeyCopy,
            )
        }
        binding.itemDeviceSigningKey.setOnClickListener(copySigningKey)
        binding.itemDeviceSigningKeyCopy.setOnClickListener(copySigningKey)
        binding.itemLinkedDevices.setOnClickListener {
            showLinkedDevices()
        }
        binding.itemWallet.setOnClickListener {
            startActivity(WalletActivity.createIntent(this))
        }
        binding.itemWipeUserData.setOnClickListener {
            confirmLocalDataWipe()
        }
        binding.itemDeleteIdentity.setOnClickListener {
            confirmLocalIdentityReset()
        }

        renderIdentity()
    }

    override fun onResume() {
        super.onResume()
        if (::binding.isInitialized) {
            refreshWallet()
            refreshLinkedDeviceCount()
        }
    }

    /** Shows the trial's time left or the balance on the Wallet line. */
    private fun refreshWallet() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            return
        }
        lifecycleScope.launch {
            runCatching { api.getEntitlement(sessionStore.baseUrl, accountId, deviceId) }
                .onSuccess { binding.itemWalletSubtitle.text = it.walletSubtitle(this@AccountIdentityActivity) }
        }
    }

    private fun renderIdentity() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        val hasLinkedIdentity = accountId.isNotBlank() && deviceId.isNotBlank()

        binding.itemAccountIdSubtitle.text = accountId.ifBlank { getString(R.string.account_missing) }
        binding.itemDeviceFingerprintSubtitle.text = linkedDeviceFingerprint().ifBlank { getString(R.string.device_missing) }
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
            linkedPhoneCount?.takeIf { it > 0 }
                ?.let { resources.getQuantityString(R.plurals.account_linked_devices_count, it, it) }
                ?: getString(R.string.account_linked_devices_subtitle)
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

    private fun accessibilityLabel(title: CharSequence, detail: CharSequence): String {
        return getString(R.string.accessibility_setting_value, title, detail)
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
                renderLinkedDeviceCount(devices)
                if (devices.isEmpty()) {
                    toast(getString(R.string.account_linked_devices_empty))
                    return@onSuccess
                }
                val activeCount = devices.count { it.revokedAt == null }
                val sheet = VirtroidSheet(this@AccountIdentityActivity)
                    .title(getString(R.string.account_linked_devices_title))
                    .message(getString(R.string.account_linked_devices_body))
                // This phone first, then the others; revoked phones last.
                devices
                    .sortedWith(compareBy({ it.revokedAt != null }, { it.id != deviceId }))
                    .forEach { device ->
                        val name = device.name.ifBlank { shortId(device.id) }
                        val isThisPhone = device.id == deviceId
                        val revoked = device.revokedAt != null
                        sheet.row(
                            icon = R.drawable.ic_mobile,
                            title = name,
                            subtitle = if (isThisPhone && !revoked) {
                                getString(R.string.account_linked_devices_this_phone, formatRemoteDate(device.createdAt))
                            } else {
                                linkedDeviceDetail(device)
                            },
                            action = when {
                                revoked -> null
                                isThisPhone -> getString(R.string.account_linked_devices_sign_out)
                                else -> getString(R.string.account_revoke_device_confirm)
                            },
                            actionTone = VirtroidSheet.Tone.DANGER,
                            dimmed = revoked,
                            onClick = if (revoked) null else {
                                { confirmDeviceRevoke(device, activeCount) }
                            },
                        )
                    }
                sheet.note(getString(R.string.account_linked_devices_add_note))
                    .secondary(getString(R.string.identity_password_close))
                    .show()
            }.onFailure { error ->
                binding.itemLinkedDevices.isEnabled = true
                appLogs.error("Linked device list failed: ${error.message}", "auth")
                toast(error.virtroidDisplayMessage(this@AccountIdentityActivity))
            }
        }
    }

    /** One line: when the phone was added and last seen, or when it was revoked. */
    private fun linkedDeviceDetail(device: TrustedDeviceSummary): String {
        device.revokedAt?.takeIf { it.isNotBlank() }?.let {
            return getString(R.string.account_linked_devices_revoked, formatRemoteDate(it))
        }
        val seen = device.lastSeenAt
            ?.takeIf { it.isNotBlank() }
            ?.let { getString(R.string.account_linked_devices_last_seen, formatRemoteDate(it)) }
            ?: getString(R.string.account_linked_devices_never_seen)
        return getString(R.string.account_linked_devices_active, formatRemoteDate(device.createdAt), seen)
    }

    private fun refreshLinkedDeviceCount() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            return
        }
        lifecycleScope.launch {
            runCatching { api.listDevices(sessionStore.baseUrl, accountId, deviceId) }
                .onSuccess(::renderLinkedDeviceCount)
        }
    }

    private fun renderLinkedDeviceCount(devices: List<TrustedDeviceSummary>) {
        linkedPhoneCount = devices.count { it.revokedAt == null }
        renderIdentity()
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
        showConfirmation(
            title = getString(R.string.account_revoke_device_title),
            message = message,
            confirmLabel = getString(R.string.account_revoke_device_confirm),
            destructive = true,
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
        showConfirmation(
            title = getString(R.string.account_wipe_local_title),
            message = getString(R.string.account_wipe_local_confirm_body),
            confirmLabel = getString(R.string.account_wipe_local_confirm),
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

        val sheet = VirtroidSheet(this).title(getString(R.string.account_identity_password_title))
        if (identityPasswordStore.isConfigured(accountId, deviceId)) {
            sheet.message(getString(R.string.identity_password_change_body))
                .row(
                    icon = R.drawable.ic_edit,
                    title = getString(R.string.identity_password_change_row),
                    subtitle = getString(R.string.identity_password_change_row_subtitle),
                ) { changeIdentityPassword(accountId, deviceId) }
            if (identityPasswordStore.isUnlockedFor(accountId, deviceId)) {
                sheet.row(
                    icon = R.drawable.ic_lock,
                    title = getString(R.string.identity_password_lock_row),
                    subtitle = getString(R.string.identity_password_lock_row_subtitle),
                ) {
                    identityPasswordStore.clearUnlocked()
                    renderIdentity()
                    Notice.confirm(this, getString(R.string.identity_password_cache_cleared))
                }
            }
        } else {
            sheet.message(getString(R.string.identity_password_setup_body))
                .row(
                    icon = R.drawable.ic_lock,
                    title = getString(R.string.identity_password_set_action),
                ) { configureIdentityPassword(accountId, deviceId) }
        }
        sheet.secondary(getString(R.string.identity_password_close)).show()
    }

    private fun configureIdentityPassword(accountId: String, deviceId: String) {
        lifecycleScope.launch {
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
                Notice.confirm(this@AccountIdentityActivity, getString(R.string.identity_password_updated))
            }.onFailure { error ->
                appLogs.error("Identity blob password setup failed: ${error.message}", "auth")
                toast(error.virtroidDisplayMessage(this@AccountIdentityActivity))
            }
        }
    }

    /**
     * Rewraps the account key under a new password: the current one opens the
     * stored envelope, so a wrong one changes nothing.
     */
    private fun changeIdentityPassword(accountId: String, deviceId: String) {
        lifecycleScope.launch {
            val current = promptIdentityPassword(
                title = getString(R.string.identity_password_current_title),
                hint = getString(R.string.identity_password_current_body),
            )?.takeIf { it.isNotBlank() } ?: return@launch
            val next = promptIdentityPasswordSetup(
                title = getString(R.string.identity_password_new_title),
                body = getString(R.string.identity_password_new_body),
            ) ?: return@launch
            if (next == current) {
                toast(getString(R.string.identity_password_same))
                return@launch
            }

            binding.itemIdentityPassword.isEnabled = false
            binding.itemIdentityPasswordSubtitle.text = getString(R.string.identity_password_changing)
            runCatching {
                identityKeyManager.changePassword(sessionStore.baseUrl, accountId, deviceId, current, next)
            }.onSuccess {
                appLogs.info("Identity password changed", "auth")
                Notice.confirm(this@AccountIdentityActivity, getString(R.string.identity_password_changed))
            }.onFailure { error ->
                appLogs.error("Identity password change failed: ${error.message}", "auth")
                toast(error.virtroidDisplayMessage(this@AccountIdentityActivity))
            }
            binding.itemIdentityPassword.isEnabled = true
            renderIdentity()
        }
    }

    private fun confirmLocalIdentityReset() {
        showConfirmation(
            title = getString(R.string.account_delete_local_identity_title),
            message = getString(R.string.account_delete_local_identity_confirm_body),
            confirmLabel = getString(R.string.account_delete_local_identity_confirm),
            destructive = true,
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

    private fun copy(label: String, value: String, message: String, button: ImageButton) {
        if (value.isBlank()) {
            return
        }
        copySensitiveToClipboard(label, value)
        Notice.confirm(this, message)
        // The button turns into an accent tick for a moment.
        button.setImageResource(R.drawable.ic_check)
        button.imageTintList = ColorStateList.valueOf(getColor(R.color.v_accent))
        button.removeCallbacks(null)
        button.postDelayed({
            button.setImageResource(R.drawable.ic_copy)
            button.imageTintList = ColorStateList.valueOf(getColor(R.color.v_text_muted))
        }, COPIED_TICK_MS)
    }

    private fun shortId(value: String): String {
        return if (value.length <= 18) {
            value
        } else {
            value.take(8) + "..." + value.takeLast(6)
        }
    }

    private fun formatRemoteDate(value: String): String {
        return runCatching {
            OffsetDateTime.parse(value).format(dateFormatter)
        }.getOrDefault(shortId(value))
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
        Notice.show(this, message)
    }

    companion object {
        private const val COPIED_TICK_MS = 1_500L

        fun createIntent(context: Context): Intent = Intent(context, AccountIdentityActivity::class.java)
    }
}
