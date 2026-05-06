package io.virtdroid.client

import android.content.Intent
import android.os.Bundle
import android.view.View
import android.view.animation.AccelerateDecelerateInterpolator
import android.widget.ImageView
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtdroid.client.api.BootstrapResult
import io.virtdroid.client.api.VirtdroidApi
import io.virtdroid.client.data.SessionStore
import io.virtdroid.client.databinding.ScreenIdentityProvisioningBinding
import io.virtdroid.client.device.DeviceRuntimeProfile
import io.virtdroid.client.security.DeviceIdentityStore
import io.virtdroid.client.security.IdentityCrypto
import io.virtdroid.client.security.IdentityPasswordStore
import io.virtdroid.client.security.enableSecureWindow
import io.virtdroid.client.security.promptIdentityPasswordSetup
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.IOException
import java.util.UUID
import kotlin.random.Random

class OnboardingActivity : AppCompatActivity() {
    private lateinit var binding: ScreenIdentityProvisioningBinding
    private val api = VirtdroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private val deviceIdentityStore = DeviceIdentityStore()
    private var pendingIdentityPassword: String? = null
    private var pendingAccountId: String? = null
    private var pendingDeviceId: String? = null
    private var accountScrambleJob: Job? = null
    private var deviceScrambleJob: Job? = null
    private var identityPreviewJob: Job? = null
    private var activeDotPulseJob: Job? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenIdentityProvisioningBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
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

        binding.passphraseOptionCard.setOnClickListener {
            lifecycleScope.launch {
                collectIdentityPassword()
            }
        }
        binding.continueSetupButton.setOnClickListener {
            lifecycleScope.launch {
                if (!ensureIdentityPasswordReady()) {
                    return@launch
                }
                if (sessionStore.hasAccess()) {
                    completeSetup()
                } else {
                    createIdentity()
                }
            }
        }

