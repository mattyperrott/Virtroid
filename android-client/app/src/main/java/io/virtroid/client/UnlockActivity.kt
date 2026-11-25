package io.virtroid.client

import android.animation.Animator
import android.content.Intent
import android.graphics.drawable.Animatable
import android.os.Bundle
import android.text.InputType
import android.view.View
import android.widget.TextView
import androidx.activity.addCallback
import androidx.appcompat.app.AppCompatActivity
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.isInvisible
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenPinAuthenticationBinding
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.ui.Notice
import io.virtroid.client.ui.SafeArea
import io.virtroid.client.ui.VirtroidMotion
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

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
    private var waiting: Animator? = null
    // Runs while too many wrong entries keep the screen from taking another.
    private var lockout: Job? = null
    private var lockoutDots = 0
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

    private val pinButtons by lazy {
        listOf(
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
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenPinAuthenticationBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        // The keyboard counts as a bar, so the passphrase field centres above it.
        ViewCompat.setOnApplyWindowInsetsListener(binding.unlockRoot) { view, insets ->
            val bars = insets.getInsets(SafeArea.barsAndKeyboard())
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
        if (returnToPrevious) {
            // Back leaves the app instead of revealing the screen beneath.
            onBackPressedDispatcher.addCallback(this) { moveTaskToBack(true) }
        }
        if (!appLockStore.isEnabled() || !appLockStore.hasCredential()) {
            launchOnboarding()
            return
        }

        pinButtons.forEach { (button, value) ->
            button.contentDescription = value
            VirtroidMotion.keyFeedback(button, label = button.getChildAt(0) as TextView)
            button.setOnClickListener { appendPin(value) }
        }
        VirtroidMotion.keyFeedback(binding.pinDeleteButton)
        VirtroidMotion.keyFeedback(binding.buttonFingerprint)
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
        showStatus(Status.ENTER)
        if (savedInstanceState == null) {
            val rows = (0 until binding.pinPad.childCount).map { binding.pinPad.getChildAt(it) }
            VirtroidMotion.riseIn(listOf(binding.pinHeader) + rows)
            // With fingerprint unlock set up, ask for it straight away; "Use PIN"
            // in the prompt leaves the pad, whose fingerprint key asks again.
            if (canUseBiometric()) {
                binding.root.post { unlockWithBiometric() }
            }
        }
    }

    private fun appendPin(value: String) {
        if (inputLocked || showingPassphrase || pinBuffer.length >= requiredPinLength) return
        pinBuffer.append(value)
        showStatus(Status.ENTER)
        updateDots()
        VirtroidMotion.popIn(pinDots[pinBuffer.lastIndex])
        if (pinBuffer.length == requiredPinLength) {
            if (showLockoutIfNeeded()) {
                rejectPin()
                return
            }
            // verify() runs two 210,000-iteration PBKDF2 derivations and opens
            // the vault, up to a second or more: the dots move meanwhile.
            inputLocked = true
            val pin = pinBuffer.toString()
            showWaiting(true)
            lifecycleScope.launch {
                val matches = withContext(Dispatchers.Default) { appLockStore.verify(pin) }
                if (matches) {
                    acceptPin()
                } else {
                    showWaiting(false)
                    rejectPin()
                    showStatus(Status.WRONG)
                }
            }
        }
    }

    /** While the PIN is checked the dots bob and the pad, which takes no taps, steps back. */
    private fun showWaiting(show: Boolean) {
        val dots = pinDots.take(requiredPinLength)
        if (show) {
            showStatus(Status.CHECKING)
            waiting = VirtroidMotion.waitingWave(dots)
        } else {
            VirtroidMotion.stopWaiting(waiting, dots)
            waiting = null
        }
        VirtroidMotion.fadeTo(binding.pinPad, if (show) PAD_WAITING_ALPHA else 1f)
    }

    private enum class Status { ENTER, CHECKING, WRONG, LOCKED_OUT }

    /** The line under the title: what to enter, or what came of it. */
    private fun showStatus(status: Status) {
        val passphraseOnly = appLockStore.mode == AppLockStore.LockMode.PASSPHRASE
        val text = when (status) {
            Status.ENTER -> if (passphraseOnly) {
                getString(R.string.unlock_enter_passphrase)
            } else {
                getString(R.string.lock_pin_field_hint, requiredPinLength)
            }
            Status.CHECKING -> getString(R.string.unlock_subtitle)
            Status.WRONG -> getString(
                if (passphraseOnly) R.string.lock_invalid_passphrase else R.string.lock_invalid_pin,
            )
            Status.LOCKED_OUT -> lockoutText(appLockStore.lockoutRemainingMs())
        }
        VirtroidMotion.changeText(binding.textSubtitle, text)
        binding.textSubtitle.setTextColor(
            getColor(
                when (status) {
                    Status.ENTER -> R.color.v_text_muted
                    Status.CHECKING -> R.color.v_accent
                    Status.WRONG, Status.LOCKED_OUT -> R.color.v_danger
                },
            ),
        )
    }

    /** A wrong PIN: the dots turn red and shake, then empty. */
    private fun rejectPin() {
        inputLocked = true
        pinDots.take(requiredPinLength).forEach { it.setBackgroundResource(R.drawable.bg_pin_dot_error) }
        VirtroidMotion.rejectHaptic(binding.pinDotsRow)
        VirtroidMotion.shake(binding.pinDotsRow) {
            pinBuffer = StringBuilder()
            if (!showLockoutIfNeeded()) {
                updateDots()
                inputLocked = false
            }
        }
    }

    /** The right PIN: the dots ripple and the app opens behind it. */
    private fun acceptPin() {
        inputLocked = true
        pinBuffer = StringBuilder(pinBuffer.length).append("x".repeat(pinBuffer.length))
        val dots = pinDots.take(requiredPinLength)
        VirtroidMotion.stopWaiting(waiting, dots)
        VirtroidMotion.wave(dots)
        openUnlocked()
    }

    /** The lock springs open and the pad leaves; the app opens as soon as that shows. */
    private fun openUnlocked() {
        // A fingerprint can open the app during a lockout.
        lockout?.cancel()
        inputLocked = true
        VirtroidMotion.confirmHaptic(binding.iconLock)
        if (!VirtroidMotion.enabled) {
            launchNext()
            return
        }
        binding.iconLock.setImageResource(R.drawable.ic_lock_opening)
        (binding.iconLock.drawable as? Animatable)?.start()
        VirtroidMotion.pulse(binding.iconLock)
        VirtroidMotion.leave(listOf(binding.pinPad, binding.passphraseSection, binding.switchUnlockModeText))
        binding.root.postDelayed({ launchNext() }, OPEN_MS)
    }

    private fun unlockWithPassphrase() {
        val value = binding.passphraseInput.text?.toString().orEmpty()
        if (showLockoutIfNeeded()) {
            return
        }
        binding.unlockButton.isEnabled = false
        VirtroidMotion.setBusy(binding.unlockButton, true)
        showStatus(Status.CHECKING)
        lifecycleScope.launch {
            val matches = withContext(Dispatchers.Default) { appLockStore.verify(value) }
            binding.unlockButton.isEnabled = true
            VirtroidMotion.setBusy(binding.unlockButton, false)
            if (matches) {
                openUnlocked()
            } else {
                VirtroidMotion.rejectHaptic(binding.passphraseInput)
                VirtroidMotion.shake(binding.passphraseInput)
                if (!showLockoutIfNeeded()) showStatus(Status.WRONG)
            }
        }
    }

    private fun toggleUnlockMode() {
        showingPassphrase = !showingPassphrase
        renderMode()
    }

    override fun onResume() {
        super.onResume()
        // A second lock screen left in the task after another one unlocked
        // the app has nothing left to guard.
        if (returnToPrevious && appLockStore.isUnlocked()) {
            finish()
            return
        }
        showLockoutIfNeeded()
    }

    override fun onDestroy() {
        waiting?.cancel()
        super.onDestroy()
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

    private fun canUseBiometric(): Boolean =
        appSettings.biometricUnlockEnabled &&
            appLockStore.canUseBiometricUnlock() &&
            appLockStore.mode == AppLockStore.LockMode.PIN &&
            BiometricManager.from(this).canAuthenticate(BIOMETRIC_AUTHENTICATORS) ==
            BiometricManager.BIOMETRIC_SUCCESS

    /** The fingerprint key sits in the PIN pad, so it shows only with the pad. */
    private fun renderBiometric() {
        binding.buttonFingerprint.isInvisible = showingPassphrase || !canUseBiometric()
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
                        openUnlocked()
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

    /**
     * After too many wrong entries, counts down to the next one: the time left
     * under the title, the dots going out with it, and the pad set back. The
     * time is the app lock's own, which refuses entries until then whatever is
     * shown. Returns whether it is counting.
     */
    private fun showLockoutIfNeeded(): Boolean {
        if (lockout?.isActive == true) return true
        if (appLockStore.lockoutRemainingMs() <= 0L) return false
        inputLocked = true
        pinBuffer = StringBuilder()
        updateDots()
        lockoutDots = requiredPinLength
        showStatus(Status.LOCKED_OUT)
        // Read out once, not every second.
        binding.textSubtitle.accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_NONE
        setEntryEnabled(false)
        lockout = lifecycleScope.launch {
            while (true) {
                val remainingMs = appLockStore.lockoutRemainingMs()
                if (remainingMs <= 0L) break
                VirtroidMotion.tickText(binding.textSubtitle, lockoutText(remainingMs))
                showLockoutDots(remainingMs)
                // To the next whole second left.
                delay((remainingMs - 1L) % 1_000L + 1L)
            }
            binding.textSubtitle.accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
            setEntryEnabled(true)
            updateDots()
            showStatus(Status.ENTER)
            VirtroidMotion.pulse(binding.iconLock)
            inputLocked = false
        }
        return true
    }

    private fun lockoutText(remainingMs: Long): String {
        val seconds = ((remainingMs + 999L) / 1_000L).coerceAtLeast(1L).toInt()
        return resources.getQuantityString(R.plurals.lock_try_again, seconds, seconds)
    }

    /** Red dots for the part of the lockout still to go. */
    private fun showLockoutDots(remainingMs: Long) {
        val length = AppLockStore.LOCKOUT_MS
        val left = ((remainingMs * requiredPinLength + length - 1) / length).toInt().coerceIn(0, requiredPinLength)
        pinDots.take(requiredPinLength).forEachIndexed { index, dot ->
            dot.setBackgroundResource(if (index < left) R.drawable.bg_pin_dot_error else R.drawable.bg_pin_dot_inactive)
            if (index in left until lockoutDots) VirtroidMotion.dip(dot)
        }
        lockoutDots = left
    }

    /** The digits, delete and the passphrase's button; a fingerprint still opens the app. */
    private fun setEntryEnabled(enabled: Boolean) {
        (pinButtons.map { it.first } + binding.pinDeleteButton).forEach { key ->
            // A key held down now would not hear the finger leave.
            if (!enabled) VirtroidMotion.cancelPress(key)
            key.isEnabled = enabled
            VirtroidMotion.fadeTo(key, if (enabled) 1f else PAD_WAITING_ALPHA)
        }
        binding.unlockButton.isEnabled = enabled
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

    private fun launchNext() {
        if (returnToPrevious) {
            finish()
            VirtroidMotion.closeWith(this, R.anim.v_zoom_enter, R.anim.v_zoom_exit)
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
        // Not CLEAR_TASK: Android covers the next screen with a blank starting
        // window for that. Finishing what is underneath leaves the same task.
        startActivity(destination, VirtroidMotion.zoomInto(this))
        finishAffinity()
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
        private const val PAD_WAITING_ALPHA = 0.35f
        // Until the ripple is done: building the next screen would freeze it part way.
        private const val OPEN_MS = 190L
        private const val BIOMETRIC_AUTHENTICATORS = BiometricManager.Authenticators.BIOMETRIC_STRONG
    }
}
