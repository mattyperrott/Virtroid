package io.virtroid.client

import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenPinAuthenticationBinding
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow

class UnlockActivity : AppCompatActivity() {
    private lateinit var binding: ScreenPinAuthenticationBinding
    private lateinit var appLockStore: AppLockStore
    private lateinit var appSettings: AppSettingsStore
    private lateinit var appLogs: AppLogStore
    private var pinBuffer = StringBuilder()
    private var showingPassphrase = false
    private var returnToPrevious = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenPinAuthenticationBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        ViewCompat.setOnApplyWindowInsetsListener(binding.unlockRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 28 + bars.top,
                right = 24 + bars.right,
                bottom = 28 + bars.bottom,
            )
            insets
        }

        appLockStore = AppLockStore(this)
        appSettings = AppSettingsStore(this)
        appLogs = AppLogStore.get(this)
        returnToPrevious = intent.getBooleanExtra(EXTRA_RETURN_TO_PREVIOUS, false)
        if (!appLockStore.isEnabled() || !appLockStore.hasCredential()) {
            launchOnboarding()
            return
        }

        val pinButtons = listOf(
            binding.pinButton0 to "0",
            binding.pinButton1 to "1",
            binding.pinButton2 to "2",
            binding.pinButton3 to "3",
            binding.pinButton4 to "4",
            binding.pinButton5 to "5",
            binding.pinButton6 to "6",
            binding.pinButton7 to "7",
            binding.pinButton8 to "8",
            binding.pinButton9 to "9",
        )
        pinButtons.forEach { (button, value) ->
            button.setOnClickListener { appendPin(value) }
        }
        binding.pinDeleteButton.setOnClickListener {
            if (pinBuffer.isNotEmpty()) {
                pinBuffer.deleteAt(pinBuffer.lastIndex)
                updateDots()
            }
        }
        binding.unlockButton.setOnClickListener { unlockWithPassphrase() }
        binding.switchUnlockModeText.setOnClickListener { toggleUnlockMode() }
        binding.buttonFingerprint.setOnClickListener { unlockWithBiometric() }

        showingPassphrase = appLockStore.mode == AppLockStore.LockMode.PASSPHRASE
        renderMode()
        renderBiometric()
    }

    private fun appendPin(value: String) {
        if (showingPassphrase || pinBuffer.length >= 6) return
        pinBuffer.append(value)
        updateDots()
        if (pinBuffer.length == 6) {
            if (showLockoutIfNeeded()) {
                pinBuffer = StringBuilder()
                updateDots()
                return
            }
            if (appLockStore.verify(pinBuffer.toString())) {
                launchMain()
            } else {
                pinBuffer = StringBuilder()
                updateDots()
                toast(getString(R.string.lock_invalid_pin))
            }
        }
    }

    private fun unlockWithPassphrase() {
        val value = binding.passphraseInput.text?.toString().orEmpty()
        if (showLockoutIfNeeded()) {
            return
        }
        if (appLockStore.verify(value)) {
            launchMain()
        } else {
            toast(getString(R.string.lock_invalid_passphrase))
        }
    }

    private fun toggleUnlockMode() {
        showingPassphrase = !showingPassphrase
        renderMode()
    }

    private fun renderMode() {
        val passphraseOnly = appLockStore.mode == AppLockStore.LockMode.PASSPHRASE
        val effectivePassphrase = passphraseOnly || showingPassphrase
        binding.passphraseSection.isVisible = effectivePassphrase
        binding.pinPad.isVisible = !effectivePassphrase
        binding.pinDotsRow.isVisible = !effectivePassphrase
        binding.switchUnlockModeText.isVisible = !passphraseOnly
        renderBiometric()
    }

    private fun renderBiometric() {
        val canUseBiometric = appSettings.biometricUnlockEnabled &&
            appLockStore.canUseBiometricUnlock() &&
            appLockStore.mode == AppLockStore.LockMode.PIN &&
            BiometricManager.from(this).canAuthenticate(BIOMETRIC_AUTHENTICATORS) ==
            BiometricManager.BIOMETRIC_SUCCESS
        binding.buttonFingerprint.isVisible = !showingPassphrase && canUseBiometric
    }

    private fun unlockWithBiometric() {
        val prompt = BiometricPrompt(
            this,
            ContextCompat.getMainExecutor(this),
            object : BiometricPrompt.AuthenticationCallback() {
                override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                    super.onAuthenticationSucceeded(result)
                    if (appLockStore.markUnlocked()) {
                        appLogs.info("Biometric unlock succeeded", "auth")
                        launchNext()
                    } else {
                        toast(getString(R.string.lock_biometric_vault_unavailable))
                    }
                }

                override fun onAuthenticationFailed() {
                    super.onAuthenticationFailed()
                    appLogs.warn("Biometric unlock failed", "auth")
                }
            },
        )
        prompt.authenticate(
            BiometricPrompt.PromptInfo.Builder()
                .setTitle(getString(R.string.biometric_unlock_title))
                .setSubtitle(getString(R.string.biometric_unlock_subtitle))
                .setNegativeButtonText(getString(R.string.biometric_unlock_use_pin))
                .setAllowedAuthenticators(BIOMETRIC_AUTHENTICATORS)
                .build(),
        )
    }

    private fun showLockoutIfNeeded(): Boolean {
        val remainingMs = appLockStore.lockoutRemainingMs()
        if (remainingMs <= 0L) {
            return false
        }
        toast(getString(R.string.lock_try_again, (remainingMs / 1_000L).coerceAtLeast(1L)))
        return true
    }

    private fun updateDots() {
        val dots = listOf(binding.pinDot1, binding.pinDot2, binding.pinDot3, binding.pinDot4, binding.pinDot5, binding.pinDot6)
        dots.forEachIndexed { index, view ->
            view.setBackgroundResource(
                if (index < pinBuffer.length) R.drawable.bg_pin_dot_active else R.drawable.bg_pin_dot_inactive,
            )
        }
    }

    private fun launchMain() {
        launchNext()
    }

    private fun launchNext() {
        if (returnToPrevious) {
            finish()
            return
        }
        val sessionStore = SessionStore(this)
        val identityPasswordStore = IdentityPasswordStore(this)
        val destination = if (
            sessionStore.hasAccess() &&
            identityPasswordStore.isConfigured(sessionStore.accountId, sessionStore.deviceId)
        ) {
            MainActivity::class.java
        } else {
            OnboardingActivity::class.java
        }
        startActivity(
            Intent(this, destination)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
        )
        finish()
    }

    private fun launchOnboarding() {
        startActivity(
            Intent(this, OnboardingActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
        )
        finish()
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        const val EXTRA_RETURN_TO_PREVIOUS = "return_to_previous"
        private const val BIOMETRIC_AUTHENTICATORS = BiometricManager.Authenticators.BIOMETRIC_STRONG
    }
}
