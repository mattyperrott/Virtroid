package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.RuntimeSummary
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenCreateSessionBinding
import io.virtroid.client.device.DeviceRuntimeProfile
import io.virtroid.client.security.enableSecureWindow
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

class NewRuntimeActivity : AppCompatActivity() {
    private lateinit var binding: ScreenCreateSessionBinding
    private val api = VirtroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore
    private var entitlement: EntitlementSummary? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenCreateSessionBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        appLogs = AppLogStore.get(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.newRuntimeRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 20 + bars.top,
                right = 24 + bars.right,
                bottom = 24 + bars.bottom,
            )
            insets
        }

        binding.newRuntimeCloseButton.setOnClickListener { finish() }
        binding.provisionRuntimeButton.setOnClickListener { provisionRuntime() }
        binding.provisionRuntimeButton.isEnabled = false
        binding.cameraPassthroughSwitch.isEnabled = false
        binding.switchCameraPassthroughLabel.text = getString(R.string.new_runtime_camera_passthrough_unavailable)
        loadEntitlement()
    }

    private fun loadEntitlement() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            renderEntitlement(null)
            return
        }

        lifecycleScope.launch {
            runCatching {
                api.getEntitlement(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess {
                entitlement = it
                renderEntitlement(it)
            }.onFailure {
                appLogs.warn("Runtime entitlement unavailable: ${it.message}", "runtime")
                renderEntitlement(null)
            }
        }
    }

    private fun renderEntitlement(entitlement: EntitlementSummary?) {
        binding.newRuntimeEntitlementText.text = if (entitlement == null) {
            getString(R.string.entitlement_unavailable)
        } else {
            getString(
                R.string.entitlement_detail,
                entitlement.runtimeCount,
                entitlement.runtimeLimit,
                entitlement.runtimeStartsRemainingToday,
            )
        }
        binding.provisionRuntimeButton.isEnabled = entitlement?.canCreateRuntime ?: false
    }

    private fun provisionRuntime() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            toast(getString(R.string.new_runtime_missing_access))
            return
        }
        entitlement?.createRuntimeBlockedMessage(this)?.let {
            toast(it)
            return
        }

        val baseUrl = sessionStore.baseUrl
        val runtimeProfile = DeviceRuntimeProfile.from(this)
        val runtimeName = binding.sessionNameInput.text?.toString().orEmpty().trim()
        binding.provisionRuntimeButton.isEnabled = false
        renderProvisionMilestone(provisionMilestoneForElapsed(0L, null))
        appLogs.info("Runtime creation requested", "runtime")

        lifecycleScope.launch {
            runCatching {
                val runtime = api.createRuntime(
                    baseUrl = baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    name = runtimeName,
                    runtimeProfile = runtimeProfile,
                    audioEnabled = binding.audioPassthroughSwitch.isChecked,
                    cameraMode = "disabled",
                    fileMode = "upload-only",
                    blobAutoSnapshot = true,
                    blobRetainDays = 7,
                )
                waitForRuntimeProvisioned(baseUrl, accountId, deviceId, runtime.id)
            }.onSuccess {
                renderProvisionMilestone(
                    ProvisionMilestone(
                        title = getString(R.string.new_runtime_provision_title_ready),
                        command = getString(R.string.new_runtime_provision_command_ready),
                        detail = getString(R.string.new_runtime_provision_detail_ready),
                        events = provisionEvents(Long.MAX_VALUE),
                    ),
                )
                appLogs.info("Runtime profile created", "runtime")
                toast(getString(R.string.runtime_created))
                delay(550L)
                setResult(RESULT_OK)
                finish()
            }.onFailure {
                appLogs.error(it.message ?: getString(R.string.status_error), "runtime")
                binding.provisionRuntimeButton.isEnabled = entitlement?.canCreateRuntime ?: false
                renderProvisionMilestone(
                    ProvisionMilestone(
                        title = getString(R.string.new_runtime_provision_title_error),
                        command = getString(R.string.new_runtime_provision_command_error),
                        detail = "... ${it.message ?: getString(R.string.status_error)}",
                        events = listOf(getString(R.string.runtime_provisioning_event_error)),
                    ),
                )
                toast(it.virtroidDisplayMessage(this@NewRuntimeActivity))
            }
        }
    }

    private suspend fun waitForRuntimeProvisioned(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
    ): RuntimeSummary {
        val startedAt = System.currentTimeMillis()
        repeat(PROVISION_WAIT_ATTEMPTS) { attempt ->
            val runtime = api.listRuntimes(baseUrl, accountId, deviceId)
                .firstOrNull { it.id == runtimeId }
                ?: throw java.io.IOException(getString(R.string.runtime_missing_for_session))
            val logs = runCatching {
                api.listRuntimeLogs(baseUrl, accountId, deviceId, runtimeId, limit = 1)
            }.getOrDefault(emptyList())
            val latestLog = logs.firstOrNull()?.message
            renderProvisionMilestone(provisionMilestoneForRuntime(runtime, System.currentTimeMillis() - startedAt, latestLog))
            if (runtime.status.equals("provisioned", ignoreCase = true) ||
                runtime.status.equals("stopped", ignoreCase = true)
            ) {
                return runtime
            }
            if (runtime.status.equals("error", ignoreCase = true)) {
                throw java.io.IOException(runtime.lastError ?: getString(R.string.status_error))
            }
            if (attempt < PROVISION_WAIT_ATTEMPTS - 1) {
                delay(PROVISION_WAIT_DELAY_MS)
            }
        }
        throw java.io.IOException(getString(R.string.runtime_start_timeout))
    }

    private fun provisionMilestoneForRuntime(runtime: RuntimeSummary, elapsedMs: Long, latestLog: String?): ProvisionMilestone {
        val detail = latestLog?.takeIf { it.isNotBlank() }?.let { "... $it" }
        return when {
            runtime.status.equals("provisioned", ignoreCase = true) ||
                runtime.status.equals("stopped", ignoreCase = true) -> ProvisionMilestone(
                title = getString(R.string.new_runtime_provision_title_ready),
                command = getString(R.string.new_runtime_provision_command_ready),
                detail = detail ?: getString(R.string.new_runtime_provision_detail_ready),
                events = provisionEvents(Long.MAX_VALUE),
            )
            elapsedMs < 2_000L -> provisionMilestoneForElapsed(elapsedMs, detail)
            elapsedMs < 5_000L -> ProvisionMilestone(
                title = getString(R.string.new_runtime_provision_title_image),
                command = getString(R.string.new_runtime_provision_command_image),
                detail = detail ?: getString(R.string.new_runtime_provision_detail_image),
                events = provisionEvents(elapsedMs),
            )
            elapsedMs < 8_000L -> ProvisionMilestone(
                title = getString(R.string.new_runtime_provision_title_profile),
                command = getString(R.string.new_runtime_provision_command_profile),
                detail = detail ?: getString(R.string.new_runtime_provision_detail_profile),
                events = provisionEvents(elapsedMs),
            )
            else -> ProvisionMilestone(
                title = getString(R.string.new_runtime_provision_title_container),
                command = getString(R.string.new_runtime_provision_command_container),
                detail = detail ?: getString(R.string.new_runtime_provision_detail_container),
                events = provisionEvents(elapsedMs),
            )
        }
    }

    private fun provisionMilestoneForElapsed(elapsedMs: Long, detail: String?): ProvisionMilestone {
        return ProvisionMilestone(
            title = getString(R.string.new_runtime_provision_title_request),
            command = getString(R.string.new_runtime_provision_command_request),
            detail = detail ?: getString(R.string.new_runtime_provision_detail_request),
            events = provisionEvents(elapsedMs),
        )
    }

    private fun provisionEvents(elapsedMs: Long): List<String> {
        val allEvents = listOf(
            getString(R.string.new_runtime_provision_event_identity),
            getString(R.string.new_runtime_provision_event_capacity),
            getString(R.string.new_runtime_provision_event_image),
            getString(R.string.new_runtime_provision_event_profile),
            getString(R.string.new_runtime_provision_event_container),
            getString(R.string.new_runtime_provision_event_ready),
        )
        val visibleCount = ((elapsedMs / 2_000L).toInt() + 2).coerceIn(2, allEvents.size)
        return allEvents.take(visibleCount).takeLast(4)
    }

    private fun renderProvisionMilestone(milestone: ProvisionMilestone) {
        binding.provisionRuntimeLogContainer.isVisible = true
        binding.provisionRuntimeTitleText.text = milestone.title
        binding.provisionRuntimeCommandText.text = milestone.command
        binding.provisionRuntimeDetailText.text = milestone.detail
        binding.provisionRuntimeEventTrailText.text = milestone.events.lastOrNull().orEmpty()
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        private const val PROVISION_WAIT_ATTEMPTS = 90
        private const val PROVISION_WAIT_DELAY_MS = 1_000L

        fun createIntent(context: Context): Intent = Intent(context, NewRuntimeActivity::class.java)
    }

    private data class ProvisionMilestone(
        val title: String,
        val command: String,
        val detail: String,
        val events: List<String>,
    )
}
