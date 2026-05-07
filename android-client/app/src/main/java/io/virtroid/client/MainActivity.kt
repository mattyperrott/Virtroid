package io.virtroid.client

import android.os.Bundle
import android.view.LayoutInflater
import android.content.Intent
import android.view.View
import android.view.animation.AccelerateDecelerateInterpolator
import android.view.animation.AlphaAnimation
import android.view.animation.Animation
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
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.RuntimeSummary
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.RuntimeCardBinding
import io.virtroid.client.databinding.ScreenMyRuntimesBinding
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPassword
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.IOException
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
    private lateinit var appLogs: AppLogStore
    private val timestampFormatter = DateTimeFormatter.ofPattern("MMM d HH:mm", Locale.US)
    private var runtimePollJob: Job? = null
    private var refreshJob: Job? = null
    private val runtimeProvisioningStartedAtMs = mutableMapOf<String, Long>()
    private val runtimeProvisioningLastMessage = mutableMapOf<String, String>()
    private val reportedRuntimeErrors = mutableMapOf<String, String>()
    private var latestEntitlement: EntitlementSummary? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenMyRuntimesBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        activeSessionStore = ActiveSessionStore(this)
        identityPasswordStore = IdentityPasswordStore(this)
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
            startActivity(SystemLogsActivity.createIntent(this, errorsOnly = true))
        }
        binding.accessToggleButton.setOnClickListener {
            startActivity(AccountIdentityActivity.createIntent(this))
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

        refreshRuntimes()
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
                val runtimes = api.listRuntimes(baseUrl, accountId, deviceId)
                reconcileStoredActiveSession(baseUrl, accountId, deviceId, runtimes)
                updateProvisioningMetadata(baseUrl, accountId, deviceId, runtimes)
                RuntimeListState(runtimes, entitlement)
            }.onSuccess { state ->
                latestEntitlement = state.entitlement
                renderRuntimes(state.runtimes, state.entitlement)
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
        runtimes: List<RuntimeSummary>,
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

        val runtime = runtimes.firstOrNull { it.id == activeSession.runtimeId }
        if (runtime == null || !runtime.isReadyForSession()) {
            activeSessionStore.clear()
            appLogs.warn("Cleared stale active session for unavailable runtime", "session")
            return
        }

        runCatching {
            api.heartbeatSession(baseUrl, accountId, deviceId, activeSession.sessionId)
        }.onSuccess {
            activeSessionStore.touch(activeSession.sessionId)
        }.onFailure { error ->
            activeSessionStore.clear()
            appLogs.warn("Cleared stale active session after heartbeat failure: ${error.message}", "session")
        }
    }

    private fun renderRuntimes(runtimes: List<RuntimeSummary>, entitlement: EntitlementSummary? = latestEntitlement) {
        latestEntitlement = entitlement
        renderEntitlement(entitlement)
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
            bindRuntimeCard(cardBinding, runtime)
            binding.runtimeListContainer.addView(cardBinding.root)
        }
        updateRuntimePolling(runtimes)
    }

    private fun renderEntitlement(entitlement: EntitlementSummary?) {
        binding.entitlementPanel.isVisible = entitlement != null
        if (entitlement == null) {
            binding.createRuntimeButton.isEnabled = !binding.progressIndicator.isVisible
            return
        }

        binding.entitlementTitleText.text = when (entitlement.source.lowercase(Locale.US)) {
            "trial" -> getString(
                R.string.entitlement_trial_title,
                entitlement.runtimeLimit,
                entitlement.runtimeStartsPerDay,
            )
            else -> getString(
                R.string.entitlement_plan_title,
                entitlement.source.ifBlank { entitlement.status }.replaceFirstChar { it.titlecase(Locale.US) },
                entitlement.runtimeLimit,
                entitlement.runtimeStartsPerDay,
            )
        }
        binding.entitlementDetailText.text = getString(
            R.string.entitlement_detail,
            entitlement.runtimeCount,
            entitlement.runtimeLimit,
            entitlement.runtimeStartsRemainingToday,
        )
        binding.createRuntimeButton.isEnabled = !binding.progressIndicator.isVisible && entitlement.canCreateRuntime
    }

    private fun bindRuntimeCard(cardBinding: RuntimeCardBinding, runtime: RuntimeSummary) {
        val isLive = runtime.isReadyForSession()
        if (runtime.isTransitioning()) {
            runtimeProvisioningStartedAtMs.putIfAbsent(runtime.id, System.currentTimeMillis())
        } else {
            runtimeProvisioningStartedAtMs.remove(runtime.id)
            runtimeProvisioningLastMessage.remove(runtime.id)
        }

        cardBinding.runtimeTitleText.text = runtime.name
        cardBinding.runtimeSubtitleText.text = if (isLive) {
            getString(R.string.runtime_connectivity_online)
        } else {
            getString(R.string.runtime_connectivity_offline)
        }
        cardBinding.runtimeStatusDot.setBackgroundResource(
            if (isLive) R.drawable.bg_dot_accent else R.drawable.bg_dot_muted,
        )
        cardBinding.runtimeIcon.setColorFilter(
            getColor(if (isLive) R.color.v_accent else R.color.v_text_muted),
        )
        cardBinding.root.strokeColor = getColor(if (isLive) R.color.v_border else R.color.v_border)

        cardBinding.runtimeStatOneValue.text = getString(R.string.runtime_stat_load_unknown)
        cardBinding.runtimeStatTwoValue.text = runtime.hardwareLabel()
        cardBinding.runtimeStatThreeValue.text = getString(R.string.runtime_stat_load_unknown)

        cardBinding.runtimeErrorText.isVisible = !runtime.lastError.isNullOrBlank()
        cardBinding.runtimeErrorText.text = runtime.lastError.orEmpty()
        runtime.lastError?.takeIf { it.isNotBlank() }?.let { error ->
            if (reportedRuntimeErrors[runtime.id] != error) {
                reportedRuntimeErrors[runtime.id] = error
                appLogs.critical("Runtime ${runtime.name}: $error", "runtime")
            }
        }
        cardBinding.connectRuntimeButton.isVisible = isLive
        cardBinding.runtimeActionRow.isVisible = !isLive
        val provisioningMilestone = runtime.provisioningMilestone()
        if (provisioningMilestone != null) {
            showRuntimeProvisioning(cardBinding, provisioningMilestone, animate = false)
        } else {
            hideRuntimeProvisioning(cardBinding)
        }
        val actionsEnabled = provisioningMilestone == null
        cardBinding.startRuntimeButton.isEnabled = actionsEnabled && (latestEntitlement?.canStartRuntime ?: true)
        cardBinding.actionControlsButton.isEnabled = actionsEnabled
        cardBinding.deleteRuntimeButton.isEnabled = actionsEnabled

        cardBinding.connectRuntimeButton.setOnClickListener {
            connectRuntime(runtime)
        }
        cardBinding.startRuntimeButton.setOnClickListener {
            latestEntitlement?.startRuntimeBlockedMessage(this)?.let { message ->
                toast(message)
                return@setOnClickListener
            }
            startRuntime(runtime, cardBinding)
        }
        cardBinding.deleteRuntimeButton.setOnClickListener {
            mutateRuntime(getString(R.string.status_deleting_runtime)) {
                api.deleteRuntime(
                    currentBaseUrl(),
                    requireAccountId() ?: return@mutateRuntime,
                    requireDeviceId() ?: return@mutateRuntime,
                    runtime.id,
                )
            }
        }
        cardBinding.runtimeControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtime.id))
        }
        cardBinding.actionControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtime.id))
        }
    }

    private fun openNewRuntime() {
        latestEntitlement?.createRuntimeBlockedMessage(this)?.let { message ->
            toast(message)
            return
        }
        startActivity(NewRuntimeActivity.createIntent(this))
    }

    private fun startRuntime(runtime: RuntimeSummary, cardBinding: RuntimeCardBinding) {
        latestEntitlement?.startRuntimeBlockedMessage(this)?.let { message ->
            toast(message)
            return
        }
        showRuntimeProvisioning(
            cardBinding,
            RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_request),
                command = getString(R.string.runtime_provisioning_command_request),
                detail = getString(R.string.runtime_provisioning_detail_request),
                events = provisioningEvents(0L),
            ),
            animate = true,
        )
        runtimeProvisioningStartedAtMs[runtime.id] = System.currentTimeMillis()
        runtimeProvisioningLastMessage[runtime.id] = getString(R.string.runtime_provisioning_detail_request)
        appLogs.info("Runtime provisioning requested for ${runtime.name}", "runtime")
        cardBinding.startRuntimeButton.isEnabled = false
        cardBinding.actionControlsButton.isEnabled = false
        cardBinding.deleteRuntimeButton.isEnabled = false

        lifecycleScope.launch {
            runCatching {
                val accountId = requireAccountId() ?: throw IOException(getString(R.string.account_missing))
                val deviceId = requireDeviceId() ?: throw IOException(getString(R.string.device_missing))
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                api.startRuntime(currentBaseUrl(), accountId, deviceId, runtime.id, blobAccessKey)
            }.onSuccess {
                appLogs.info("Runtime start accepted for ${runtime.name}", "runtime")
                refreshRuntimes(showBusy = false)
            }.onFailure { error ->
                appLogs.error(error.message ?: getString(R.string.status_error), "runtime")
                showRuntimeProvisioning(
                    cardBinding,
                    RuntimeProvisioningMilestone(
                        title = getString(R.string.runtime_provisioning_title_error),
                        command = getString(R.string.runtime_provisioning_command_error),
                        detail = "... ${error.message ?: getString(R.string.status_error)}",
                        events = listOf(getString(R.string.runtime_provisioning_event_error)),
                    ),
                    animate = false,
                )
                cardBinding.startRuntimeButton.isEnabled = true
                cardBinding.actionControlsButton.isEnabled = true
                cardBinding.deleteRuntimeButton.isEnabled = true
                showError(error)
            }
        }
    }

    private fun connectRuntime(runtime: RuntimeSummary) {
        activeSessionStore.loadForRuntime(runtime.id)?.let { session ->
            lifecycleScope.launch {
                runCatching {
                    api.heartbeatSession(session.baseUrl, session.accountId, session.deviceId, session.sessionId)
                }.onSuccess {
                    activeSessionStore.touch(session.sessionId)
                    appLogs.info("Returning to active session for ${runtime.name}", "session")
                    startActivity(SessionActivity.createIntent(this@MainActivity, session).addFlags(Intent.FLAG_ACTIVITY_REORDER_TO_FRONT))
                }.onFailure { error ->
                    activeSessionStore.clear()
                    appLogs.warn("Stored active session was stale: ${error.message}", "session")
                    connectRuntime(runtime)
                }
            }
            return
        }

        val accountId = requireAccountId() ?: return
        val deviceId = requireDeviceId() ?: return
        val baseUrl = currentBaseUrl()
        setBusy(true, getString(R.string.status_preparing_session))
        appLogs.info("Preparing session for ${runtime.name}", "session")

        lifecycleScope.launch {
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
                )
                activeSessionStore.save(activeSession)
                appLogs.info("Session created for ${readyRuntime.name}", "session")
                startActivity(
                    SessionActivity.createIntent(this@MainActivity, activeSession),
                )
            }.onFailure { error ->
                setBusy(false)
                appLogs.error(error.message ?: getString(R.string.status_error), "session")
                showError(error)
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
        if (runtime.isReadyForSession()) {
            return runtime
        }
        latestEntitlement?.startRuntimeBlockedMessage(this)?.let { message ->
            throw IOException(message)
        }

        setBusy(true, getString(R.string.status_starting_runtime_for_session))
        api.startRuntime(baseUrl, accountId, deviceId, runtime.id, blobAccessKey)
        return waitForRuntimeReady(baseUrl, accountId, deviceId, runtime.id)
    }

    private suspend fun waitForRuntimeReady(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
    ): RuntimeSummary {
        repeat(CONNECT_WAIT_ATTEMPTS) { attempt ->
            val runtimes = api.listRuntimes(baseUrl, accountId, deviceId)
            updateProvisioningMetadata(baseUrl, accountId, deviceId, runtimes)
            renderRuntimes(runtimes)
            val runtime = runtimes.firstOrNull { it.id == runtimeId }
                ?: throw IOException(getString(R.string.runtime_missing_for_session))

            if (runtime.isReadyForSession()) {
                return runtime
            }

            if (!runtime.lastError.isNullOrBlank() &&
                !runtime.status.equals("running", ignoreCase = true)
            ) {
                throw IOException(runtime.lastError)
            }

            setBusy(
                true,
                getString(
                    R.string.status_waiting_runtime_ready,
                    runtime.status.ifBlank { "starting" },
                    runtime.connectionStatus.ifBlank { "offline" },
                ),
            )

            if (attempt < CONNECT_WAIT_ATTEMPTS - 1) {
                delay(CONNECT_WAIT_DELAY_MS)
            }
        }

        throw IOException(getString(R.string.runtime_start_timeout))
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
        return identityPasswordStore.unlock(accountId, deviceId, password)
    }

    private fun currentBaseUrl(): String {
        return sessionStore.baseUrl
    }

    private fun updateRuntimePolling(runtimes: List<RuntimeSummary>) {
        val shouldPoll = runtimes.any { it.isTransitioning() }
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
        binding.accessToggleButton.isEnabled = !isBusy
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
    ) {
        val transitioningIds = runtimes
            .filter { it.isTransitioning() }
            .map { it.id }
            .toSet()
        runtimeProvisioningStartedAtMs.keys.removeAll { it !in transitioningIds }
        runtimeProvisioningLastMessage.keys.removeAll { it !in transitioningIds }
        transitioningIds.forEach { runtimeId ->
            runtimeProvisioningStartedAtMs.putIfAbsent(runtimeId, System.currentTimeMillis())
            runCatching {
                api.listRuntimeLogs(baseUrl, accountId, deviceId, runtimeId, limit = 1)
                    .firstOrNull()
                    ?.message
                    ?.takeIf { it.isNotBlank() }
            }.onSuccess { message ->
                if (!message.isNullOrBlank()) {
                    runtimeProvisioningLastMessage[runtimeId] = message
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
        startRuntimeProvisioningPulse(cardBinding.runtimeProvisioningDot)
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
        cardBinding.runtimeProvisioningDot.clearAnimation()
        cardBinding.runtimeProvisioningLogContainer.clearAnimation()
        cardBinding.runtimeProvisioningLogContainer.isVisible = false
    }

    private fun startRuntimeProvisioningPulse(view: View) {
        if (view.animation != null) {
            return
        }
        val animation = AlphaAnimation(0.35f, 1f).apply {
            duration = 520L
            repeatMode = Animation.REVERSE
            repeatCount = Animation.INFINITE
        }
        view.startAnimation(animation)
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
            .distinctBy { it.lowercase(Locale.US) }
            .joinToString(" ")
            .ifBlank { getString(R.string.runtime_stat_load_unknown) }
    }

    private fun showError(error: Throwable) {
        toast(error.virtroidDisplayMessage(this))
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    private fun RuntimeSummary.isReadyForSession(): Boolean {
        return status.equals("running", ignoreCase = true) &&
            connectionStatus.equals("online", ignoreCase = true) &&
            !hostId.isNullOrBlank()
    }

    private fun RuntimeSummary.isTransitioning(): Boolean {
        return desiredState.equals("running", ignoreCase = true) && !isReadyForSession() ||
            status.equals("starting", ignoreCase = true) ||
            connectionStatus.equals("connecting", ignoreCase = true)
    }

    private fun RuntimeSummary.provisioningMilestone(): RuntimeProvisioningMilestone? {
        if (isReadyForSession() || !isTransitioning()) {
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

        val startedAt = runtimeProvisioningStartedAtMs.getOrPut(id) { System.currentTimeMillis() }
        val elapsedMs = System.currentTimeMillis() - startedAt
        fun detail(defaultValue: String): String {
            val latest = runtimeProvisioningLastMessage[id]?.trim().orEmpty()
            return if (latest.isNotBlank()) {
                if (latest.startsWith("...")) latest else "... $latest"
            } else {
                defaultValue
            }
        }

        return when {
            connectionStatus.equals("connecting", ignoreCase = true) -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_connect),
                command = getString(R.string.runtime_provisioning_command_connect),
                detail = detail(getString(R.string.runtime_provisioning_detail_connect)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 2_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_container),
                command = getString(R.string.runtime_provisioning_command_container),
                detail = detail(getString(R.string.runtime_provisioning_detail_container)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 3_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_request),
                command = getString(R.string.runtime_provisioning_command_request),
                detail = detail(getString(R.string.runtime_provisioning_detail_request)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 6_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_storage),
                command = getString(R.string.runtime_provisioning_command_storage),
                detail = detail(getString(R.string.runtime_provisioning_detail_storage)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 9_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_restore),
                command = getString(R.string.runtime_provisioning_command_restore),
                detail = detail(getString(R.string.runtime_provisioning_detail_restore)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 13_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_network),
                command = getString(R.string.runtime_provisioning_command_network),
                detail = detail(getString(R.string.runtime_provisioning_detail_network)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 18_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_boot),
                command = getString(R.string.runtime_provisioning_command_boot),
                detail = detail(getString(R.string.runtime_provisioning_detail_boot)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 28_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_android),
                command = getString(R.string.runtime_provisioning_command_android),
                detail = detail(getString(R.string.runtime_provisioning_detail_android)),
                events = provisioningEvents(elapsedMs),
            )
            elapsedMs < 40_000L -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_viewer),
                command = getString(R.string.runtime_provisioning_command_viewer),
                detail = detail(getString(R.string.runtime_provisioning_detail_viewer)),
                events = provisioningEvents(elapsedMs),
            )
            else -> RuntimeProvisioningMilestone(
                title = getString(R.string.runtime_provisioning_title_connect),
                command = getString(R.string.runtime_provisioning_command_connect),
                detail = detail(getString(R.string.runtime_provisioning_detail_connect)),
                events = provisioningEvents(elapsedMs),
            )
        }
    }

    private fun provisioningEvents(elapsedMs: Long): List<String> {
        val allEvents = listOf(
            getString(R.string.runtime_provisioning_event_container),
            getString(R.string.runtime_provisioning_event_storage),
            getString(R.string.runtime_provisioning_event_network),
            getString(R.string.runtime_provisioning_event_filesystem),
            getString(R.string.runtime_provisioning_event_image),
            getString(R.string.runtime_provisioning_event_services),
            getString(R.string.runtime_provisioning_event_channel),
            getString(R.string.runtime_provisioning_event_handoff),
            getString(R.string.runtime_provisioning_event_ready),
        )
        val visibleCount = ((elapsedMs / 3_000L).toInt() + 2).coerceIn(2, allEvents.size)
        return allEvents.take(visibleCount).takeLast(4)
    }

    private data class RuntimeProvisioningMilestone(
        val title: String,
        val command: String,
        val detail: String,
        val events: List<String>,
    )

    private data class RuntimeListState(
        val runtimes: List<RuntimeSummary>,
        val entitlement: EntitlementSummary,
    )

    private companion object {
        val DEFAULT_CONTROL_PLANE_URL = BuildConfig.DEFAULT_CONTROL_PLANE_URL
        const val DEFAULT_SESSION_BIT_RATE = 4_000_000
        const val CONNECT_WAIT_ATTEMPTS = 45
        const val CONNECT_WAIT_DELAY_MS = 1_000L
        const val RUNTIME_POLL_DELAY_MS = 2_000L
    }
}