        refreshIdentityState()
    }

    override fun onDestroy() {
        accountScrambleJob?.cancel()
        deviceScrambleJob?.cancel()
        identityPreviewJob?.cancel()
        activeDotPulseJob?.cancel()
        super.onDestroy()
    }

    private suspend fun createIdentity() {
        val password = pendingIdentityPassword ?: return
        binding.continueSetupButton.isEnabled = false
        val identity = ensurePreviewIdentity()
        showProvisioningLog()

        runCatching {
            updateActiveMilestone(
                title = "Generating Local Signing Key ...",
                command = "> android-keystore create EC signing key",
                detail = "... exporting public verification material_",
            )
            val publicKey = deviceIdentityStore.publicKeyMaterial()
            delay(STEP_VISUAL_DELAY_MS)

            updateActiveMilestone(
                title = "Registering Account ID ...",
                command = "> POST /api/v1/bootstrap",
                detail = "... requesting account and trusted-device registration_",
            )
            val result = api.bootstrap(
                baseUrl = sessionStore.baseUrl,
                accountId = identity.accountId,
                deviceId = identity.deviceId,
                deviceName = deviceIdentityStore.defaultDeviceName(),
                publicKey = publicKey,
                runtimeProfile = DeviceRuntimeProfile.from(this@OnboardingActivity),
            )
            sessionStore.saveBootstrap(result.accountId, result.deviceId)
            pendingAccountId = result.accountId
            pendingDeviceId = result.deviceId
            renderProvisionedAccount(result.accountId)
            markPreviousMilestone("Account ID created", shortDescriptor(result.accountId))
            delay(STEP_VISUAL_DELAY_MS)

            updateActiveMilestone(
                title = "Generating Device Fingerprint ...",
                command = "> bind device public key",
                detail = "... deriving hardware-backed device identity handle_",
            )
            renderProvisionedDevice(result.deviceId)
            markPreviousMilestone("Device fingerprint registered", shortDescriptor(result.deviceId))
            delay(STEP_VISUAL_DELAY_MS)

            registerBlobIdentity(result, password)
            markPreviousMilestone("Encrypted blob key sealed", "local password verifier registered")
            updateActiveMilestone(
                title = "Identity Ready",
                command = "> launch secure client",
                detail = "... provisioning completed_",
            )
            delay(FINAL_VISUAL_DELAY_MS)
        }.onSuccess {
            launchMain()
        }.onFailure {
            updateActiveMilestone(
                title = "Provisioning Failed",
                command = "> provisioning error",
                detail = "... ${it.message ?: getString(R.string.status_error)}",
            )
            toast(it.message ?: getString(R.string.status_error))
            binding.continueSetupButton.isEnabled = true
        }
    }

    private suspend fun completeSetup() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            toast(getString(R.string.new_runtime_missing_access))
            return
        }

        if (!identityPasswordStore.isConfigured(accountId, deviceId)) {
            val password = pendingIdentityPassword ?: return
            binding.continueSetupButton.isEnabled = false
            showProvisioningLog()
            runCatching {
                updateActiveMilestone(
                    title = "Generating Encrypted Blob Key ...",
                    command = "> derive blob_access_key",
                    detail = "... using local identity password_",
                )
                registerBlobIdentity(
                    BootstrapResult(accountId = accountId, deviceId = deviceId, runtimeId = ""),
                    password,
                )
                markPreviousMilestone("Encrypted blob key sealed", "local password verifier registered")
                updateActiveMilestone(
                    title = "Identity Ready",
                    command = "> launch secure client",
                    detail = "... provisioning completed_",
                )
                delay(FINAL_VISUAL_DELAY_MS)
            }.onFailure {
                updateActiveMilestone(
                    title = "Provisioning Failed",
                    command = "> identity register",
                    detail = "... ${it.message ?: getString(R.string.status_error)}",
                )
                toast(it.message ?: getString(R.string.status_error))
                binding.continueSetupButton.isEnabled = true
                return
            }
        }

        launchMain()
    }

    private suspend fun registerBlobIdentity(result: BootstrapResult, password: String) {
        updateActiveMilestone(
            title = "Generating Encrypted Blob Key ...",
            command = "> pbkdf2-hmac-sha256 account + device + password",
            detail = "... deriving runtime snapshot recovery material_",
        )
        val blobAccessKey = IdentityCrypto.deriveBlobAccessKey(result.accountId, result.deviceId, password)
        val blobKeyVerifier = IdentityCrypto.blobKeyVerifier(blobAccessKey)
        delay(STEP_VISUAL_DELAY_MS)

        updateActiveMilestone(
            title = "Registering Blob Verifier ...",
            command = "> POST /api/v1/me/identity/register",
            detail = "... sending verifier only; raw password stays local_",
        )
        api.registerIdentity(
            baseUrl = sessionStore.baseUrl,
            accountId = result.accountId,
            deviceId = result.deviceId,
            blobKeyVerifier = blobKeyVerifier,
        )
        identityPasswordStore.saveConfigured(result.accountId, result.deviceId)
        identityPasswordStore.unlock(result.accountId, result.deviceId, password)
        pendingIdentityPassword = password
        renderPasswordRequirement()
    }

    private suspend fun ensureIdentityPasswordReady(): Boolean {
        if (!pendingIdentityPassword.isNullOrBlank()) {
            return true
        }

        if (identityPasswordStore.isConfigured(sessionStore.accountId, sessionStore.deviceId)) {
            renderPasswordRequirement()
            return true
        }

        return collectIdentityPassword()
    }

    private suspend fun collectIdentityPassword(): Boolean {
        val password = promptIdentityPasswordSetup() ?: return false
        if (password.isBlank()) {
            toast(getString(R.string.identity_password_required))
            return false
        }
        pendingIdentityPassword = password
        renderPasswordRequirement()
        toast(getString(R.string.onboarding_password_set))
        return true
    }

    private fun refreshIdentityState() {
        if (sessionStore.hasAccess()) {
            pendingAccountId = sessionStore.accountId
            pendingDeviceId = sessionStore.deviceId
            renderProvisionedAccount(sessionStore.accountId)
            renderProvisionedDevice(sessionStore.deviceId)
        } else {
            renderProvisionedAccount(pendingAccountId)
            renderProvisionedDevice(pendingDeviceId)
            startIdentityPreviewSequence()
        }
        renderPasswordRequirement()
        binding.continueSetupButton.isEnabled = true
        binding.continueSetupButton.setText(
            if (sessionStore.hasAccess()) R.string.onboarding_continue else R.string.onboarding_create_identity,
        )
    }

    private fun startIdentityPreviewSequence() {
        if (identityPreviewJob?.isActive == true || sessionStore.hasAccess()) {
            return
        }

        identityPreviewJob = lifecycleScope.launch {
            if (pendingAccountId.isNullOrBlank()) {
                delay(PREVIEW_ACCOUNT_DELAY_MS)
                if (sessionStore.hasAccess()) {
                    return@launch
                }
                pendingAccountId = newAccountId()
                renderProvisionedAccount(pendingAccountId)
            }

            if (pendingDeviceId.isNullOrBlank()) {
                delay(PREVIEW_DEVICE_DELAY_MS)
                if (sessionStore.hasAccess()) {
                    return@launch
                }
                pendingDeviceId = deriveDeviceId(pendingAccountId.orEmpty())
                renderProvisionedDevice(pendingDeviceId)
            }
        }
    }

    private suspend fun ensurePreviewIdentity(): PendingIdentity {
        identityPreviewJob?.cancel()
        val accountId = pendingAccountId ?: newAccountId().also {
            pendingAccountId = it
            renderProvisionedAccount(it)
        }
        val deviceId = pendingDeviceId ?: deriveDeviceId(accountId).also {
            pendingDeviceId = it
            renderProvisionedDevice(it)
        }
        return PendingIdentity(accountId, deviceId)
    }

    private fun newAccountId(): String = UUID.randomUUID().toString()

    private suspend fun deriveDeviceId(accountId: String): String =
        withContext(Dispatchers.IO) {
            deviceIdentityStore.deviceFingerprint(this@OnboardingActivity, accountId)
        }

    private fun renderProvisionedAccount(accountId: String?) {
        if (accountId.isNullOrBlank()) {
            binding.onboardingAccountIdText.setTextColor(getColor(R.color.v_text_primary))
            setStatusIndicator(binding.accountStatusIndicator, binding.accountStatusCheck, provisioned = false)
            startScramble(binding.onboardingAccountIdText, isAccount = true)
            return
        }

        accountScrambleJob?.cancel()
        binding.onboardingAccountIdText.text = accountId
        binding.onboardingAccountIdText.setTextColor(getColor(R.color.v_accent))
        setStatusIndicator(binding.accountStatusIndicator, binding.accountStatusCheck, provisioned = true)
    }

    private fun renderProvisionedDevice(deviceId: String?) {
        if (deviceId.isNullOrBlank()) {
            binding.onboardingDeviceIdText.setTextColor(getColor(R.color.v_text_primary))
            setStatusIndicator(binding.deviceFingerprintStatusIndicator, binding.deviceFingerprintStatusCheck, provisioned = false)
            startScramble(binding.onboardingDeviceIdText, isAccount = false)
            return
        }

        deviceScrambleJob?.cancel()
        binding.onboardingDeviceIdText.text = deviceId
        binding.onboardingDeviceIdText.setTextColor(getColor(R.color.v_accent))
        setStatusIndicator(binding.deviceFingerprintStatusIndicator, binding.deviceFingerprintStatusCheck, provisioned = true)
    }

    private fun renderPasswordRequirement() {
        val configured = !pendingIdentityPassword.isNullOrBlank() ||
            identityPasswordStore.isConfigured(sessionStore.accountId, sessionStore.deviceId)
        binding.passphraseOptionCard.setBackgroundResource(
            if (configured) R.drawable.bg_selected_card_24 else R.drawable.bg_surface_stroke_24,
        )
        binding.passphraseOptionIndicator.setBackgroundResource(
            if (configured) R.drawable.bg_dot_accent else R.drawable.bg_dot_accent_outline,
        )
    }

    private fun setStatusIndicator(container: View, check: ImageView, provisioned: Boolean) {
        container.setBackgroundResource(
            if (provisioned) R.drawable.bg_status_pill_success else R.drawable.bg_status_pill_muted,
        )
        check.setColorFilter(getColor(if (provisioned) R.color.v_bg else R.color.v_text_dim))
    }

    private fun startScramble(target: TextView, isAccount: Boolean) {
        val existing = if (isAccount) accountScrambleJob else deviceScrambleJob
        if (existing?.isActive == true) {
            return
        }

        val job = lifecycleScope.launch {
            val stable = getString(R.string.onboarding_not_provisioned)
            while (isActive) {
                repeat(4) {
                    target.text = scrambled(stable)
                    delay(SCRAMBLE_FRAME_MS)
                }
                target.text = stable
                delay(SCRAMBLE_STABLE_MS)
            }
        }
        if (isAccount) {
            accountScrambleJob = job
        } else {
            deviceScrambleJob = job
        }
    }

    private fun scrambled(template: String): String {
        return template.map { char ->
            when {
                char.isWhitespace() -> char
                else -> SCRAMBLE_CHARS[Random.nextInt(SCRAMBLE_CHARS.length)]
            }
        }.joinToString("")
    }

    private fun showProvisioningLog() {
        if (!binding.provisioningLogContainer.isVisible) {
            binding.iconIdentityShield.animate()
                .alpha(0f)
                .translationY(-dp(18).toFloat())
                .setDuration(180L)
                .withEndAction { binding.iconIdentityShield.isVisible = false }
                .start()
            binding.identityScrollContent.animate()
                .translationY(-dp(2).toFloat())
                .setDuration(260L)
                .setInterpolator(AccelerateDecelerateInterpolator())
                .start()
            binding.provisioningLogContainer.alpha = 0f
            binding.provisioningLogContainer.translationY = 46f
            binding.provisioningLogContainer.isVisible = true
            binding.provisioningLogContainer.animate()
                .alpha(1f)
                .translationY(0f)
                .setDuration(260L)
                .setInterpolator(AccelerateDecelerateInterpolator())
                .start()
        }
        startActiveDotPulse()
    }

    private fun startActiveDotPulse() {
        if (activeDotPulseJob?.isActive == true) {
            return
        }
        activeDotPulseJob = lifecycleScope.launch {
            while (isActive) {
                binding.activeMilestoneDot.animate().alpha(0.35f).setDuration(420L).start()
                delay(420L)
                binding.activeMilestoneDot.animate().alpha(1f).setDuration(420L).start()
                delay(420L)
            }
        }
    }

    private fun markPreviousMilestone(title: String, descriptor: String) {
        binding.previousMilestoneTitleText.text = title
        binding.previousMilestoneSubtitleText.text = descriptor
        binding.previousMilestoneStatusText.text = getString(R.string.provisioning_done)
    }

    private fun updateActiveMilestone(title: String, command: String, detail: String) {
        binding.activeMilestoneTitleText.text = title
        binding.activeMilestoneCommandText.text = command
        binding.activeMilestoneDetailText.text = detail
    }

    private fun shortDescriptor(value: String): String {
        return if (value.length <= 18) value else "${value.take(10)}...${value.takeLast(6)}"
    }

    private fun dp(value: Int): Int {
        return (value * resources.displayMetrics.density).toInt()
    }

    private fun launchMain() {
        startActivity(
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
        )
        finish()
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    private data class PendingIdentity(
        val accountId: String,
        val deviceId: String,
    )

    private companion object {
        const val SCRAMBLE_CHARS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789#%+=?"
        const val SCRAMBLE_FRAME_MS = 70L
        const val SCRAMBLE_STABLE_MS = 320L
        const val PREVIEW_ACCOUNT_DELAY_MS = 2_000L
        const val PREVIEW_DEVICE_DELAY_MS = 1_500L
        const val STEP_VISUAL_DELAY_MS = 420L
        const val FINAL_VISUAL_DELAY_MS = 680L
    }
}
