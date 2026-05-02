package io.virtdroid.client

import android.os.Bundle
import android.view.LayoutInflater
import android.content.Intent
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtdroid.client.api.RuntimeSummary
import io.virtdroid.client.api.VirtdroidApi
import io.virtdroid.client.data.SessionStore
import io.virtdroid.client.databinding.RuntimeCardBinding
import io.virtdroid.client.databinding.ScreenMyRuntimesBinding
import io.virtdroid.client.security.IdentityPasswordStore
import io.virtdroid.client.security.enableSecureWindow
import io.virtdroid.client.security.promptIdentityPassword
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.io.IOException
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlin.math.max

class MainActivity : AppCompatActivity() {
    private lateinit var binding: ScreenMyRuntimesBinding

    private val api = VirtdroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private val timestampFormatter = DateTimeFormatter.ofPattern("MMM d HH:mm", Locale.US)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenMyRuntimesBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        identityPasswordStore = IdentityPasswordStore(this)
        if (sessionStore.baseUrl.isBlank()) {
            sessionStore.baseUrl = DEFAULT_CONTROL_PLANE_URL
        }

        ViewCompat.setOnApplyWindowInsetsListener(binding.mainContentLayout) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 24 + bars.top,
                right = 24 + bars.right,
                bottom = 112 + bars.bottom,
            )
            insets
        }

        binding.refreshButton.setOnClickListener { refreshRuntimes() }
        binding.accessToggleButton.setOnClickListener {
            startActivity(AccountIdentityActivity.createIntent(this))
        }
        binding.createRuntimeButton.setOnClickListener {
            startActivity(NewRuntimeActivity.createIntent(this))
        }
        binding.runtimesNavButton.setOnClickListener { refreshRuntimes() }
        binding.securityNavButton.setOnClickListener {
            startActivity(AccountIdentityActivity.createIntent(this))
        }
        binding.logsNavButton.setOnClickListener {
            toast(getString(R.string.bottom_logs_runtime_hint))
        }
        binding.settingsNavButton.setOnClickListener {
            startActivity(FundStorageActivity.createIntent(this))
        }

        refreshRuntimes()
    }

    override fun onResume() {
        super.onResume()
        refreshRuntimes()
    }

    private fun refreshRuntimes() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            startActivity(Intent(this, OnboardingActivity::class.java))
            finish()
            return
        }

        val baseUrl = currentBaseUrl()
        setBusy(true, getString(R.string.status_refreshing))

        lifecycleScope.launch {
            runCatching {
                api.listRuntimes(baseUrl, accountId, deviceId)
            }.onSuccess { runtimes ->
                renderRuntimes(runtimes)
                setBusy(false)
            }.onFailure { error ->
                setBusy(false)
                showError(error)
            }
        }
    }

    private fun renderRuntimes(runtimes: List<RuntimeSummary>) {
        binding.runtimeListContainer.removeAllViews()
        binding.runtimeEmptyText.isVisible = runtimes.isEmpty()
        if (runtimes.isEmpty()) {
            return
        }

        val inflater = LayoutInflater.from(this)
        runtimes.forEach { runtime ->
            val cardBinding = RuntimeCardBinding.inflate(inflater, binding.runtimeListContainer, false)
            bindRuntimeCard(cardBinding, runtime)
            binding.runtimeListContainer.addView(cardBinding.root)
        }
    }

    private fun bindRuntimeCard(cardBinding: RuntimeCardBinding, runtime: RuntimeSummary) {
        val isLive = runtime.isReadyForSession()
        val hasData = runtime.blobLastSnapshotAt != null

        cardBinding.runtimeTitleText.text = runtime.name
        cardBinding.runtimeSubtitleText.text = when {
            isLive -> getString(R.string.runtime_state_active)
            runtime.status.equals("starting", ignoreCase = true) -> getString(R.string.runtime_state_provisioning)
            hasData -> getString(R.string.runtime_state_offline_saved)
            else -> getString(R.string.runtime_state_offline_wiped)
        }
        cardBinding.runtimeStatusDot.setBackgroundResource(
            if (isLive) R.drawable.bg_dot_accent else R.drawable.bg_dot_muted,
        )
        cardBinding.runtimeIcon.setColorFilter(
            getColor(if (isLive) R.color.v_accent else R.color.v_text_muted),
        )
        cardBinding.root.strokeColor = getColor(if (isLive) R.color.v_border else R.color.v_border)

        cardBinding.runtimeStatOneValue.text = if (isLive) {
            getString(R.string.runtime_stat_live)
        } else if (hasData) {
            getString(R.string.runtime_stat_saved)
        } else {
            formatTimestamp(runtime.blobLastSnapshotAt)
        }
        cardBinding.runtimeStatTwoValue.text = getString(R.string.runtime_stat_load_unknown)
        cardBinding.runtimeStatThreeValue.text = if (isLive) {
            getString(R.string.runtime_stat_ping_live)
        } else {
            getString(R.string.runtime_stat_ping_offline)
        }

        cardBinding.runtimeErrorText.isVisible = !runtime.lastError.isNullOrBlank()
        cardBinding.runtimeErrorText.text = runtime.lastError.orEmpty()
        cardBinding.connectRuntimeButton.isVisible = isLive
        cardBinding.runtimeActionRow.isVisible = !isLive

        cardBinding.connectRuntimeButton.setOnClickListener {
            connectRuntime(runtime)
        }
        cardBinding.startRuntimeButton.setOnClickListener {
            mutateRuntime(getString(R.string.status_starting_runtime)) {
                val accountId = requireAccountId() ?: return@mutateRuntime
                val deviceId = requireDeviceId() ?: return@mutateRuntime
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                api.startRuntime(currentBaseUrl(), accountId, deviceId, runtime.id, blobAccessKey)
            }
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
    }

    private fun connectRuntime(runtime: RuntimeSummary) {
        val accountId = requireAccountId() ?: return
        val deviceId = requireDeviceId() ?: return
        val baseUrl = currentBaseUrl()
        setBusy(true, getString(R.string.status_preparing_session))

        lifecycleScope.launch {
            runCatching {
                val readyRuntime = ensureRuntimeReady(baseUrl, accountId, deviceId, runtime)
                val session = api.createSession(
                    baseUrl = baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    runtimeId = readyRuntime.id,
                    maxSize = sessionMaxSize(),
                    bitRate = DEFAULT_SESSION_BIT_RATE,
                )
                Pair(session, readyRuntime)
            }.onSuccess { (session, readyRuntime) ->
                setBusy(false)
                startActivity(
                    SessionActivity.createIntent(
                        context = this@MainActivity,
                        accountId = accountId,
                        deviceId = deviceId,
                        baseUrl = baseUrl,
                        runtimeId = readyRuntime.id,
                        runtimeName = readyRuntime.name,
                        relayHost = session.relayHost,
                        relayPort = session.relayPort,
                        relayTls = session.relayTls,
                        relayPath = session.relayPath,
                        relayToken = session.relayToken,
                        sessionId = session.sessionId,
                        viewerAddress = session.viewerAddress,
                    ),
                )
            }.onFailure { error ->
                setBusy(false)
                showError(error)
            }
        }
    }

    private suspend fun ensureRuntimeReady(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtime: RuntimeSummary,
    ): RuntimeSummary {
        if (runtime.isReadyForSession()) {
            return runtime
        }

        setBusy(true, getString(R.string.status_starting_runtime_for_session))
        val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
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

    private fun sessionMaxSize(): Int {
        val metrics = resources.displayMetrics
        return max(metrics.widthPixels, metrics.heightPixels).coerceIn(720, 1600)
    }

    private fun setBusy(isBusy: Boolean, message: String? = null) {
        binding.progressIndicator.isVisible = isBusy
        binding.statusText.text = message ?: getString(R.string.home_secure_client)
        binding.refreshButton.isEnabled = !isBusy
        binding.createRuntimeButton.isEnabled = !isBusy
        binding.accessToggleButton.isEnabled = !isBusy
    }

    private fun formatTimestamp(value: String?): String {
        if (value.isNullOrBlank()) {
            return getString(R.string.runtime_stat_ping_offline)
        }

        return runCatching {
            OffsetDateTime.parse(value).format(timestampFormatter)
        }.getOrDefault(value)
    }

    private fun showError(error: Throwable) {
        toast(error.message ?: getString(R.string.status_error))
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    private fun RuntimeSummary.isReadyForSession(): Boolean {
        return status.equals("running", ignoreCase = true) &&
            connectionStatus.equals("online", ignoreCase = true) &&
            !hostId.isNullOrBlank()
    }

	private companion object {
		val DEFAULT_CONTROL_PLANE_URL = BuildConfig.DEFAULT_CONTROL_PLANE_URL
		const val DEFAULT_SESSION_BIT_RATE = 4_000_000
		const val CONNECT_WAIT_ATTEMPTS = 45
        const val CONNECT_WAIT_DELAY_MS = 1_000L
    }
}
