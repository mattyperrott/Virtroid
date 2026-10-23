package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.api.AppCatalogEntry
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.RuntimeEgress
import io.virtroid.client.api.RuntimeIdlePeriods
import io.virtroid.client.api.RuntimeSummary
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenCreateSessionBinding
import io.virtroid.client.device.DeviceRuntimeProfile
import io.virtroid.client.security.IdentityKeyManager
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.RegionSeal
import io.virtroid.client.security.RuntimeRegion
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPassword
import io.virtroid.client.security.unlockAccountKeyWithBiometric
import java.io.IOException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import io.virtroid.client.ui.Notice
import io.virtroid.client.ui.VirtroidSheet

class NewRuntimeActivity : AppCompatActivity() {
    private lateinit var binding: ScreenCreateSessionBinding
    private val api = VirtroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore
    private var entitlement: EntitlementSummary? = null
    private var latestProvisionRuntimeLogs: List<String> = emptyList()
    private var provisionJob: Job? = null
    private var appPicks: List<AppCatalogEntry> = emptyList()
    private var idleTimeoutSeconds = RuntimeIdlePeriods.DEFAULT_SECONDS
    private var region: RuntimeRegion? = null
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private lateinit var identityKeyManager: IdentityKeyManager
    private val pickLocation = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode != RESULT_OK) return@registerForActivityResult
        when (val choice = LocationPickerActivity.choice(result.data)) {
            is LocationPickerActivity.Choice.Place -> region = choice.region
            LocationPickerActivity.Choice.Off -> region = null
            null -> return@registerForActivityResult
        }
        renderRegion()
    }
    private val pickApps = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == RESULT_OK) {
            appPicks = AppInstallActivity.decodePicks(result.data?.getStringExtra(AppInstallActivity.EXTRA_PICKS))
            renderAppPicks()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenCreateSessionBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        appLogs = AppLogStore.get(this)
        identityPasswordStore = IdentityPasswordStore(this)
        identityKeyManager = IdentityKeyManager(this, api)

        ViewCompat.setOnApplyWindowInsetsListener(binding.newRuntimeRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.newRuntimeCloseButton.setOnClickListener { finish() }
        binding.provisionRuntimeButton.setOnClickListener { provisionRuntime() }
        binding.provisionRuntimeButton.isEnabled = false
        binding.audioPassthroughSwitch.isChecked = true
        binding.cameraPassthroughSwitch.isEnabled = true
        binding.cameraPassthroughSwitch.isChecked = true
        binding.switchCameraPassthroughLabel.text = getString(R.string.new_runtime_camera_passthrough)
        binding.newRuntimeAppsRow.setOnClickListener {
            pickApps.launch(AppInstallActivity.pickIntent(this, appPicks))
        }
        appPicks = AppInstallActivity.decodePicks(savedInstanceState?.getString(STATE_APP_PICKS))
        renderAppPicks()
        idleTimeoutSeconds = savedInstanceState?.getInt(STATE_IDLE_TIMEOUT, idleTimeoutSeconds) ?: idleTimeoutSeconds
        renderIdle()
        binding.newRuntimeIdleRow.setOnClickListener {
            showIdlePeriodSheet(selectedSeconds = idleTimeoutSeconds) { seconds ->
                idleTimeoutSeconds = seconds
                renderIdle()
            }
        }
        region = savedInstanceState?.let(::restoreRegion)
        renderRegion()
        binding.newRuntimeLocationRow.setOnClickListener {
            pickLocation.launch(LocationPickerActivity.intent(this, hasRegion = region != null))
        }
        binding.optionStandardPersistent.setOnClickListener { showStorageInfo() }
        binding.newRuntimeStorageInfo.setOnClickListener { showStorageInfo() }
        loadEntitlement()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putString(STATE_APP_PICKS, AppInstallActivity.encodePicks(appPicks))
        outState.putInt(STATE_IDLE_TIMEOUT, idleTimeoutSeconds)
        region?.let {
            outState.putDouble(STATE_REGION_LATITUDE, it.latitude)
            outState.putDouble(STATE_REGION_LONGITUDE, it.longitude)
            outState.putString(STATE_REGION_TIMEZONE, it.timezone)
            outState.putString(STATE_REGION_LABEL, it.label)
        }
    }

    private fun renderIdle() {
        binding.newRuntimeIdleSubtitle.text = getString(R.string.new_runtime_idle_summary, idlePeriodLabel(idleTimeoutSeconds))
        binding.newRuntimeIdleRow.contentDescription = getString(
            R.string.accessibility_setting_value,
            binding.newRuntimeIdleTitle.text,
            binding.newRuntimeIdleSubtitle.text,
        )
    }

    private fun restoreRegion(state: Bundle): RuntimeRegion? {
        val timezone = state.getString(STATE_REGION_TIMEZONE) ?: return null
        return RuntimeRegion(
            latitude = state.getDouble(STATE_REGION_LATITUDE),
            longitude = state.getDouble(STATE_REGION_LONGITUDE),
            timezone = timezone,
            label = state.getString(STATE_REGION_LABEL).orEmpty(),
        )
    }

    private fun renderRegion() {
        binding.newRuntimeLocationSubtitle.text = region?.label ?: getString(R.string.location_none)
        binding.newRuntimeLocationRow.contentDescription = getString(
            R.string.accessibility_setting_value,
            binding.newRuntimeLocationTitle.text,
            binding.newRuntimeLocationSubtitle.text,
        )
    }

    /** What happens to the runtime's data, behind the storage card's one line. */
    private fun showStorageInfo() {
        VirtroidSheet(this)
            .title(getString(R.string.new_runtime_persistent_title))
            .row(R.drawable.ic_lock, getString(R.string.storage_info_encrypted_title), getString(R.string.storage_info_encrypted_body))
            .row(R.drawable.ic_hexagon, getString(R.string.storage_info_shards_title), getString(R.string.storage_info_shards_body))
            .row(R.drawable.ic_refresh, getString(R.string.storage_info_kept_title), getString(R.string.storage_info_kept_body))
            .row(R.drawable.ic_delete, getString(R.string.storage_info_removed_title), getString(R.string.storage_info_removed_body))
            .note(getString(R.string.storage_info_note))
            .primary(getString(R.string.storage_info_close)) {}
            .show()
    }

    private fun renderAppPicks() {
        binding.newRuntimeAppsSubtitle.text = if (appPicks.isEmpty()) {
            getString(R.string.new_runtime_apps_none)
        } else {
            getString(R.string.new_runtime_apps_selected, appPicks.size, appPicks.joinToString(", ") { it.displayName })
        }
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
                        appPackages = appPicks.map { it.packageName },
                        idleTimeoutSeconds = idleTimeoutSeconds,
                        egressMode = if (binding.torEgressSwitch.isChecked) RuntimeEgress.TOR else RuntimeEgress.DIRECT,
                    )
                    region?.let { saveRegion(baseUrl, accountId, deviceId, runtime.id, it) }
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
                            detail = latestProvisionRuntimeLogs.lastOrNull()
                                ?: getString(R.string.new_runtime_provision_detail_ready),
                            events = runtimeProgressEvents(
                                latestProvisionRuntimeLogs,
                                getString(R.string.new_runtime_provision_event_ready),
                            ),
                        ),
                    )
                    appLogs.info("Runtime profile created", "runtime")
                    delay(550L)
                    setResult(RESULT_OK)
                    finish()
                }.onFailure {
                    // The screen closed while it waited: nothing failed.
                    if (it is CancellationException) return@launch
                    appLogs.error(it.message ?: getString(R.string.status_error), "runtime")
                    binding.provisionRuntimeButton.isEnabled = entitlement?.canCreateRuntime ?: false
                    renderProvisionMilestone(
                        ProvisionMilestone(
                            title = getString(R.string.new_runtime_provision_title_error),
                            command = getString(R.string.new_runtime_provision_command_error),
                            detail = it.message ?: getString(R.string.status_error),
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

    /**
     * Seals the chosen region for the new runtime's VM and saves it. The
     * runtime exists by now, so a failure here is reported but not fatal.
     */
    private suspend fun saveRegion(baseUrl: String, accountId: String, deviceId: String, runtimeId: String, region: RuntimeRegion) {
        runCatching {
            val viewerKey = RegionSeal.viewerKey(requireBlobAccessKey(accountId, deviceId), runtimeId)
            val sealed = try {
                RegionSeal.seal(viewerKey, runtimeId, region)
            } finally {
                viewerKey.fill(0)
            }
            api.updateRuntimeRegion(baseUrl, accountId, deviceId, runtimeId, sealed)
        }.onFailure {
            appLogs.warn("Runtime location not saved: ${it.message}", "runtime")
            toast(getString(R.string.new_runtime_region_failed, it.virtroidDisplayMessage(this)))
        }
    }

    private suspend fun requireBlobAccessKey(accountId: String, deviceId: String): String {
        identityPasswordStore.unlockedBlobAccessKey(accountId, deviceId)?.let { return it }
        unlockAccountKeyWithBiometric(accountId, deviceId, identityPasswordStore)?.let { return it }
        val password = promptIdentityPassword(
            title = getString(R.string.identity_unlock_title),
            hint = getString(R.string.identity_password_prompt),
        )?.trim().orEmpty()
        if (password.isBlank()) {
            throw IOException(getString(R.string.identity_password_required))
        }
        return identityKeyManager.unlockOrMigrate(sessionStore.baseUrl, accountId, deviceId, password)
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
        val detail = logs.lastOrNull()
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
        Notice.show(this, message)
    }

    companion object {
        private const val PROVISION_WAIT_MAX_MS = 5 * 60 * 1_000L
        private const val STATE_APP_PICKS = "app_picks"
        private const val STATE_IDLE_TIMEOUT = "idle_timeout_seconds"
        private const val STATE_REGION_LATITUDE = "region_latitude"
        private const val STATE_REGION_LONGITUDE = "region_longitude"
        private const val STATE_REGION_TIMEZONE = "region_timezone"
        private const val STATE_REGION_LABEL = "region_label"

        fun createIntent(context: Context): Intent = Intent(context, NewRuntimeActivity::class.java)
    }

    private data class ProvisionMilestone(
        val title: String,
        val command: String,
        val detail: String,
        val events: List<String>,
    )
}
