package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.view.LayoutInflater
import android.view.animation.AccelerateDecelerateInterpolator
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.Lifecycle
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.lifecycleScope
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.RuntimeState
import io.virtroid.client.api.RuntimeSummary
import io.virtroid.client.api.RuntimeUpdate
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.RuntimeCardBinding
import io.virtroid.client.device.DeviceRuntimeProfile
import io.virtroid.client.databinding.ScreenMyRuntimesBinding
import io.virtroid.client.security.IdentityKeyManager
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.SnapshotRollbackGuard
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPassword
import io.virtroid.client.security.showTypedConfirmation
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.IOException
import java.time.Duration
import java.time.Instant
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlin.math.max

class MainActivity : AppCompatActivity() {
    private lateinit var binding: ScreenMyRuntimesBinding

    private val api = VirtroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var activeSessionStore: ActiveSessionStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private lateinit var identityKeyManager: IdentityKeyManager
    private lateinit var snapshotRollbackGuard: SnapshotRollbackGuard
    private lateinit var appLogs: AppLogStore
    private val timestampFormatter = DateTimeFormatter.ofPattern("MMM d HH:mm", Locale.US)
    private var runtimePollJob: Job? = null
    private var refreshJob: Job? = null
    private var connectJob: Job? = null
    private val runtimeProvisioningStartedAtMs = mutableMapOf<String, Long>()
    private val runtimeProvisioningLogMessages = mutableMapOf<String, List<String>>()
    private val reportedRuntimeErrors = mutableMapOf<String, String>()
    private val locallyStoppingRuntimeIds = mutableSetOf<String>()
    private val locallyStoppingRuntimeStartedAtMs = mutableMapOf<String, Long>()
    private var latestEntitlement: EntitlementSummary? = null
    private var latestRuntimes: List<RuntimeSummary> = emptyList()
    private var latestRuntimeStates: Map<String, RuntimeState> = emptyMap()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenMyRuntimesBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        activeSessionStore = ActiveSessionStore(this)
        identityPasswordStore = IdentityPasswordStore(this)
        identityKeyManager = IdentityKeyManager(this, api)
        snapshotRollbackGuard = SnapshotRollbackGuard(this)
        appLogs = AppLogStore.get(this)
        if (sessionStore.baseUrl.isBlank()) {
            sessionStore.baseUrl = DEFAULT_CONTROL_PLANE_URL
        }

