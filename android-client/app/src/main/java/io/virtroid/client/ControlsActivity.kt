package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.text.InputType
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.RuntimeLogEntry
import io.virtroid.client.api.RuntimeState
import io.virtroid.client.api.RuntimeSummary
import io.virtroid.client.api.RuntimeUpdate
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenSessionControlsBinding
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPassword
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.io.IOException
import kotlin.math.max

class ControlsActivity : AppCompatActivity() {
    private lateinit var binding: ScreenSessionControlsBinding
    private val api = VirtroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var activeSessionStore: ActiveSessionStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private lateinit var appLogs: AppLogStore
    private var runtimeId: String = ""
    private var runtime: RuntimeSummary? = null
    private var runtimeState: RuntimeState? = null
    private var entitlement: EntitlementSummary? = null
    private var connectOrStartJob: Job? = null
    private var logsExpanded = false
    private val displayBodyText by lazy { findViewById<android.widget.TextView>(R.id.controlsDisplayBodyText) }
    private val buildBodyText by lazy { findViewById<android.widget.TextView>(R.id.controlsBuildBodyText) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenSessionControlsBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        activeSessionStore = ActiveSessionStore(this)
        identityPasswordStore = IdentityPasswordStore(this)
        appLogs = AppLogStore.get(this)
        runtimeId = intent.getStringExtra(EXTRA_RUNTIME_ID).orEmpty()
        if (runtimeId.isBlank()) {
            finish()
            return
        }

        ViewCompat.setOnApplyWindowInsetsListener(binding.controlsRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 20 + bars.top,
                right = 24 + bars.right,
                bottom = 24 + bars.bottom,
            )
            insets
        }

        binding.controlsBackButton.setOnClickListener { finish() }
        binding.controlsConnectButton.setOnClickListener { connectOrStart() }
        binding.displayOutputRow.setOnClickListener { showDisplayDialog() }
        binding.consoleLogsRow.setOnClickListener { toggleLogs() }
        binding.wipeRow.setOnClickListener { confirmWipe() }
        binding.destroyRow.setOnClickListener { confirmDelete() }

