package io.virtdroid.client

import android.content.ClipData
import android.content.ClipboardManager
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
import io.virtdroid.client.databinding.ScreenIdentityProvisioningBinding
import io.virtdroid.client.device.DeviceRuntimeProfile
import io.virtdroid.client.security.AppLockStore
import io.virtdroid.client.security.DeviceIdentityStore
import io.virtdroid.client.security.IdentityCrypto
import io.virtdroid.client.security.IdentityPasswordStore
import io.virtdroid.client.security.enableSecureWindow
import io.virtdroid.client.security.promptPinCode
import io.virtdroid.client.security.promptIdentityPasswordWithConfirmation
import kotlinx.coroutines.launch

class OnboardingActivity : AppCompatActivity() {
    private lateinit var binding: ScreenIdentityProvisioningBinding
    private val api = VirtdroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var appLockStore: AppLockStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private val deviceIdentityStore = DeviceIdentityStore()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenIdentityProvisioningBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        appLockStore = AppLockStore(this)
        identityPasswordStore = IdentityPasswordStore(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.onboardingRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 24 + bars.top,
                right = 24 + bars.right,
                bottom = 28 + bars.bottom,
            )
            insets
        }

        binding.copyAccountButton.setOnClickListener { copyAccountId() }
        binding.continueSetupButton.setOnClickListener {
            if (sessionStore.hasAccess()) {
                completeSetup()
            } else {
                createIdentity()
            }
        }

        renderSecurityRequirements()
        refreshIdentityState()
    }

    private fun createIdentity() {
        binding.continueSetupButton.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                val result = api.bootstrap(
                    baseUrl = sessionStore.baseUrl,
                    deviceName = deviceIdentityStore.defaultDeviceName(),
                    publicKey = deviceIdentityStore.publicKeyMaterial(),
                    runtimeProfile = DeviceRuntimeProfile.from(this@OnboardingActivity),
                    bootstrapToken = BuildConfig.DEFAULT_BOOTSTRAP_INVITE_TOKEN,
                )
                sessionStore.saveBootstrap(result.accountId, result.deviceId)
            }.onSuccess {
                refreshIdentityState()
                completeSetup()
            }.onFailure {
                toast(it.message ?: getString(R.string.status_error))
            }
            binding.continueSetupButton.isEnabled = true
        }
    }

    private fun refreshIdentityState() {
        val hasAccess = sessionStore.hasAccess()
        binding.onboardingAccountIdText.text = sessionStore.accountId ?: getString(R.string.onboarding_not_provisioned)
        binding.onboardingDeviceIdText.text = sessionStore.deviceId ?: getString(R.string.onboarding_not_provisioned)
        binding.continueSetupButton.isEnabled = true
        binding.continueSetupButton.setText(
            if (hasAccess) R.string.onboarding_continue else R.string.onboarding_create_identity,
        )
    }

    private fun renderSecurityRequirements() {
        binding.passphraseOptionCard.setBackgroundResource(R.drawable.bg_selected_card_24)
        binding.pinOptionCard.setBackgroundResource(R.drawable.bg_selected_card_24)
        binding.passphraseOptionIndicator.setBackgroundResource(R.drawable.bg_dot_accent_outline)
        binding.pinOptionIndicator.setBackgroundResource(R.drawable.bg_dot_accent_outline)
    }

    private fun completeSetup() {
        if (!sessionStore.hasAccess()) {
            toast(getString(R.string.new_runtime_missing_access))
            return
        }

        lifecycleScope.launch {
            val accountId = sessionStore.accountId ?: return@launch
            val deviceId = sessionStore.deviceId ?: return@launch

            if (!identityPasswordStore.isConfigured(accountId, deviceId)) {
                val password = promptIdentityPasswordWithConfirmation(
                    title = getString(R.string.identity_password_title),
                    hint = getString(R.string.identity_password_prompt),
                    confirmHint = getString(R.string.identity_password_confirm_prompt),
                )
                when {
                    password == null -> return@launch
                    password.isBlank() -> {
                        toast(getString(R.string.identity_password_mismatch))
                        return@launch
                    }
                }

                val blobAccessKey = IdentityCrypto.deriveBlobAccessKey(accountId, deviceId, password)
                val blobKeyVerifier = IdentityCrypto.blobKeyVerifier(blobAccessKey)

                runCatching {
                    api.registerIdentity(
                        baseUrl = sessionStore.baseUrl,
                        accountId = accountId,
                        deviceId = deviceId,
                        blobKeyVerifier = blobKeyVerifier,
                    )
                }.onSuccess {
                    identityPasswordStore.saveConfigured(accountId, deviceId)
                    identityPasswordStore.unlock(accountId, deviceId, password)
                }.onFailure {
                    toast(it.message ?: getString(R.string.status_error))
                    return@launch
                }
            }

            if (appLockStore.hasCredential()) {
                appLockStore.markUnlocked()
                launchMain()
                return@launch
            }

            promptForPin()
        }
    }

    private fun promptForPin() {
        lifecycleScope.launch {
            val initial = promptPinCode(
                title = getString(R.string.onboarding_pin_title),
                hint = getString(R.string.lock_pin_prompt),
            )?.trim().orEmpty()
            if (initial.isBlank()) {
                return@launch
            }
            if (initial.length != 6 || initial.any { !it.isDigit() }) {
                toast(getString(R.string.lock_invalid_pin))
                return@launch
            }
            val confirmed = promptPinCode(
                title = getString(R.string.onboarding_pin_title),
                hint = getString(R.string.lock_pin_confirm_prompt),
            )?.trim().orEmpty()
            if (initial != confirmed) {
                toast(getString(R.string.lock_pin_mismatch))
                return@launch
            }
            appLockStore.saveCredential(AppLockStore.LockMode.PIN, initial)
            launchMain()
        }
    }

    private fun launchMain() {
        startActivity(
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
        )
        finish()
    }

    private fun copyAccountId() {
        val accountId = sessionStore.accountId ?: return
        val clipboard = getSystemService(ClipboardManager::class.java)
        clipboard?.setPrimaryClip(ClipData.newPlainText("account_id", accountId))
        toast(getString(R.string.onboarding_account_copied))
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }
}
