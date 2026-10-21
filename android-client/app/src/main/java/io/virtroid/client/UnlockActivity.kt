package io.virtroid.client

import android.content.Intent
import android.os.Bundle
import android.text.InputType
import androidx.appcompat.app.AppCompatActivity
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isInvisible
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenPinAuthenticationBinding
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.ui.Notice
import io.virtroid.client.ui.VirtroidMotion

class UnlockActivity : AppCompatActivity() {
    private lateinit var binding: ScreenPinAuthenticationBinding
    private lateinit var appLockStore: AppLockStore
    private lateinit var appSettings: AppSettingsStore
    private lateinit var appLogs: AppLogStore
    private var pinBuffer = StringBuilder()
    private var showingPassphrase = false
    private var returnToPrevious = false
    private var requiredPinLength = AppLockStore.LEGACY_PIN_LENGTH
    // Set while a wrong PIN shakes or a right one plays out, so taps then are ignored.
    private var inputLocked = false
    private val pinDots by lazy {
        listOf(
            binding.pinDot1,
            binding.pinDot2,
            binding.pinDot3,
            binding.pinDot4,
            binding.pinDot5,
            binding.pinDot6,
            binding.pinDot7,
            binding.pinDot8,
        )
    }

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
        requiredPinLength = appLockStore.requiredPinLength()
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
            button.contentDescription = value
            VirtroidMotion.pressFeedback(button)
            button.setOnClickListener { appendPin(value) }
        }
        VirtroidMotion.pressFeedback(binding.pinDeleteButton)
        binding.pinDeleteButton.setOnClickListener {
            if (!inputLocked && pinBuffer.isNotEmpty()) {
                val removed = pinBuffer.lastIndex
                pinBuffer.deleteAt(removed)
                updateDots()
                VirtroidMotion.dip(pinDots[removed])
            }
        }
        binding.unlockButton.setOnClickListener { unlockWithPassphrase() }
        binding.switchUnlockModeText.setOnClickListener { toggleUnlockMode() }
        binding.buttonFingerprint.setOnClickListener { unlockWithBiometric() }

        showingPassphrase = appLockStore.mode == AppLockStore.LockMode.PASSPHRASE
        renderMode()
        renderBiometric()
        updateDots()
        if (savedInstanceState == null) {
            val rows = (0 until binding.pinPad.childCount).map { binding.pinPad.getChildAt(it) }
            VirtroidMotion.riseIn(listOf(binding.pinHeader) + rows)
        }
    }

    private fun appendPin(value: String) {
        if (inputLocked || showingPassphrase || pinBuffer.length >= requiredPinLength) return
        pinBuffer.append(value)
        updateDots()
        VirtroidMotion.popIn(pinDots[pinBuffer.lastIndex])
        if (pinBuffer.length == requiredPinLength) {
            if (showLockoutIfNeeded()) {
                rejectPin()
                return
            }
            if (appLockStore.verify(pinBuffer.toString())) {
                acceptPin()
            } else {
                rejectPin()
                toast(getString(R.string.lock_invalid_pin))
            }
        }
    }

    /** A wrong PIN: the dots turn red and shake, then empty. */
    private fun rejectPin() {
        inputLocked = true
        pinDots.take(requiredPinLength).forEach { it.setBackgroundResource(R.drawable.bg_pin_dot_error) }
        VirtroidMotion.rejectHaptic(binding.pinDotsRow)
        VirtroidMotion.shake(binding.pinDotsRow) {
            pinBuffer = StringBuilder()
            updateDots()
            inputLocked = false
        }
    }

    /** The right PIN: the dots ripple and the lock pulses before the app opens. */
    private fun acceptPin() {
        inputLocked = true
        pinBuffer = StringBuilder(pinBuffer.length).append("x".repeat(pinBuffer.length))
        VirtroidMotion.confirmHaptic(binding.pinDotsRow)
        VirtroidMotion.pulse(binding.iconLock)
        val waveMs = VirtroidMotion.wave(pinDots.take(requiredPinLength))
        binding.root.postDelayed({ launchMain() }, waveMs)
    }

    private fun unlockWithPassphrase() {
        val value = binding.passphraseInput.text?.toString().orEmpty()
        if (showLockoutIfNeeded()) {
            return
        }
        if (appLockStore.verify(value)) {
            launchMain()
        } else {
            VirtroidMotion.rejectHaptic(binding.passphraseInput)
            VirtroidMotion.shake(binding.passphraseInput)
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
        binding.passphraseInput.inputType = if (passphraseOnly) {
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        } else {
            InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_VARIATION_PASSWORD
        }
        binding.passphraseInput.hint = if (passphraseOnly) {
            getString(R.string.unlock_enter_passphrase)
        } else {
            getString(R.string.lock_pin_field_hint, requiredPinLength)
        }
        binding.passphraseSection.isVisible = effectivePassphrase
        binding.pinPad.isVisible = !effectivePassphrase
        binding.pinDotsRow.isVisible = !effectivePassphrase
        binding.switchUnlockModeText.isVisible = !passphraseOnly
        binding.switchUnlockModeText.text = getString(
            if (effectivePassphrase) R.string.unlock_use_pin_pad else R.string.unlock_use_keyboard,
        )
        renderBiometric()
    }

    private fun renderBiometric() {
        val canUseBiometric = appSettings.biometricUnlockEnabled &&
            appLockStore.canUseBiometricUnlock() &&
            appLockStore.mode == AppLockStore.LockMode.PIN &&
            BiometricManager.from(this).canAuthenticate(BIOMETRIC_AUTHENTICATORS) ==
            BiometricManager.BIOMETRIC_SUCCESS
        binding.buttonFingerprint.isInvisible = !showingPassphrase || !canUseBiometric
    }

    private fun unlockWithBiometric() {
        val cipher = appLockStore.biometricDecryptCipher()
        if (cipher == null) {
            toast(getString(R.string.lock_biometric_vault_unavailable))
            renderBiometric()
            return
        }
        val prompt = BiometricPrompt(
            this,
            ContextCompat.getMainExecutor(this),
            object : BiometricPrompt.AuthenticationCallback() {
                override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                    super.onAuthenticationSucceeded(result)
                    if (appLockStore.unlockWithBiometric(result.cryptoObject?.cipher)) {
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
            BiometricPrompt.CryptoObject(cipher),
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
        binding.pinDotsRow.contentDescription = getString(
            R.string.lock_pin_progress,
            pinBuffer.length,
            requiredPinLength,
        )
        pinDots.forEachIndexed { index, view ->
            view.isVisible = index < requiredPinLength
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
        val hasIdentity = sessionStore.hasAccess() &&
            identityPasswordStore.isConfigured(sessionStore.accountId, sessionStore.deviceId)
        val destination = when {
            !hasIdentity -> Intent(this, OnboardingActivity::class.java)
            !AppSettingsStore(this).permissionsSetupCompleted -> PermissionsActivity.createIntent(this)
            else -> Intent(this, MainActivity::class.java)
        }
        startActivity(
            destination.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
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
        Notice.show(this, message)
    }

    companion object {
        const val EXTRA_RETURN_TO_PREVIOUS = "return_to_previous"
        private const val BIOMETRIC_AUTHENTICATORS = BiometricManager.Authenticators.BIOMETRIC_STRONG
    }
}