        loadRuntime()
    }

    override fun onResume() {
        super.onResume()
        loadRuntime()
    }

    override fun onDestroy() {
        connectOrStartJob?.cancel()
        super.onDestroy()
    }

    private fun loadRuntime() {
        val accountId = sessionStore.accountId ?: return
        val deviceId = sessionStore.deviceId ?: return
        lifecycleScope.launch {
            runCatching {
                val entitlement = api.getEntitlement(sessionStore.baseUrl, accountId, deviceId)
                val state = api.getRuntimeState(sessionStore.baseUrl, accountId, deviceId, runtimeId)
                ControlsState(state.runtime, entitlement, state)
            }.onSuccess { loaded ->
                runtime = loaded.runtime
                runtimeState = loaded.runtimeState
                entitlement = loaded.entitlement
                bindRuntime(loaded.runtime, loaded.runtimeState)
            }.onFailure {
                toast(it.virtroidDisplayMessage(this@ControlsActivity))
                finish()
            }
        }
    }

    private fun bindRuntime(runtime: RuntimeSummary, state: RuntimeState? = runtimeState) {
        binding.controlsRuntimeNameText.text = runtime.name
        binding.controlsRuntimeSubtitleText.text = getString(R.string.controls_runtime_subtitle, runtime.id)
        binding.controlsStateValue.text = runtime.lifecycleLabel()
        binding.controlsTunnelValue.text = getString(R.string.controls_tunnel_encrypted)
        binding.controlsStorageValue.text = when {
            runtime.blobLastSnapshotAt != null -> getString(R.string.controls_loaded)
            runtime.blobAutoSnapshot -> getString(R.string.controls_storage_persistent)
            else -> getString(R.string.controls_storage_volatile)
        }
        binding.cameraPassthroughRow.isVisible = !runtime.cameraMode.equals("disabled", ignoreCase = true)
        buildBodyText.text = getString(
            R.string.controls_build_info_body,
            runtime.personaModel ?: runtime.name,
            runtime.personaRelease ?: formatAndroidVersion(runtime.androidVersion),
        )
        displayBodyText.text = getString(
            R.string.controls_display_body,
            runtime.widthPx,
            runtime.heightPx,
            runtime.densityDpi,
        )
        val lifecycleBusy = state?.isBusy ?: runtime.isLifecycleBusy()
        val canConnectOrStart = state?.let {
            it.canConnect || it.canStart || it.effectiveState.equals("starting", ignoreCase = true)
        } ?: (!lifecycleBusy || runtime.isReadyForSession())
        binding.controlsConnectButton.isEnabled = canConnectOrStart && connectOrStartJob?.isActive != true
        binding.wipeRow.isEnabled = state?.canWipe ?: !lifecycleBusy
        binding.destroyRow.isEnabled = state?.canDelete ?: !lifecycleBusy
        binding.displayOutputRow.isEnabled = !lifecycleBusy
    }

    private fun showDisplayDialog() {
        val current = runtime ?: return
        val container = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(32, 24, 32, 0)
        }
        val widthInput = EditText(this).apply {
            hint = "Width"
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(current.widthPx.toString())
            setTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface))
            setHintTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface_muted))
        }
        val heightInput = EditText(this).apply {
            hint = "Height"
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(current.heightPx.toString())
            setTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface))
            setHintTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface_muted))
        }
        val dpiInput = EditText(this).apply {
            hint = "DPI"
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(current.densityDpi.toString())
            setTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface))
            setHintTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface_muted))
        }
        container.addView(widthInput)
        container.addView(heightInput)
        container.addView(dpiInput)

        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.controls_display_dialog_title))
            .setView(container)
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.controls_confirm)) { _, _ ->
                val width = widthInput.text.toString().toIntOrNull() ?: current.widthPx
                val height = heightInput.text.toString().toIntOrNull() ?: current.heightPx
                val dpi = dpiInput.text.toString().toIntOrNull() ?: current.densityDpi
                updateRuntime(
                    current.copy(),
                    widthPx = width,
                    heightPx = height,
                    densityDpi = dpi,
                )
            }
            .show()
    }

    private fun updateRuntime(
        current: RuntimeSummary,
        widthPx: Int = current.widthPx,
        heightPx: Int = current.heightPx,
        densityDpi: Int = current.densityDpi,
    ) {
        val accountId = sessionStore.accountId ?: return
        val deviceId = sessionStore.deviceId ?: return
        lifecycleScope.launch {
            runCatching {
                api.updateRuntime(
                    baseUrl = sessionStore.baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    runtimeId = current.id,
                    update = RuntimeUpdate(
                        name = current.name,
                        androidImage = current.androidImage,
                        androidVersion = current.androidVersion,
                        widthPx = widthPx,
                        heightPx = heightPx,
                        densityDpi = densityDpi,
                        audioEnabled = current.audioEnabled,
                        cameraMode = current.cameraMode,
                        fileMode = current.fileMode,
                        blobAutoSnapshot = current.blobAutoSnapshot,
                        blobRetainDays = current.blobRetainDays,
                    ),
                )
            }.onSuccess {
                runtime = it
                runtimeState = null
                bindRuntime(it)
                toast(getString(R.string.runtime_saved))
            }.onFailure {
                toast(it.virtroidDisplayMessage(this@ControlsActivity))
                loadRuntime()
            }
        }
    }

    private fun toggleLogs() {
        if (logsExpanded) {
            logsExpanded = false
            binding.controlsLogsText.isVisible = false
            return
        }

        val accountId = sessionStore.accountId ?: return
        val deviceId = sessionStore.deviceId ?: return
        lifecycleScope.launch {
            runCatching {
                api.listRuntimeLogs(sessionStore.baseUrl, accountId, deviceId, runtimeId, limit = 20)
            }.onSuccess {
                logsExpanded = true
                binding.controlsLogsText.isVisible = true
                binding.controlsLogsText.text = renderLogs(it)
            }.onFailure {
                toast(it.virtroidDisplayMessage(this@ControlsActivity))
            }
        }
    }

    private fun confirmWipe() {
        val current = runtime ?: return
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.controls_wipe_confirm_title))
            .setMessage(getString(R.string.controls_wipe_confirm_body))
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.controls_confirm)) { _, _ ->
                val accountId = sessionStore.accountId ?: return@setPositiveButton
                lifecycleScope.launch {
                    runCatching {
                        val deviceId = sessionStore.deviceId ?: throw IOException(getString(R.string.device_missing))
                        val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                        val updated = api.wipeRuntime(sessionStore.baseUrl, accountId, deviceId, current.id, blobAccessKey)
                        identityPasswordStore.saveConfigured(accountId, deviceId)
                        updated
                    }.onSuccess {
                        activeSessionStore.loadForRuntime(current.id)?.let {
                            activeSessionStore.clear()
                        }
                        toast(getString(R.string.status_wiping_runtime))
                        finishToRuntimeList()
                    }.onFailure {
                        toast(it.virtroidDisplayMessage(this@ControlsActivity))
                    }
                }
            }
            .show()
    }

    private fun renderLogs(logs: List<RuntimeLogEntry>): String {
        return logs.joinToString("\n") { "[${it.createdAt}] ${it.level}/${it.source}: ${it.message}" }
            .ifBlank { getString(R.string.runtime_logs_empty) }
    }

    private fun connectOrStart() {
        val current = runtime ?: return
        if (connectOrStartJob?.isActive == true) {
            return
        }
        binding.controlsConnectButton.isEnabled = false
        connectOrStartJob = lifecycleScope.launch {
            var handedOffToViewerPreparation = false
            try {
                runCatching {
                    val accountId = sessionStore.accountId ?: throw IOException(getString(R.string.account_missing))
                    val deviceId = sessionStore.deviceId ?: throw IOException(getString(R.string.device_missing))
                    val state = api.getRuntimeState(sessionStore.baseUrl, accountId, deviceId, current.id)
                    runtime = state.runtime
                    runtimeState = state
                    bindRuntime(state.runtime, state)
                    if (state.canConnectRuntime(current.id)) {
                        return@runCatching state.runtime
                    }
                    if (state.effectiveState.equals("starting", ignoreCase = true)) {
                        return@runCatching waitForRuntimeReady(accountId, deviceId, state.runtime.id)
                    }
                    if (!state.canStart) {
                        throw IOException(state.blockedReason ?: state.effectiveState.ifBlank { getString(R.string.runtime_missing_for_session) })
                    }
                    entitlement?.startRuntimeBlockedMessage(this@ControlsActivity)?.let {
                        throw IOException(it)
                    }
                    val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                    api.startRuntime(sessionStore.baseUrl, accountId, deviceId, state.runtime.id, blobAccessKey)
                    identityPasswordStore.saveConfigured(accountId, deviceId)
                    waitForRuntimeReady(accountId, deviceId, state.runtime.id)
                }.onSuccess {
                    runtime = it
                    runtimeState = null
                    bindRuntime(it)
                    handedOffToViewerPreparation = true
                    connectRuntime(it)
                }.onFailure {
                    toast(it.virtroidDisplayMessage(this@ControlsActivity))
                }
            } finally {
                connectOrStartJob = null
                if (!handedOffToViewerPreparation) {
                    runtime?.let { bindRuntime(it, runtimeState) }
                }
            }
        }
    }

    private suspend fun waitForRuntimeReady(accountId: String, deviceId: String, runtimeId: String): RuntimeSummary {
        val startedAtMs = System.currentTimeMillis()
        while (true) {
            val state = api.getRuntimeState(sessionStore.baseUrl, accountId, deviceId, runtimeId)
            runtime = state.runtime
            runtimeState = state
            bindRuntime(state.runtime, state)
            if (state.canConnectRuntime(runtimeId)) {
                return state.runtime
            }
            terminalRuntimeStartReason(state)?.let { throw IOException(it) }
            if (System.currentTimeMillis() - startedAtMs >= CONNECT_WAIT_MAX_MS) {
                throw IOException(getString(R.string.runtime_start_timeout))
            }
            delay(runtimeWaitDelayMs(startedAtMs))
        }
    }

    private fun runtimeWaitDelayMs(startedAtMs: Long): Long {
        val elapsedMs = System.currentTimeMillis() - startedAtMs
        return when {
            elapsedMs < 30_000L -> 1_000L
            elapsedMs < 120_000L -> 2_000L
            else -> 5_000L
        }
    }

    private fun terminalRuntimeStartReason(state: RuntimeState): String? {
        val runtime = state.runtime
        val runtimeError = runtime.lastError?.takeIf { it.isNotBlank() }
        val blockedReason = state.blockedReason?.takeIf { it.isNotBlank() }

        return when {
            runtimeError != null && !runtime.status.equals("running", ignoreCase = true) -> runtimeError
            state.effectiveState.equals("error", ignoreCase = true) -> {
                runtimeError ?: blockedReason ?: getString(R.string.status_error)
            }
            state.effectiveState.equals("deleted", ignoreCase = true) ||
                state.effectiveState.equals("deleting", ignoreCase = true) -> {
                blockedReason ?: getString(R.string.runtime_deleted)
            }
            state.effectiveState.equals("stopping", ignoreCase = true) ||
                state.effectiveState.equals("wiping", ignoreCase = true) -> {
                blockedReason ?: getString(R.string.runtime_shutdown_in_progress)
            }
            state.effectiveState.equals("stopped", ignoreCase = true) -> {
                blockedReason ?: getString(R.string.runtime_missing_for_session)
            }
            else -> null
        }
    }

    private fun connectRuntime(runtime: RuntimeSummary) {
        activeSessionStore.loadForRuntime(runtime.id)?.let { storedSession ->
            lifecycleScope.launch {
                runCatching {
                    val state = api.getSessionState(
                        baseUrl = storedSession.baseUrl,
                        accountId = storedSession.accountId,
                        deviceId = storedSession.deviceId,
                        sessionId = storedSession.sessionId,
                    )
                    val relayToken = if (state.canResumeRuntime(runtime.id)) {
                        api.issueSessionRelayToken(
                            storedSession.baseUrl,
                            storedSession.accountId,
                            storedSession.deviceId,
                            storedSession.sessionId,
                        )
                    } else {
                        ""
                    }
                    state to relayToken
                }.onSuccess {
                    val (state, relayToken) = it
                    if (state.canResumeRuntime(runtime.id) && relayToken.isNotBlank()) {
                        val updatedSession = storedSession.copy(relayToken = relayToken)
                        activeSessionStore.save(updatedSession)
                        appLogs.info("Returning to active session from controls for ${runtime.name}", "session")
                        startActivity(SessionActivity.createIntent(this@ControlsActivity, updatedSession).addFlags(Intent.FLAG_ACTIVITY_REORDER_TO_FRONT))
                    } else {
                        activeSessionStore.clear()
                        appLogs.warn("Stored active session from controls is ${state.effectiveStatus}; creating a fresh session", "session")
                        connectRuntime(runtime)
                    }
                }.onFailure { error ->
                    if (error.isGoneSessionResponse()) {
                        activeSessionStore.clear()
                        appLogs.warn("Stored active session from controls was gone on backend: ${error.message}", "session")
                        connectRuntime(runtime)
                    } else {
                        appLogs.warn("Stored active session heartbeat from controls failed: ${error.message}", "session")
                        toast(error.virtroidDisplayMessage(this@ControlsActivity))
                    }
                }
            }
            return
        }

        val accountId = sessionStore.accountId ?: return
        val deviceId = sessionStore.deviceId ?: return
        binding.controlsConnectButton.isEnabled = false
        binding.controlsStateValue.text = getString(R.string.controls_state_preparing_viewer)
        lifecycleScope.launch {
            runCatching {
                val state = api.getRuntimeState(sessionStore.baseUrl, accountId, deviceId, runtime.id)
                runtimeState = state
                this@ControlsActivity.runtime = state.runtime
                bindRuntime(state.runtime, state)
                binding.controlsConnectButton.isEnabled = false
                if (!state.canConnectRuntime(runtime.id)) {
                    throw IOException(state.blockedReason ?: getString(R.string.runtime_start_timeout))
                }
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                val session = api.createSession(
                    baseUrl = sessionStore.baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    runtimeId = state.runtime.id,
                    maxSize = sessionMaxSize(),
                    bitRate = DEFAULT_SESSION_BIT_RATE,
                    blobAccessKey = blobAccessKey,
                )
                identityPasswordStore.saveConfigured(accountId, deviceId)
                Pair(session, state.runtime)
            }.onSuccess { (session, readyRuntime) ->
                val activeSession = ActiveSessionStore.ActiveSession(
                    accountId = accountId,
                    deviceId = deviceId,
                    baseUrl = sessionStore.baseUrl,
                    runtimeId = readyRuntime.id,
                    runtimeName = readyRuntime.name,
                    viewerAddress = session.viewerAddress,
                    relayHost = session.relayHost,
                    relayPort = session.relayPort,
                    relayTls = session.relayTls,
                    relayPath = session.relayPath,
                    relayToken = session.relayToken,
                    sessionId = session.sessionId,
                    viewerPublicKey = session.viewerPublicKey,
                )
                activeSessionStore.save(activeSession)
                appLogs.info("Session created from controls for ${readyRuntime.name}", "session")
                startActivity(
                    SessionActivity.createIntent(this@ControlsActivity, activeSession),
                )
            }.onFailure {
                appLogs.error(it.message ?: getString(R.string.status_error), "session")
                binding.controlsConnectButton.isEnabled = true
                binding.controlsStateValue.text = runtime.lifecycleLabel()
                showSessionPrepareError(it, runtime)
            }
        }
    }

    private fun confirmDelete() {
        val current = runtime ?: return
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.controls_delete_confirm_title))
            .setMessage(getString(R.string.controls_delete_confirm_body))
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.controls_confirm)) { _, _ ->
                val accountId = sessionStore.accountId ?: return@setPositiveButton
                val deviceId = sessionStore.deviceId ?: return@setPositiveButton
                lifecycleScope.launch {
                    runCatching {
                        val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                        api.wipeRuntime(sessionStore.baseUrl, accountId, deviceId, current.id, blobAccessKey)
                        identityPasswordStore.saveConfigured(accountId, deviceId)
                        activeSessionStore.loadForRuntime(current.id)?.let {
                            activeSessionStore.clear()
                        }
                        api.deleteRuntime(sessionStore.baseUrl, accountId, deviceId, current.id, blobAccessKey)
                    }.onSuccess {
                        toast(getString(R.string.runtime_delete_queued))
                        finishToRuntimeList()
                    }.onFailure {
                        toast(it.virtroidDisplayMessage(this@ControlsActivity))
                    }
                }
            }
            .show()
    }

    private fun formatAndroidVersion(value: String): String {
        return value
            .removePrefix("android-")
            .removePrefix("Android ")
            .replace('-', ' ')
            .replaceFirstChar { if (it.isLowerCase()) it.titlecase() else it.toString() }
    }

    private fun finishToRuntimeList() {
        startActivity(
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP),
        )
        finish()
    }

    private fun sessionMaxSize(): Int {
        val metrics = resources.displayMetrics
        return max(metrics.widthPixels, metrics.heightPixels).coerceIn(720, 1600)
    }

    private suspend fun requireBlobAccessKey(accountId: String, deviceId: String): String {
        identityPasswordStore.unlockedBlobAccessKey(accountId, deviceId)?.let { return it }
        val password = promptIdentityPassword(
            title = getString(R.string.identity_unlock_title),
            hint = getString(R.string.identity_password_prompt),
        )?.trim().orEmpty()
        if (password.isBlank()) {
            throw IOException(getString(R.string.identity_password_required))
        }
        return identityPasswordStore.unlock(accountId, deviceId, password)
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    private fun RuntimeSummary.isReadyForSession(): Boolean {
        return status.equals("running", ignoreCase = true) &&
            desiredState.equals("running", ignoreCase = true) &&
            connectionStatus.equals("online", ignoreCase = true) &&
            !hostId.isNullOrBlank()
    }

    private fun RuntimeSummary.lifecycleLabel(): String {
        return when {
            isReadyForSession() -> getString(R.string.controls_state_running)
            desiredState.equals("deleted", ignoreCase = true) ||
                status.equals("deleting", ignoreCase = true) -> getString(R.string.controls_state_deleting)
            status.equals("wiping", ignoreCase = true) -> getString(R.string.controls_state_wiping)
            status.equals("stopping", ignoreCase = true) ||
                desiredState.equals("stopped", ignoreCase = true) && connectionStatus.equals("online", ignoreCase = true) -> getString(R.string.controls_state_stopping)
            status.equals("starting", ignoreCase = true) ||
                connectionStatus.equals("connecting", ignoreCase = true) -> getString(R.string.controls_state_starting)
            status.equals("provisioning", ignoreCase = true) ||
                connectionStatus.equals("preparing", ignoreCase = true) -> getString(R.string.controls_state_preparing)
            else -> getString(R.string.controls_state_stopped)
        }
    }

    private fun RuntimeSummary.isLifecycleBusy(): Boolean {
        return desiredState.equals("deleted", ignoreCase = true) ||
            status.equals("deleting", ignoreCase = true) ||
            status.equals("wiping", ignoreCase = true) ||
            status.equals("stopping", ignoreCase = true) ||
            desiredState.equals("stopped", ignoreCase = true) && connectionStatus.equals("online", ignoreCase = true) ||
            status.equals("starting", ignoreCase = true) ||
            status.equals("provisioning", ignoreCase = true) ||
            connectionStatus.equals("connecting", ignoreCase = true) ||
            connectionStatus.equals("preparing", ignoreCase = true)
    }

    private fun showSessionPrepareError(error: Throwable, runtime: RuntimeSummary) {
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.session_prepare_failed_title))
            .setMessage(error.virtroidDisplayMessage(this))
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.session_prepare_retry)) { _, _ ->
                connectRuntime(runtime)
            }
            .show()
    }

    companion object {
        private const val EXTRA_RUNTIME_ID = "extra_runtime_id"
        private const val DEFAULT_SESSION_BIT_RATE = 4_000_000
        private const val CONNECT_WAIT_MAX_MS = 10 * 60 * 1_000L

        fun createIntent(context: Context, runtimeId: String): Intent =
            Intent(context, ControlsActivity::class.java)
                .putExtra(EXTRA_RUNTIME_ID, runtimeId)
    }

    private data class ControlsState(
        val runtime: RuntimeSummary,
        val entitlement: EntitlementSummary,
        val runtimeState: RuntimeState,
    )
}