        ViewCompat.setOnApplyWindowInsetsListener(binding.mainContentLayout) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 76 + bars.top,
                right = 24 + bars.right,
                bottom = 112 + bars.bottom,
            )
            insets
        }

        binding.notificationButton.setOnClickListener {
            startActivity(SystemLogsActivity.createIntent(this))
        }
        binding.createRuntimeButton.setOnClickListener {
            openNewRuntime()
        }
        binding.runtimesNavButton.setOnClickListener { refreshRuntimes() }
        binding.securityNavButton.setOnClickListener {
            startActivity(AccountIdentityActivity.createIntent(this))
        }
        binding.logsNavButton.setOnClickListener {
            startActivity(SystemLogsActivity.createIntent(this))
        }
        binding.settingsNavButton.setOnClickListener {
            startActivity(PrivacySecurityActivity.createIntent(this))
        }
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                appLogs.entries.collect {
                    updateNotificationBadge()
                }
            }
        }

        handleRuntimeLifecycleIntent(intent)
        refreshRuntimes()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleRuntimeLifecycleIntent(intent)
        refreshRuntimes(showBusy = false)
    }

    override fun onResume() {
        super.onResume()
        refreshRuntimes(showBusy = false)
    }

    override fun onPause() {
        runtimePollJob?.cancel()
        runtimePollJob = null
        super.onPause()
    }

    override fun onDestroy() {
        runtimePollJob?.cancel()
        refreshJob?.cancel()
        connectJob?.cancel()
        super.onDestroy()
    }

    private fun refreshRuntimes(showBusy: Boolean = true) {
        if (refreshJob?.isActive == true) {
            return
        }
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            startActivity(Intent(this, OnboardingActivity::class.java))
            finish()
            return
        }

        val baseUrl = currentBaseUrl()
        if (showBusy) {
            setBusy(true, getString(R.string.status_refreshing))
        }

        refreshJob = lifecycleScope.launch {
            runCatching {
                appLogs.info("Refreshing runtime list", "runtime")
                val entitlement = api.getEntitlement(baseUrl, accountId, deviceId)
                val runtimeStates = api.listRuntimeStates(baseUrl, accountId, deviceId)
                runtimeStates.forEach { snapshotRollbackGuard.verifyAndRecord(accountId, it.runtime) }
                val runtimes = runtimeStates.map { it.runtime }
                val stateByRuntimeId = runtimeStates.associateBy { it.runtime.id }
                reconcileStoredActiveSession(baseUrl, accountId, deviceId)
                updateProvisioningMetadata(baseUrl, accountId, deviceId, runtimes, stateByRuntimeId)
                RuntimeListState(runtimeStates, entitlement)
            }.onSuccess { state ->
                latestEntitlement = state.entitlement
                state.runtimeStates
                    .map { it.runtime }
                    .filter { runtime ->
                        val runtimeState = state.runtimeStates.firstOrNull { it.runtime.id == runtime.id }
                        runtime.isStoppedForSession() ||
                            runtimeState?.effectiveState.equals("stopped", ignoreCase = true)
                    }
                    .forEach { clearRuntimeLocallyStopping(it.id) }
                renderRuntimeStates(state.runtimeStates, state.entitlement)
                if (showBusy) {
                    setBusy(false)
                }
            }.onFailure { error ->
                appLogs.error(error.message ?: getString(R.string.status_error), "runtime")
                if (showBusy) {
                    setBusy(false)
                    showError(error)
                }
            }
            refreshJob = null
        }
    }

    private suspend fun reconcileStoredActiveSession(
        baseUrl: String,
        accountId: String,
        deviceId: String,
    ) {
        val activeSession = activeSessionStore.load() ?: return
        if (
            activeSession.accountId != accountId ||
            activeSession.deviceId != deviceId ||
            activeSession.baseUrl.trimEnd('/') != baseUrl.trimEnd('/') ||
            activeSession.sessionId.isBlank()
        ) {
            activeSessionStore.clear()
            appLogs.warn("Cleared active session because it no longer matches this device", "session")
            return
        }

        runCatching {
            api.getSessionState(baseUrl, accountId, deviceId, activeSession.runtimeId, activeSession.sessionId)
        }.onSuccess {
            if (it.canResumeRuntime(activeSession.runtimeId)) {
                activeSessionStore.touch(activeSession.sessionId)
            } else {
                activeSessionStore.clear()
                appLogs.warn("Cleared active session because backend state is ${it.effectiveStatus}", "session")
            }
        }.onFailure { error ->
            if (error.isGoneSessionResponse()) {
                activeSessionStore.clear()
                appLogs.warn("Cleared stale active session after backend reported it gone: ${error.message}", "session")
            } else {
                appLogs.warn("Active session heartbeat unavailable during refresh: ${error.message}", "session")
            }
        }
    }

    private fun renderRuntimeStates(runtimeStates: List<RuntimeState>, entitlement: EntitlementSummary? = latestEntitlement) {
        renderRuntimes(
            runtimes = runtimeStates.map { it.runtime },
            entitlement = entitlement,
            stateByRuntimeId = runtimeStates.associateBy { it.runtime.id },
        )
    }

    private fun renderRuntimes(
        runtimes: List<RuntimeSummary>,
        entitlement: EntitlementSummary? = latestEntitlement,
        stateByRuntimeId: Map<String, RuntimeState> = latestRuntimeStates,
    ) {
        latestEntitlement = entitlement
        latestRuntimes = runtimes
        latestRuntimeStates = stateByRuntimeId
        binding.runtimeListContainer.removeAllViews()
        binding.runtimeEmptyText.isVisible = runtimes.isEmpty()
        binding.runtimeEmptyText.text = if (runtimes.isEmpty()) {
            entitlement?.createRuntimeBlockedMessage(this) ?: getString(R.string.home_empty_subtitle)
        } else {
            getString(R.string.home_empty_subtitle)
        }
        if (runtimes.isEmpty()) {
            updateRuntimePolling(runtimes)
            return
        }

        val inflater = LayoutInflater.from(this)
        runtimes.forEach { runtime ->
            val cardBinding = RuntimeCardBinding.inflate(inflater, binding.runtimeListContainer, false)
            bindRuntimeCard(cardBinding, runtime, stateByRuntimeId[runtime.id])
            binding.runtimeListContainer.addView(cardBinding.root)
        }
        updateRuntimePolling(runtimes)
    }

    private fun mergeRuntimeState(state: RuntimeState) {
        latestRuntimeStates = latestRuntimeStates + (state.runtime.id to state)
        mergeRuntimeSnapshot(state.runtime)
    }

    private fun mergeRuntimeSnapshot(runtime: RuntimeSummary) {
        if (latestRuntimes.isEmpty()) {
            latestRuntimes = listOf(runtime)
        } else {
            var replaced = false
            latestRuntimes = latestRuntimes.map {
                if (it.id == runtime.id) {
                    replaced = true
                    runtime
                } else {
                    it
                }
            }
            if (!replaced) {
                latestRuntimes = listOf(runtime) + latestRuntimes
            }
        }
        renderRuntimes(latestRuntimes)
    }

    private fun bindRuntimeCard(cardBinding: RuntimeCardBinding, runtime: RuntimeSummary, state: RuntimeState?) {
        val effectiveState = state?.effectiveState.orEmpty()
        val backendConnectable = state?.canConnectRuntime(runtime.id) == true
        if (runtime.isStoppedForSession() || effectiveState.equals("stopped", ignoreCase = true)) {
            clearRuntimeLocallyStopping(runtime.id)
        }
        if (
            runtime.id in locallyStoppingRuntimeIds &&
            backendConnectable &&
            localStoppingMarkerExpired(runtime.id)
        ) {
            clearRuntimeLocallyStopping(runtime.id)
            appLogs.warn("Runtime ${runtime.name} still reported online after shutdown request; local stopping marker cleared", "session")
        }
        val isLocallyStopping = runtime.id in locallyStoppingRuntimeIds && !runtime.isStoppedForSession()
        val isLive = (backendConnectable || runtime.isReadyForSession()) && !isLocallyStopping
        val isBusy = state?.needsRuntimePolling() ?: runtime.isTransitioning()
        if (isBusy || isLocallyStopping) {
            runtimeProvisioningStartedAtMs.putIfAbsent(runtime.id, System.currentTimeMillis())
        } else {
            runtimeProvisioningStartedAtMs.remove(runtime.id)
            runtimeProvisioningLogMessages.remove(runtime.id)
        }

        cardBinding.runtimeTitleText.text = runtime.name
        cardBinding.runtimeSubtitleText.text = if (isLocallyStopping) {
            getString(R.string.runtime_connectivity_stopping)
        } else {
            state?.connectivityLabel(isLive) ?: runtime.connectivityLabel(isLive)
        }
        cardBinding.runtimeStatusDot.setBackgroundResource(
            when {
                isLive -> R.drawable.bg_dot_accent
                isLocallyStopping -> R.drawable.bg_dot_amber
                isBusy -> R.drawable.bg_dot_amber
                else -> R.drawable.bg_dot_muted
            },
        )
        cardBinding.runtimeIcon.setColorFilter(
            getColor(if (isLive) R.color.v_accent else R.color.v_text_muted),
        )
        cardBinding.root.strokeColor = getColor(if (isLive) R.color.v_border else R.color.v_border)

        cardBinding.runtimeStatOneValue.text = runtime.hardwareLabel()
        cardBinding.runtimeStatTwoValue.text = runtime.uptimeLabel()
        cardBinding.runtimeStatThreeValue.text = runtime.loadLabel()

        cardBinding.runtimeErrorText.isVisible = !runtime.lastError.isNullOrBlank()
        cardBinding.runtimeErrorText.text = runtime.lastError.orEmpty()
        runtime.lastError?.takeIf { it.isNotBlank() }?.let { error ->
            if (reportedRuntimeErrors[runtime.id] != error) {
                reportedRuntimeErrors[runtime.id] = error
                appLogs.critical("Runtime ${runtime.name}: $error", "runtime")
            }
        }
        cardBinding.connectRuntimeButton.isVisible = isLive
        cardBinding.liveRuntimeControlsButton.isVisible = isLive
        cardBinding.liveRuntimeControlsButton.isEnabled = isLive
        cardBinding.runtimeActionRow.isVisible = !isLive
        val provisioningMilestone = if (isLocallyStopping) {
            runtime.stoppingMilestone()
        } else {
            runtime.provisioningMilestone(effectiveState)
        }
        if (provisioningMilestone != null) {
            showRuntimeProvisioning(cardBinding, provisioningMilestone, animate = false)
        } else {
            hideRuntimeProvisioning(cardBinding)
        }
        val actionsEnabled = provisioningMilestone == null && !isLocallyStopping
        cardBinding.startRuntimeButton.isEnabled = actionsEnabled &&
            (state?.canStart ?: true) &&
            (latestEntitlement?.canStartRuntime ?: true)
        cardBinding.actionControlsButton.isEnabled = actionsEnabled
        cardBinding.deleteRuntimeButton.isEnabled = actionsEnabled && (state?.canDelete ?: true)

        cardBinding.connectRuntimeButton.setOnClickListener {
            if (isLocallyStopping) {
                toast(getString(R.string.runtime_shutdown_in_progress))
                refreshRuntimes(showBusy = false)
                return@setOnClickListener
            }
            connectRuntime(runtime)
        }
        cardBinding.startRuntimeButton.setOnClickListener {
            if (isLocallyStopping) {
                toast(getString(R.string.runtime_shutdown_in_progress))
                refreshRuntimes(showBusy = false)
                return@setOnClickListener
            }
            latestEntitlement?.startRuntimeBlockedMessage(this)?.let { message ->
                toast(message)
                return@setOnClickListener
            }
            connectRuntime(runtime)
        }
        cardBinding.deleteRuntimeButton.setOnClickListener {
            confirmRuntimeDelete(runtime)
        }
        cardBinding.actionControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtime.id))
        }
        cardBinding.liveRuntimeControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtime.id))
        }
    }

    private fun confirmRuntimeDelete(runtime: RuntimeSummary) {
        showTypedConfirmation(
            title = getString(R.string.controls_delete_confirm_title),
            message = getString(R.string.controls_delete_confirm_body),
            confirmationPhrase = "DELETE",
            confirmLabel = getString(R.string.controls_confirm),
            onConfirmed = { deleteRuntime(runtime) },
        )
    }

    private fun deleteRuntime(runtime: RuntimeSummary) {
        mutateRuntime(getString(R.string.status_deleting_runtime)) {
            val accountId = requireAccountId() ?: return@mutateRuntime
            val deviceId = requireDeviceId() ?: return@mutateRuntime
            val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
            val state = runCatching {
                api.getRuntimeState(currentBaseUrl(), accountId, deviceId, runtime.id)
            }.getOrNull()
            if (state?.canWipe != false) {
                api.wipeRuntime(currentBaseUrl(), accountId, deviceId, runtime.id, blobAccessKey)
                snapshotRollbackGuard.clearRuntime(accountId, runtime.id)
            }
            activeSessionStore.loadForRuntime(runtime.id)?.let {
                activeSessionStore.clear()
            }
            api.deleteRuntime(
                currentBaseUrl(),
                accountId,
                deviceId,
                runtime.id,
                blobAccessKey,
            )
            snapshotRollbackGuard.clearRuntime(accountId, runtime.id)
            identityPasswordStore.saveConfigured(accountId, deviceId)
        }
    }

    private fun openNewRuntime() {
        latestEntitlement?.createRuntimeBlockedMessage(this)?.let { message ->
            toast(message)
            return
        }
        startActivity(NewRuntimeActivity.createIntent(this))
    }

    private fun connectRuntime(runtime: RuntimeSummary) {
        val runtimeState = latestRuntimeStates[runtime.id]
        if (runtime.isStoppedForSession() || runtimeState?.effectiveState.equals("stopped", ignoreCase = true)) {
            clearRuntimeLocallyStopping(runtime.id)
        }
        if (
            runtime.id in locallyStoppingRuntimeIds &&
            runtimeState?.canConnectRuntime(runtime.id) == true &&
            localStoppingMarkerExpired(runtime.id)
        ) {
            clearRuntimeLocallyStopping(runtime.id)
            appLogs.warn("Runtime ${runtime.name} still reported online after shutdown request; allowing reconnect", "session")
        }
        if (runtime.id in locallyStoppingRuntimeIds && !runtime.isStoppedForSession()) {
            toast(getString(R.string.runtime_shutdown_in_progress))
            refreshRuntimes(showBusy = false)
            return
        }

        activeSessionStore.loadForRuntime(runtime.id)?.let { session ->
            lifecycleScope.launch {
                runCatching {
                    val state = api.getSessionState(session.baseUrl, session.accountId, session.deviceId, session.runtimeId, session.sessionId)
                    val relayRefresh = if (state.canResumeRuntime(runtime.id)) {
                        api.issueSessionRelayToken(session.baseUrl, session.accountId, session.deviceId, session.runtimeId, session.sessionId)
                    } else {
                        null
                    }
                    state to relayRefresh
                }.onSuccess {
                    val (state, relayRefresh) = it
                    if (state.canResumeRuntime(runtime.id) && relayRefresh != null) {
                        val updatedSession = session.copy(
                            relayToken = relayRefresh.relayToken,
                            viewerPublicKey = relayRefresh.viewerPublicKey,
                        )
                        activeSessionStore.save(updatedSession)
                        appLogs.info("Returning to active session for ${runtime.name}", "session")
                        startActivity(SessionActivity.createIntent(this@MainActivity, updatedSession).addFlags(Intent.FLAG_ACTIVITY_REORDER_TO_FRONT))
                    } else {
                        activeSessionStore.clear()
                        appLogs.warn("Stored active session is ${state.effectiveStatus}; creating a fresh session", "session")
                        connectRuntime(runtime)
                    }
                }.onFailure { error ->
                    if (error.isGoneSessionResponse()) {
                        activeSessionStore.clear()
                        appLogs.warn("Stored active session was gone on backend: ${error.message}", "session")
                        connectRuntime(runtime)
                    } else {
                        appLogs.warn("Stored active session heartbeat failed: ${error.message}", "session")
                        showError(error)
                    }
                }
            }
            return
        }

        val accountId = requireAccountId() ?: return
        val deviceId = requireDeviceId() ?: return
        val baseUrl = currentBaseUrl()
        if (connectJob?.isActive == true) {
            toast(getString(R.string.status_preparing_session))
            return
        }
        setBusy(true, getString(R.string.status_preparing_session))
        appLogs.info("Preparing session for ${runtime.name}", "session")

        connectJob = lifecycleScope.launch {
            try {
                runCatching {
                    val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                    val readyRuntime = ensureRuntimeReady(baseUrl, accountId, deviceId, runtime, blobAccessKey)
                    val session = api.createSession(
                        baseUrl = baseUrl,
                        accountId = accountId,
                        deviceId = deviceId,
                        runtimeId = readyRuntime.id,
                        maxSize = sessionMaxSize(),
                        bitRate = DEFAULT_SESSION_BIT_RATE,
                        blobAccessKey = blobAccessKey,
                    )
                    identityPasswordStore.saveConfigured(accountId, deviceId)
                    Pair(session, readyRuntime)
                }.onSuccess { (session, readyRuntime) ->
                    setBusy(false)
                    val activeSession = ActiveSessionStore.ActiveSession(
                        accountId = accountId,
                        deviceId = deviceId,
                        baseUrl = baseUrl,
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
                        audioEnabled = readyRuntime.audioEnabled,
                        cameraMode = readyRuntime.cameraMode,
                        fileMode = readyRuntime.fileMode,
                    )
                    activeSessionStore.save(activeSession)
                    appLogs.info("Session created for ${readyRuntime.name}", "session")
                    startActivity(
                        SessionActivity.createIntent(this@MainActivity, activeSession),
                    )
                }.onFailure { error ->
                    setBusy(false)
                    appLogs.error(error.message ?: getString(R.string.status_error), "session")
                    showSessionPrepareError(error, runtime)
                }
            } finally {
                connectJob = null
            }
        }
    }

    private suspend fun ensureRuntimeReady(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtime: RuntimeSummary,
        blobAccessKey: String,
    ): RuntimeSummary {
        val currentState = latestRuntimeState(baseUrl, accountId, deviceId, runtime.id)
        val currentRuntime = currentState.runtime
        if (currentRuntime.isStoppedForSession() || currentState.effectiveState.equals("stopped", ignoreCase = true)) {
            clearRuntimeLocallyStopping(currentRuntime.id)
        }
        if (
            currentRuntime.id in locallyStoppingRuntimeIds &&
            currentState.canConnectRuntime(runtime.id) &&
            localStoppingMarkerExpired(currentRuntime.id)
        ) {
            clearRuntimeLocallyStopping(currentRuntime.id)
            appLogs.warn("Runtime ${currentRuntime.name} still reported online after shutdown request; allowing session start", "session")
        }
        if (currentRuntime.id in locallyStoppingRuntimeIds && !currentRuntime.isStoppedForSession()) {
            throw IOException(getString(R.string.runtime_shutdown_in_progress))
        }
        if (currentState.canConnectRuntime(runtime.id)) {
            return currentRuntime
        }
        if (currentState.effectiveState.equals("starting", ignoreCase = true)) {
            return waitForRuntimeReady(baseUrl, accountId, deviceId, currentRuntime.id)
        }
        if (currentState.isBusy || currentState.effectiveState in setOf("stopping", "wiping", "deleting", "deleted")) {
            throw IOException(currentState.blockedReason ?: getString(R.string.runtime_shutdown_in_progress))
        }
        latestEntitlement?.startRuntimeBlockedMessage(this)?.let { message ->
            throw IOException(message)
        }

        setBusy(true, getString(R.string.status_starting_runtime_for_session))
        val profiledRuntime = updateRuntimeForViewerAspect(baseUrl, accountId, deviceId, currentRuntime)
        api.startRuntime(baseUrl, accountId, deviceId, profiledRuntime.id, blobAccessKey)
        return waitForRuntimeReady(baseUrl, accountId, deviceId, profiledRuntime.id)
    }

    private suspend fun latestRuntimeState(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
    ): RuntimeState {
        val state = api.getRuntimeState(baseUrl, accountId, deviceId, runtimeId)
        mergeRuntimeState(state)
        updateProvisioningMetadata(baseUrl, accountId, deviceId, latestRuntimes)
        return state
    }

    private suspend fun updateRuntimeForViewerAspect(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtime: RuntimeSummary,
    ): RuntimeSummary {
        if (runtime.isReadyForSession()) {
            return runtime
        }
        val profile = DeviceRuntimeProfile.from(this)
        if (runtime.widthPx == profile.widthPx &&
            runtime.heightPx == profile.heightPx &&
            runtime.densityDpi == profile.densityDpi
        ) {
            return runtime
        }
        appLogs.info(
            "Updating ${runtime.name} display profile to ${profile.widthPx}x${profile.heightPx}@${profile.densityDpi}dpi for viewer fit",
            "runtime",
        )
        return api.updateRuntime(
            baseUrl = baseUrl,
            accountId = accountId,
            deviceId = deviceId,
            runtimeId = runtime.id,
            update = RuntimeUpdate(
                name = runtime.name,
                androidImage = runtime.androidImage,
                androidVersion = runtime.androidVersion,
                widthPx = profile.widthPx,
                heightPx = profile.heightPx,
                densityDpi = profile.densityDpi,
                audioEnabled = runtime.audioEnabled,
                cameraMode = runtime.cameraMode,
                fileMode = runtime.fileMode,
                blobAutoSnapshot = runtime.blobAutoSnapshot,
                blobRetainDays = runtime.blobRetainDays,
            ),
        )
    }

    private suspend fun waitForRuntimeReady(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
    ): RuntimeSummary {
        val startedAtMs = System.currentTimeMillis()
        while (true) {
            val state = latestRuntimeState(baseUrl, accountId, deviceId, runtimeId)
            val runtime = state.runtime

            if (state.canConnectRuntime(runtimeId)) {
                return runtime
            }

            terminalRuntimeStartReason(state)?.let { throw IOException(it) }

            setBusy(
                true,
                getString(
                    R.string.status_waiting_runtime_ready,
                    state.effectiveState.ifBlank { runtime.status.ifBlank { "starting" } },
                    runtime.connectionStatus.ifBlank { "offline" },
                ),
            )

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

    private fun mutateRuntime(
        statusMessage: String,
        request: suspend () -> Unit,
    ) {
        setBusy(true, statusMessage)
        lifecycleScope.launch {
            runCatching { request() }
                .onSuccess { refreshRuntimes() }
                .onFailure {
                    setBusy(false)
                    showError(it)
                }
        }
    }

    private fun requireAccountId(): String? {
        return sessionStore.accountId?.takeIf { it.isNotBlank() } ?: run {
            toast(getString(R.string.account_missing))
            null
        }
    }

    private fun requireDeviceId(): String? {
        return sessionStore.deviceId?.takeIf { it.isNotBlank() } ?: run {
            toast(getString(R.string.device_missing))
            null
        }
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
        return identityKeyManager.unlockOrMigrate(
            sessionStore.baseUrl,
            accountId,
            deviceId,
            password,
        )
    }

    private fun currentBaseUrl(): String {
        return sessionStore.baseUrl
    }

    private fun updateRuntimePolling(runtimes: List<RuntimeSummary>) {
        val shouldPoll = runtimes.any {
            latestRuntimeStates[it.id]?.needsRuntimePolling() == true ||
                it.isTransitioning() ||
                it.id in locallyStoppingRuntimeIds
        }
        if (!shouldPoll) {
            runtimePollJob?.cancel()
            runtimePollJob = null
            return
        }
        if (runtimePollJob?.isActive == true) {
            return
        }

        runtimePollJob = lifecycleScope.launch {
            while (isActive) {
                delay(RUNTIME_POLL_DELAY_MS)
                refreshRuntimes(showBusy = false)
            }
        }
    }

    private fun sessionMaxSize(): Int {
        val metrics = resources.displayMetrics
        return max(metrics.widthPixels, metrics.heightPixels).coerceIn(720, 1600)
    }

    private fun setBusy(isBusy: Boolean, message: String? = null) {
        binding.progressIndicator.isVisible = isBusy
        binding.statusText.text = getString(R.string.home_secure_client)
        binding.notificationButton.isEnabled = !isBusy
        binding.createRuntimeButton.isEnabled = !isBusy && (latestEntitlement?.canCreateRuntime ?: true)
    }

    private fun updateNotificationBadge() {
        val count = appLogs.unresolvedCriticalCount()
        binding.notificationBadgeText.isVisible = count > 0
        binding.notificationBadgeText.text = count.coerceAtMost(99).toString()
    }

    private suspend fun updateProvisioningMetadata(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimes: List<RuntimeSummary>,
        stateByRuntimeId: Map<String, RuntimeState> = latestRuntimeStates,
    ) {
        val transitioningIds = runtimes
            .filter {
                stateByRuntimeId[it.id]?.needsRuntimePolling() == true ||
                    it.isTransitioning() ||
                    it.id in locallyStoppingRuntimeIds
            }
            .map { it.id }
            .toSet()
        runtimeProvisioningStartedAtMs.keys.removeAll { it !in transitioningIds }
        runtimeProvisioningLogMessages.keys.removeAll { it !in transitioningIds }
        transitioningIds.forEach { runtimeId ->
            runtimeProvisioningStartedAtMs.putIfAbsent(runtimeId, System.currentTimeMillis())
            runCatching {
                api.listRuntimeLogs(baseUrl, accountId, deviceId, runtimeId, limit = 4)
                    .asReversed()
                    .mapNotNull { sanitizeRuntimeProgressMessage(it.message) }
            }.onSuccess { messages ->
                if (!messages.isNullOrEmpty()) {
                    runtimeProvisioningLogMessages[runtimeId] = messages
                }
            }
        }
    }

    private fun showRuntimeProvisioning(
        cardBinding: RuntimeCardBinding,
        milestone: RuntimeProvisioningMilestone,
        animate: Boolean,
    ) {
        cardBinding.runtimeProvisioningTitleText.text = milestone.title
        cardBinding.runtimeProvisioningCommandText.text = milestone.command
        cardBinding.runtimeProvisioningDetailText.text = milestone.detail
        cardBinding.runtimeProvisioningEventTrailText.text = milestone.events.joinToString("\n")
        cardBinding.runtimeInteractiveContent.alpha = 0.08f
        cardBinding.runtimeProvisioningLogContainer.isVisible = true
        if (animate) {
            cardBinding.runtimeProvisioningLogContainer.alpha = 0f
            cardBinding.runtimeProvisioningLogContainer.translationY = -dp(22).toFloat()
            cardBinding.runtimeProvisioningLogContainer.animate()
                .alpha(1f)
                .translationY(0f)
                .setDuration(260L)
                .setInterpolator(AccelerateDecelerateInterpolator())
                .start()
        } else {
            cardBinding.runtimeProvisioningLogContainer.alpha = 1f
            cardBinding.runtimeProvisioningLogContainer.translationY = 0f
        }
    }

    private fun hideRuntimeProvisioning(cardBinding: RuntimeCardBinding) {
        cardBinding.runtimeInteractiveContent.alpha = 1f
        cardBinding.runtimeProvisioningLogContainer.clearAnimation()
        cardBinding.runtimeProvisioningLogContainer.isVisible = false
    }

    private fun dp(value: Int): Int {
        return (value * resources.displayMetrics.density).toInt()
    }

    private fun formatTimestamp(value: String?): String {
        if (value.isNullOrBlank()) {
            return getString(R.string.runtime_stat_ping_offline)
        }

        return runCatching {
            OffsetDateTime.parse(value).format(timestampFormatter)
        }.getOrDefault(value)
    }

    private fun RuntimeSummary.hardwareLabel(): String {
        val brand = personaBrand?.trim().orEmpty()
        val model = personaModel?.trim().orEmpty()
        return listOf(brand, model)
            .filter { it.isNotBlank() }
            .map { it.toTitleWords() }
            .distinctBy { it.lowercase(Locale.US) }
            .joinToString(" ")
            .ifBlank { getString(R.string.runtime_stat_load_unknown) }
    }

    private fun RuntimeSummary.uptimeLabel(): String {
        if (!isReadyForSession()) {
            return getString(R.string.runtime_stat_load_unknown)
        }
        val started = startedAt?.let { value ->
            runCatching { OffsetDateTime.parse(value).toInstant() }
                .recoverCatching { Instant.parse(value) }
                .getOrNull()
        } ?: return getString(R.string.runtime_stat_load_unknown)
        val seconds = Duration.between(started, Instant.now()).seconds.coerceAtLeast(0L)
        val days = seconds / 86_400L
        val hours = (seconds % 86_400L) / 3_600L
        val minutes = (seconds % 3_600L) / 60L
        return when {
            days > 0L -> "${days}d ${hours}h"
            hours > 0L -> "${hours}h ${minutes}m"
            minutes > 0L -> "${minutes}m"
            else -> "${seconds}s"
        }
    }

    private fun RuntimeSummary.loadLabel(): String {
        val value = loadAverage ?: return getString(R.string.runtime_stat_load_unknown)
        return String.format(Locale.US, "%.2f", value)
    }

    private fun String.toTitleWords(): String {
        return split(Regex("\\s+"))
            .filter { it.isNotBlank() }
            .joinToString(" ") { word ->
                word.replaceFirstChar { char ->
                    if (char.isLowerCase()) char.titlecase(Locale.US) else char.toString()
                }
            }
    }

    private fun showError(error: Throwable) {
        toast(error.virtroidDisplayMessage(this))
    }

    private fun showSessionPrepareError(error: Throwable, runtime: RuntimeSummary) {
        val message = error.virtroidDisplayMessage(this)
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.session_prepare_failed_title))
            .setMessage(message)
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.session_prepare_retry)) { _, _ ->
                connectRuntime(runtime)
            }
            .show()
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

    private fun RuntimeSummary.isStoppedForSession(): Boolean {
        val stopped = status.equals("stopped", ignoreCase = true)
        val desiredStopped = desiredState.equals("stopped", ignoreCase = true)
        val offline = connectionStatus.isBlank() ||
            connectionStatus.equals("offline", ignoreCase = true) ||
            connectionStatus.equals("disconnected", ignoreCase = true)
        return stopped && desiredStopped && offline
    }

    private fun RuntimeState.connectivityLabel(isLive: Boolean): String {
        return when (effectiveState.lowercase(Locale.US)) {
            "running" -> if (isLive) getString(R.string.runtime_connectivity_online) else getString(R.string.runtime_connectivity_offline)
            "starting" -> getString(R.string.runtime_connectivity_starting)
            "stopping" -> getString(R.string.runtime_connectivity_stopping)
            "wiping" -> getString(R.string.runtime_connectivity_wiping)
            "deleting" -> getString(R.string.runtime_connectivity_deleting)
            else -> getString(R.string.runtime_connectivity_offline)
        }
    }

    private fun RuntimeState.needsRuntimePolling(): Boolean {
        return isBusy || effectiveState.equals("starting", ignoreCase = true)
    }

    private fun RuntimeSummary.connectivityLabel(isLive: Boolean): String {
        return when {
            isLive -> getString(R.string.runtime_connectivity_online)
            isStartingForSession() -> getString(R.string.runtime_connectivity_starting)
            else -> getString(R.string.runtime_connectivity_offline)
        }
    }

    private fun RuntimeSummary.isStartingForSession(): Boolean {
        if (status.equals("error", ignoreCase = true)) {
            return false
        }
        return desiredState.equals("running", ignoreCase = true) && !isReadyForSession() ||
            status.equals("starting", ignoreCase = true) ||
            status.equals("provisioning", ignoreCase = true) ||
            connectionStatus.equals("connecting", ignoreCase = true) ||
            connectionStatus.equals("preparing", ignoreCase = true)
    }

    private fun RuntimeSummary.isTransitioning(): Boolean {
        if (status.equals("error", ignoreCase = true)) {
            return false
        }
        return desiredState.equals("deleted", ignoreCase = true) ||
            status.equals("deleting", ignoreCase = true) ||
            status.equals("wiping", ignoreCase = true) ||
            status.equals("stopping", ignoreCase = true) ||
            desiredState.equals("stopped", ignoreCase = true) && connectionStatus.equals("online", ignoreCase = true) ||
            desiredState.equals("running", ignoreCase = true) && !isReadyForSession() ||
            status.equals("starting", ignoreCase = true) ||
            status.equals("provisioning", ignoreCase = true) ||
            connectionStatus.equals("connecting", ignoreCase = true) ||
            connectionStatus.equals("preparing", ignoreCase = true)
    }

    private fun RuntimeSummary.provisioningMilestone(effectiveState: String = ""): RuntimeProvisioningMilestone? {
        val backendState = effectiveState.lowercase(Locale.US)
        val backendBusy = backendState in setOf("starting", "stopping", "wiping", "deleting")
        if (!backendBusy && (isReadyForSession() || !isTransitioning())) {
            return null
        }
        if (!lastError.isNullOrBlank()) {
            return RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_error),
                command = getString(R.string.runtime_provisioning_command_error),
                detail = "... ${lastError.orEmpty()}",
                events = listOf(getString(R.string.runtime_provisioning_event_error)),
            )
        }

        runtimeProvisioningStartedAtMs.getOrPut(id) { System.currentTimeMillis() }
        val logMessages = runtimeProvisioningLogMessages[id].orEmpty()
        val latestLog = logMessages.lastOrNull()
        fun detail(defaultValue: String): String = latestLog?.let { "... $it" } ?: defaultValue
        fun command(): String = getString(
            R.string.runtime_progress_command_state,
            backendState.ifBlank { status.ifBlank { "unknown" } },
            connectionStatus.ifBlank { "offline" },
        )
        fun events(fallback: String): List<String> = runtimeProgressEvents(logMessages, fallback)

        return when {
            backendState == "deleting" ||
                desiredState.equals("deleted", ignoreCase = true) ||
                status.equals("deleting", ignoreCase = true) -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_lifecycle_title_deleting),
                command = command(),
                detail = detail(getString(R.string.runtime_lifecycle_detail_deleting)),
                events = events(getString(R.string.runtime_progress_event_waiting)),
            )
            backendState == "wiping" || status.equals("wiping", ignoreCase = true) -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_lifecycle_title_wiping),
                command = command(),
                detail = detail(getString(R.string.runtime_lifecycle_detail_wiping)),
                events = events(getString(R.string.runtime_progress_event_waiting)),
            )
            backendState == "stopping" ||
                status.equals("stopping", ignoreCase = true) ||
                desiredState.equals("stopped", ignoreCase = true) && connectionStatus.equals("online", ignoreCase = true) -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_lifecycle_title_stopping),
                command = command(),
                detail = detail(getString(R.string.runtime_lifecycle_detail_stopping)),
                events = events(getString(R.string.runtime_progress_event_waiting)),
            )
            status.equals("provisioning", ignoreCase = true) || connectionStatus.equals("preparing", ignoreCase = true) -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_viewer),
                command = command(),
                detail = detail(getString(R.string.runtime_provisioning_detail_viewer)),
                events = events(getString(R.string.runtime_progress_event_waiting)),
            )
            else -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_connect),
                command = command(),
                detail = detail(getString(R.string.runtime_provisioning_detail_connect)),
                events = events(getString(R.string.runtime_progress_event_waiting)),
            )
        }
    }

    private fun RuntimeSummary.stoppingMilestone(): RuntimeProvisioningMilestone {
        runtimeProvisioningStartedAtMs.getOrPut(id) { System.currentTimeMillis() }
        val logMessages = runtimeProvisioningLogMessages[id].orEmpty()
        val detail = logMessages.lastOrNull()?.let { "... $it" }
            ?: getString(R.string.runtime_lifecycle_detail_stopping)
        return RuntimeProvisioningMilestone(
            title = getString(R.string.runtime_lifecycle_title_stopping),
            command = getString(
                R.string.runtime_progress_command_state,
                status.ifBlank { "stopping" },
                connectionStatus.ifBlank { "offline" },
            ),
            detail = detail,
            events = runtimeProgressEvents(logMessages, getString(R.string.runtime_progress_event_waiting)),
        )
    }

    private fun handleRuntimeLifecycleIntent(intent: Intent?) {
        val stoppingRuntimeId = intent
            ?.getStringExtra(EXTRA_STOPPING_RUNTIME_ID)
            ?.takeIf { it.isNotBlank() }
            ?: return
        markRuntimeLocallyStopping(stoppingRuntimeId)
        activeSessionStore.clear()
        if (latestRuntimes.isNotEmpty()) {
            renderRuntimes(latestRuntimes)
        }
    }

    private fun markRuntimeLocallyStopping(runtimeId: String) {
        locallyStoppingRuntimeIds.add(runtimeId)
        locallyStoppingRuntimeStartedAtMs[runtimeId] = System.currentTimeMillis()
    }

    private fun clearRuntimeLocallyStopping(runtimeId: String) {
        locallyStoppingRuntimeIds.remove(runtimeId)
        locallyStoppingRuntimeStartedAtMs.remove(runtimeId)
    }

    private fun localStoppingMarkerExpired(runtimeId: String): Boolean {
        val startedAt = locallyStoppingRuntimeStartedAtMs[runtimeId] ?: return false
        return System.currentTimeMillis() - startedAt >= LOCAL_STOPPING_MARKER_GRACE_MS
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

    private data class RuntimeProvisioningMilestone(
        val title: String,
        val command: String,
        val detail: String,
        val events: List<String>,
    )

    private data class RuntimeListState(
        val runtimeStates: List<RuntimeState>,
        val entitlement: EntitlementSummary,
    )

    companion object {
        val DEFAULT_CONTROL_PLANE_URL = BuildConfig.DEFAULT_CONTROL_PLANE_URL
        private const val EXTRA_STOPPING_RUNTIME_ID = "io.virtroid.client.extra.STOPPING_RUNTIME_ID"
        const val DEFAULT_SESSION_BIT_RATE = 4_000_000
        private const val CONNECT_WAIT_MAX_MS = 10 * 60 * 1_000L
        private const val LOCAL_STOPPING_MARKER_GRACE_MS = 45_000L
        const val RUNTIME_POLL_DELAY_MS = 2_000L

        fun createRuntimeStoppingIntent(context: Context, runtimeId: String): Intent {
            return Intent(context, MainActivity::class.java)
                .putExtra(EXTRA_STOPPING_RUNTIME_ID, runtimeId)
        }
    }
}
