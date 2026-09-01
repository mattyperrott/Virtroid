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
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

class NewRuntimeActivity : AppCompatActivity() {
    private lateinit var binding: ScreenCreateSessionBinding
    private val api = VirtroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore
    private var entitlement: EntitlementSummary? = null
    private var latestProvisionRuntimeLogs: List<String> = emptyList()
    private var provisionJob: Job? = null

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
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.newRuntimeCloseButton.setOnClickListener { finish() }
        binding.provisionRuntimeButton.setOnClickListener { provisionRuntime() }
        binding.provisionRuntimeButton.isEnabled = false
        binding.cameraPassthroughSwitch.isEnabled = true
        binding.cameraPassthroughSwitch.isChecked = true
        binding.switchCameraPassthroughLabel.text = getString(R.string.new_runtime_camera_passthrough)
        loadEntitlement()
    }

    override fun onDestroy() {
        provisionJob?.cancel()
        super.onDestroy()
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
        binding.provisionRuntimeButton.isEnabled = entitlement?.canCreateRuntime ?: false
    }

    private fun provisionRuntime() {
        if (provisionJob?.isActive == true) {
            return
        }
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
        latestProvisionRuntimeLogs = emptyList()
        renderProvisionMilestone(provisionMilestoneForRequest())
        appLogs.info("Runtime creation requested", "runtime")

        provisionJob = lifecycleScope.launch {
            try {
                runCatching {
                    val runtime = api.createRuntime(
                        baseUrl = baseUrl,
                        accountId = accountId,
                        deviceId = deviceId,
                        name = runtimeName,
                        runtimeProfile = runtimeProfile,
                        audioEnabled = binding.audioPassthroughSwitch.isChecked,
						cameraMode = if (binding.cameraPassthroughSwitch.isChecked) "photo-import" else "disabled",
                        fileMode = "upload-only",
                        blobAutoSnapshot = true,
                        blobRetainDays = 7,
                    )
                    waitForRuntimeProvisioned(baseUrl, accountId, deviceId, runtime.id)
                }.onSuccess {
                    renderProvisionMilestone(
                        ProvisionMilestone(
                            title = getString(R.string.new_runtime_provision_title_ready),
                            command = getString(
                                R.string.runtime_progress_command_state,
                                it.status.ifBlank { "stopped" },
                                it.connectionStatus.ifBlank { "offline" },
                            ),
                            detail = latestProvisionRuntimeLogs.lastOrNull()?.let { message -> "... $message" }
                                ?: getString(R.string.new_runtime_provision_detail_ready),
                            events = runtimeProgressEvents(
                                latestProvisionRuntimeLogs,
                                getString(R.string.new_runtime_provision_event_ready),
                            ),
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
            } finally {
                provisionJob = null
            }
        }
    }

    private suspend fun waitForRuntimeProvisioned(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
    ): RuntimeSummary {
        val startedAtMs = System.currentTimeMillis()
        while (true) {
            val runtime = api.listRuntimes(baseUrl, accountId, deviceId)
                .firstOrNull { it.id == runtimeId }
                ?: throw java.io.IOException(getString(R.string.runtime_missing_for_session))
            val logs = runCatching {
                api.listRuntimeLogs(baseUrl, accountId, deviceId, runtimeId, limit = 4)
            }.getOrDefault(emptyList())
                .asReversed()
                .mapNotNull { sanitizeRuntimeProgressMessage(it.message) }
            latestProvisionRuntimeLogs = logs
            renderProvisionMilestone(provisionMilestoneForRuntime(runtime, logs))
            if (runtime.status.equals("provisioned", ignoreCase = true) ||
                runtime.status.equals("stopped", ignoreCase = true)
            ) {
                return runtime
            }
            if (runtime.status.equals("error", ignoreCase = true)) {
                throw java.io.IOException(runtime.lastError ?: getString(R.string.status_error))
            }
            if (System.currentTimeMillis() - startedAtMs >= PROVISION_WAIT_MAX_MS) {
                throw java.io.IOException(getString(R.string.runtime_start_timeout))
            }
            delay(provisionWaitDelayMs(startedAtMs))
        }
    }

    private fun provisionWaitDelayMs(startedAtMs: Long): Long {
        val elapsedMs = System.currentTimeMillis() - startedAtMs
        return when {
            elapsedMs < 30_000L -> 1_000L
            elapsedMs < 120_000L -> 2_000L
            else -> 5_000L
        }
    }

    private fun provisionMilestoneForRuntime(runtime: RuntimeSummary, logs: List<String>): ProvisionMilestone {
        val detail = logs.lastOrNull()?.let { "... $it" }
        val command = getString(
            R.string.runtime_progress_command_state,
            runtime.status.ifBlank { "unknown" },
            runtime.connectionStatus.ifBlank { "offline" },
        )
        return when {
            runtime.status.equals("provisioned", ignoreCase = true) ||
                runtime.status.equals("stopped", ignoreCase = true) -> ProvisionMilestone(
                title = getString(R.string.new_runtime_provision_title_ready),
                command = command,
                detail = detail ?: getString(R.string.new_runtime_provision_detail_ready),
                events = runtimeProgressEvents(logs, getString(R.string.new_runtime_provision_event_ready)),
            )
            else -> ProvisionMilestone(
                title = getString(R.string.new_runtime_provision_title_container),
                command = command,
                detail = detail ?: getString(R.string.runtime_progress_detail_waiting),
                events = runtimeProgressEvents(logs, getString(R.string.runtime_progress_event_waiting)),
            )
        }
    }

    private fun provisionMilestoneForRequest(): ProvisionMilestone {
        return ProvisionMilestone(
            title = getString(R.string.new_runtime_provision_title_request),
            command = getString(R.string.new_runtime_provision_command_request),
            detail = getString(R.string.new_runtime_provision_detail_request),
            events = listOf(getString(R.string.runtime_progress_event_submitting)),
        )
    }

    private fun runtimeProgressEvents(messages: List<String>, fallback: String): List<String> {
        val events = messages
            .mapNotNull(::sanitizeRuntimeProgressMessage)
            .takeLast(3)
            .map { getString(R.string.runtime_provisioning_event_runtime_log, it) }
        return events.ifEmpty { listOf(fallback) }
    }

    private fun sanitizeRuntimeProgressMessage(message: String): String? {
        var sanitized = message.trim()
        if (sanitized.isBlank()) {
            return null
        }
        sanitized = sanitized.replace(Regex(""" on host [A-Za-z0-9_.-]+"""), "")
        sanitized = sanitized.replace(
            Regex("""Runtime container [A-Za-z0-9_.-]+ started on port \d+ with persona"""),
            "Runtime container started with persona",
        )
        sanitized = sanitized.replace(
            Regex("""Encrypted viewer proxy prepared on guest port \d+\."""),
            "Encrypted viewer proxy prepared.",
        )
        return sanitized
    }

    private fun renderProvisionMilestone(milestone: ProvisionMilestone) {
        binding.provisionRuntimeLogContainer.isVisible = true
        binding.provisionRuntimeTitleText.text = milestone.title
        binding.provisionRuntimeCommandText.text = milestone.command
        binding.provisionRuntimeDetailText.text = milestone.detail
        binding.provisionRuntimeEventTrailText.text = milestone.events.joinToString("\n")
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        private const val PROVISION_WAIT_MAX_MS = 5 * 60 * 1_000L

        fun createIntent(context: Context): Intent = Intent(context, NewRuntimeActivity::class.java)
    }

    private data class ProvisionMilestone(
        val title: String,
        val command: String,
        val detail: String,
        val events: List<String>,
    )
}
