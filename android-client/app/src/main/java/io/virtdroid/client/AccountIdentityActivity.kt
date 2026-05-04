package io.virtdroid.client

import android.content.ClipData
import android.content.ClipboardManager
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
import io.virtdroid.client.api.VirtdroidApi
import io.virtdroid.client.data.SessionStore
import io.virtdroid.client.databinding.ScreenAccountIdentityBinding
import io.virtdroid.client.security.enableSecureWindow
import kotlinx.coroutines.launch
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.util.Locale

class AccountIdentityActivity : AppCompatActivity() {
    private lateinit var binding: ScreenAccountIdentityBinding
    private lateinit var sessionStore: SessionStore
    private val api = VirtdroidApi()
    private val timestampFormatter = DateTimeFormatter.ofPattern("MMM d, yyyy", Locale.US)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenAccountIdentityBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.topAppBar) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = 24 + bars.top)
            insets
        }

        binding.buttonBack.setOnClickListener { finish() }
        binding.buttonSettings.setOnClickListener { toast(getString(R.string.status_idle)) }
        binding.itemAccountId.setOnClickListener {
            copy("account_id", sessionStore.accountId.orEmpty(), getString(R.string.onboarding_account_copied))
        }
        binding.itemDeviceFingerprint.setOnClickListener {
            copy("device_id", sessionStore.deviceId.orEmpty(), getString(R.string.onboarding_account_copied))
        }
        binding.itemIdentityPassword.setOnClickListener {
            toast(getString(R.string.identity_unlock_title))
        }
        binding.itemDeviceSigningKey.setOnClickListener {
            toast(getString(R.string.account_present))
        }
        binding.itemWipeUserData.setOnClickListener {
            toast(getString(R.string.controls_wipe_title))
        }
        binding.itemDeleteIdentity.setOnClickListener {
            toast(getString(R.string.controls_delete_confirm_title))
        }

        renderIdentity()
        refreshStorage()
    }

    private fun renderIdentity() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()

        binding.itemAccountIdSubtitle.text = accountId.ifBlank { getString(R.string.account_missing) }
        binding.itemDeviceFingerprintSubtitle.text = if (deviceId.isBlank()) {
            getString(R.string.device_missing)
        } else {
            "${shortId(deviceId)} / Registered"
        }
        binding.identityEncryptionValue.text = getString(R.string.identity_password_title)
        binding.identityCreatedValue.text = getString(R.string.status_idle)
        binding.identityLastSyncValue.text = getString(R.string.status_idle)
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
                binding.storageReadyChip.text = storage.status.ifBlank { getString(R.string.status_idle) }
                binding.storageCreditValue.text = storage.walletAddress?.takeIf { it.isNotBlank() }?.let(::shortId) ?: "--"
                binding.storageUsageValue.text = "0"
                binding.storageUsageUnit.text = " MB"
                binding.storageUsageSubtitle.text = "No snapshot usage reported"
                binding.storageSnapshots.text = "No snapshots"
                binding.storageRunwayIcon.text = "--"
                binding.storageRunwayValue.text = "--"
                binding.identityLastSyncValue.text = OffsetDateTime.now().format(timestampFormatter)
            }
        }
    }

    private fun copy(label: String, value: String, message: String) {
        if (value.isBlank()) {
            return
        }
        val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        clipboard.setPrimaryClip(ClipData.newPlainText(label, value))
        toast(message)
    }

    private fun shortId(value: String): String {
        return if (value.length <= 18) {
            value
        } else {
            value.take(8) + "..." + value.takeLast(6)
        }
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        fun createIntent(context: Context): Intent = Intent(context, AccountIdentityActivity::class.java)
    }
}
