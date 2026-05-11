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
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenAccountIdentityBinding
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.DeviceIdentityStore
import io.virtroid.client.security.IdentityCrypto
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.copySensitiveToClipboard
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPassword
import io.virtroid.client.security.promptIdentityPasswordWithConfirmation
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
        appLockStore = AppLockStore(this)
        appLogs = AppLogStore.get(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.topAppBar) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = 24 + bars.top)
            insets
        }

        binding.buttonBack.setOnClickListener { finish() }
        binding.buttonSettings.setOnClickListener {
            startActivity(PrivacySecurityActivity.createIntent(this))
        }
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
        binding.itemWipeUserData.setOnClickListener {
            confirmLocalDataWipe()
        }
        binding.itemDeleteIdentity.setOnClickListener {
            confirmLocalIdentityReset()
        }

        renderIdentity()
        refreshStorage()
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
        binding.identityCreatedValue.text = if (hasLinkedIdentity) {
            getString(R.string.account_identity_created)
        } else {
            getString(R.string.account_identity_not_linked)
        }
        binding.identityLastSyncValue.text = getString(R.string.account_storage_not_synced)
    }

    private fun refreshStorage() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            return
        }

        lifecycleScope.launch {
            runCatching {
                api.getStorage(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess { storage ->
                binding.storageBackendValue.text = storage.provider.ifBlank { "local" }
                binding.storageReadyChip.text = storage.status.ifBlank { getString(R.string.account_storage_unavailable) }
                binding.storageCreditValue.text = storage.walletAddress?.takeIf { it.isNotBlank() }?.let(::shortId) ?: "--"
                binding.storageUsageValue.text = "--"
                binding.storageUsageUnit.text = ""
                binding.storageUsageSubtitle.text = getString(R.string.account_storage_usage_unreported)
                binding.storageSnapshots.text = getString(R.string.account_storage_snapshots_unreported)
                binding.storageRunwayIcon.text = "--"
                binding.storageRunwayValue.text = getString(R.string.account_storage_runway_unreported)
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

    private fun confirmLocalDataWipe() {
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.account_wipe_local_title))
            .setMessage(getString(R.string.account_wipe_local_body))
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.privacy_force_clear_cache)) { _, _ ->
                activeSessionStore.clear()
                identityPasswordStore.clearUnlocked()
                val result = appSettings.clearSafeLocalCache()
                appLogs.warn("Local user data cache wipe completed: ${result.deletedItems} items", "privacy")
                toast(getString(R.string.account_wipe_local_done, result.deletedItems))
            }
            .show()
    }

    private fun showIdentityPasswordActions() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            toast(getString(R.string.account_identity_not_linked))
            return
        }

        val configured = identityPasswordStore.isConfigured(accountId, deviceId)
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.identity_password_title))
            .setMessage(
                if (configured) {
                    getString(R.string.identity_password_reset_body)
                } else {
                    getString(R.string.identity_password_setup_body)
                },
            )
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setNeutralButton(getString(R.string.identity_password_clear_cache)) { _, _ ->
                identityPasswordStore.clearUnlocked()
                renderIdentity()
                toast(getString(R.string.identity_password_cache_cleared))
            }
            .setPositiveButton(
                if (configured) getString(R.string.identity_password_reset_action)
                else getString(R.string.identity_password_set_action),
            ) { _, _ ->
                resetIdentityPassword(accountId, deviceId)
            }
            .show()
    }

    private fun resetIdentityPassword(accountId: String, deviceId: String) {
        lifecycleScope.launch {
            val configured = identityPasswordStore.isConfigured(accountId, deviceId)
            val currentBlobAccessKey = if (configured) {
                val currentPassword = promptIdentityPassword(
                    title = getString(R.string.identity_password_title),
                    hint = getString(R.string.identity_password_current_prompt),
                )?.trim()
                when {
                    currentPassword == null -> return@launch
                    currentPassword.isBlank() -> {
                        toast(getString(R.string.identity_password_required))
                        return@launch
                    }
                    else -> IdentityCrypto.deriveBlobAccessKey(accountId, deviceId, currentPassword)
                }
            } else {
                null
            }

            val password = promptIdentityPasswordWithConfirmation(
                title = getString(R.string.identity_password_title),
                hint = getString(R.string.identity_password_prompt),
                confirmHint = getString(R.string.identity_password_confirm_prompt),
            )?.trim()
            when {
                password == null -> return@launch
                password.isBlank() -> {
                    toast(getString(R.string.identity_password_mismatch))
                    return@launch
                }
            }

            runCatching {
                val blobAccessKey = IdentityCrypto.deriveBlobAccessKey(accountId, deviceId, password)
                val verifier = IdentityCrypto.blobKeyVerifier(blobAccessKey)
                api.registerIdentity(
                    baseUrl = sessionStore.baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    blobKeyVerifier = verifier,
                    currentBlobAccessKey = currentBlobAccessKey,
                )
                identityPasswordStore.unlock(accountId, deviceId, password)
                identityPasswordStore.saveConfigured(accountId, deviceId)
            }.onSuccess {
                appLogs.warn("Identity blob password verifier updated", "auth")
                renderIdentity()
                toast(getString(R.string.identity_password_updated))
            }.onFailure { error ->
                appLogs.error("Identity blob password update failed: ${error.message}", "auth")
                toast(error.virtroidDisplayMessage(this@AccountIdentityActivity))
            }
        }
    }

    private fun confirmLocalIdentityReset() {
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.account_delete_local_identity_title))
            .setMessage(getString(R.string.account_delete_local_identity_body))
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.account_delete_local_identity_confirm)) { _, _ ->
                deleteServerAccountAndResetLocal()
            }
            .show()
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
        identityPasswordStore.clearConfigured()
        appLockStore.clearCredential()
        appSettings.clearSafeLocalCache()
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
