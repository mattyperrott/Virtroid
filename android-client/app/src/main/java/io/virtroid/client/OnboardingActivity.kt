package io.virtroid.client

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
import io.virtroid.client.api.BootstrapResult
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenIdentityProvisioningBinding
import io.virtroid.client.device.DeviceRuntimeProfile
import io.virtroid.client.security.DeviceIdentityStore
import io.virtroid.client.security.IdentityCrypto
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPasswordSetup
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
    private val api = VirtroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private val deviceIdentityStore = DeviceIdentityStore()
    private var pendingIdentityPassword: String? = null
    private var pendingAccountId: String? = null
    private var pendingDeviceId: String? = null
    private var accountScrambleJob: Job? = null
    private var deviceScrambleJob: Job? = null
    private var identityPreviewJob: Job? = null
    private val provisioningEvents = mutableListOf<String>()

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
                if (!sessionStore.hasAccess() && !ensureBootstrapInviteReady()) {
                    return@launch
                }
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
        super.onDestroy()
    }

    private suspend fun createIdentity() {
        val password = pendingIdentityPassword ?: return
        val inviteToken = binding.bootstrapInviteInput.text?.toString()?.trim().orEmpty()
        if (inviteToken.isBlank()) {
            ensureBootstrapInviteReady()
            return
        }
        binding.continueSetupButton.isEnabled = false
        val identity = ensurePreviewIdentity()
        showProvisioningLog()

        runCatching {
            updateActiveMilestone(
                title = "Preparing Local Signing Key ...",
                command = "> android-keystore load signing key",
                detail = "... loading public verification material_",
            )
            val publicKey = deviceIdentityStore.publicKeyMaterial()
            markPreviousMilestone("Local signing key ready", deviceIdentityStore.defaultDeviceName(this@OnboardingActivity))

            updateActiveMilestone(
                title = "Registering Account And Device ...",
                command = "> POST /api/v1/bootstrap",
                detail = "... waiting for control plane registration_",
            )
            val result = api.bootstrap(
                baseUrl = sessionStore.baseUrl,
                accountId = identity.accountId,
                deviceId = identity.deviceId,
                deviceName = deviceIdentityStore.defaultDeviceName(this@OnboardingActivity),
                publicKey = publicKey,
                inviteToken = inviteToken,
                runtimeProfile = DeviceRuntimeProfile.from(this@OnboardingActivity),
            )
            sessionStore.saveBootstrap(result.accountId, result.deviceId)
            binding.bootstrapInviteInput.text?.clear()
            pendingAccountId = result.accountId
            pendingDeviceId = result.deviceId
            renderProvisionedAccount(result.accountId, registered = true)
            markPreviousMilestone("Account registered", shortDescriptor(result.accountId))

            updateActiveMilestone(
                title = "Binding Device Identity ...",
                command = "> bind device public key",
                detail = "... confirming signed device registration_",
            )
            renderProvisionedDevice(result.deviceId, registered = true)
            markPreviousMilestone("Device ID registered", shortDescriptor(result.deviceId))

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
            val message = it.virtroidDisplayMessage(this@OnboardingActivity)
            updateActiveMilestone(
                title = "Provisioning Failed",
                command = "> provisioning error",
                detail = "... $message",
            )
            toast(message)
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
                    title = "Preparing Identity Verifier ...",
                    command = "> derive identity verifier",
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
        markPreviousMilestone("Blob key verifier derived", "raw password remains local")

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

    private fun ensureBootstrapInviteReady(): Boolean {
        val ready = binding.bootstrapInviteInput.text?.toString()?.trim()?.isNotEmpty() == true
        binding.bootstrapInviteInputLayout.error =
            if (ready) null else getString(R.string.onboarding_invite_required)
        if (!ready) {
            binding.bootstrapInviteInput.requestFocus()
        }
        return ready
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
        binding.bootstrapInviteDescription.isVisible = !sessionStore.hasAccess()
        binding.bootstrapInviteInputLayout.isVisible = !sessionStore.hasAccess()
        if (sessionStore.hasAccess()) {
            pendingAccountId = sessionStore.accountId
            pendingDeviceId = sessionStore.deviceId
            renderProvisionedAccount(sessionStore.accountId, registered = true)
            renderProvisionedDevice(sessionStore.deviceId, registered = true)
        } else {
            renderProvisionedAccount(pendingAccountId, registered = false)
            renderProvisionedDevice(pendingDeviceId, registered = false)
            startIdentityPreviewSequence()
        }
        renderPasswordRequirement()
        binding.continueSetupButton.isEnabled = true
        binding.continueSetupButton.setText(
            if (sessionStore.hasAccess()) R.string.onboarding_continue else R.string.onboarding_create_identity,
        )
    }

    private suspend fun ensurePreviewIdentity(): PendingIdentity {
        identityPreviewJob?.cancel()
        val accountId = pendingAccountId ?: newAccountId().also {
            pendingAccountId = it
            renderProvisionedAccount(it, registered = false)
        }
        val deviceId = pendingDeviceId ?: deriveDeviceId(accountId).also {
            pendingDeviceId = it
            renderProvisionedDevice(it, registered = false)
        }
        return PendingIdentity(accountId, deviceId)
    }

    private fun newAccountId(): String = UUID.randomUUID().toString()

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
                renderProvisionedAccount(pendingAccountId, registered = false)
            }

            if (pendingDeviceId.isNullOrBlank()) {
                delay(PREVIEW_DEVICE_DELAY_MS)
                if (sessionStore.hasAccess()) {
                    return@launch
                }
                pendingDeviceId = deriveDeviceId(pendingAccountId.orEmpty())
                renderProvisionedDevice(pendingDeviceId, registered = false)
            }
        }
    }

    private suspend fun deriveDeviceId(accountId: String): String =
        withContext(Dispatchers.IO) {
            deviceIdentityStore.deviceFingerprint(this@OnboardingActivity, accountId)
        }

    private fun renderProvisionedAccount(accountId: String?, registered: Boolean) {
        if (accountId.isNullOrBlank()) {
            binding.onboardingAccountIdText.setTextColor(getColor(R.color.v_text_primary))
            setStatusIndicator(binding.accountStatusIndicator, binding.accountStatusCheck, provisioned = false)
            startScramble(binding.onboardingAccountIdText, isAccount = true)
            return
        }

        accountScrambleJob?.cancel()
        binding.onboardingAccountIdText.text = accountId
        binding.onboardingAccountIdText.setTextColor(getColor(if (registered) R.color.v_accent else R.color.v_text_primary))
        setStatusIndicator(binding.accountStatusIndicator, binding.accountStatusCheck, provisioned = true)
    }

    private fun renderProvisionedDevice(deviceId: String?, registered: Boolean) {
        if (deviceId.isNullOrBlank()) {
            binding.onboardingDeviceIdText.setTextColor(getColor(R.color.v_text_primary))
            setStatusIndicator(binding.deviceFingerprintStatusIndicator, binding.deviceFingerprintStatusCheck, provisioned = false)
            startScramble(binding.onboardingDeviceIdText, isAccount = false)
            return
        }

        deviceScrambleJob?.cancel()
        binding.onboardingDeviceIdText.text = deviceId
        binding.onboardingDeviceIdText.setTextColor(getColor(if (registered) R.color.v_accent else R.color.v_text_primary))
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
            while (isActive) {
                target.text = randomPlaceholder()
                delay(SCRAMBLE_FRAME_MS)
            }
        }
        if (isAccount) {
            accountScrambleJob = job
        } else {
            deviceScrambleJob = job
        }
    }

    private fun randomPlaceholder(): String {
        return UUID_PATTERN.joinToString("-") { length ->
            buildString {
                repeat(length) {
                    append(SCRAMBLE_CHARS[Random.nextInt(SCRAMBLE_CHARS.length)])
                }
            }
        }
    }

    private fun showProvisioningLog() {
        provisioningEvents.clear()
        binding.activeMilestoneEventTrailText.text = getString(R.string.runtime_progress_event_submitting)
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
    }

    private fun markPreviousMilestone(title: String, descriptor: String) {
        provisioningEvents += "[done] $title - $descriptor"
        binding.activeMilestoneEventTrailText.text = provisioningEvents.takeLast(3).joinToString("\n")
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
        const val SCRAMBLE_CHARS = "0123456789abcdef"
        val UUID_PATTERN = intArrayOf(8, 4, 4, 4, 12)
        const val SCRAMBLE_FRAME_MS = 90L
        const val PREVIEW_ACCOUNT_DELAY_MS = 2_000L
        const val PREVIEW_DEVICE_DELAY_MS = 1_500L
        const val FINAL_VISUAL_DELAY_MS = 680L
    }
}
