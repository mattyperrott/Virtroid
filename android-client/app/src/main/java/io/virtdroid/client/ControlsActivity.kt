package io.virtdroid.client

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
import io.virtdroid.client.api.RuntimeLogEntry
import io.virtdroid.client.api.RuntimeSummary
import io.virtdroid.client.api.RuntimeUpdate
import io.virtdroid.client.api.VirtdroidApi
import io.virtdroid.client.data.ActiveSessionStore
import io.virtdroid.client.data.AppLogStore
import io.virtdroid.client.data.SessionStore
import io.virtdroid.client.databinding.ScreenSessionControlsBinding
import io.virtdroid.client.security.IdentityPasswordStore
import io.virtdroid.client.security.enableSecureWindow
import io.virtdroid.client.security.promptIdentityPassword
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.io.IOException
import kotlin.math.max

class ControlsActivity : AppCompatActivity() {
    private lateinit var binding: ScreenSessionControlsBinding
    private val api = VirtdroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var activeSessionStore: ActiveSessionStore
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private lateinit var appLogs: AppLogStore
    private var runtimeId: String = ""
    private var runtime: RuntimeSummary? = null
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

    private fun loadRuntime() {
        val accountId = sessionStore.accountId ?: return
        val deviceId = sessionStore.deviceId ?: return
        lifecycleScope.launch {
            runCatching {
                api.listRuntimes(sessionStore.baseUrl, accountId, deviceId)
                    .firstOrNull { it.id == runtimeId }
                    ?: throw IOException(getString(R.string.runtime_missing_for_session))
            }.onSuccess { loaded ->
                runtime = loaded
                bindRuntime(loaded)
            }.onFailure {
                toast(it.message ?: getString(R.string.status_error))
                finish()
            }
        }
    }

    private fun bindRuntime(runtime: RuntimeSummary) {
        binding.controlsRuntimeNameText.text = runtime.name
        binding.controlsRuntimeSubtitleText.text = getString(R.string.controls_runtime_subtitle, runtime.id)
        binding.controlsStateValue.text = if (runtime.isReadyForSession()) {
            getString(R.string.controls_state_running)
        } else {
            getString(R.string.controls_state_stopped)
        }
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
            setTextColor(ContextCompat.getColor(context, R.color.virtdroid_on_surface))
            setHintTextColor(ContextCompat.getColor(context, R.color.virtdroid_on_surface_muted))
        }
        val heightInput = EditText(this).apply {
            hint = "Height"
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(current.heightPx.toString())
            setTextColor(ContextCompat.getColor(context, R.color.virtdroid_on_surface))
            setHintTextColor(ContextCompat.getColor(context, R.color.virtdroid_on_surface_muted))
        }
        val dpiInput = EditText(this).apply {
            hint = "DPI"
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(current.densityDpi.toString())
            setTextColor(ContextCompat.getColor(context, R.color.virtdroid_on_surface))
            setHintTextColor(ContextCompat.getColor(context, R.color.virtdroid_on_surface_muted))
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
                bindRuntime(it)
                toast(getString(R.string.runtime_saved))
            }.onFailure {
                toast(it.message ?: getString(R.string.status_error))
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
                toast(it.message ?: getString(R.string.status_error))
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
                        api.wipeRuntime(sessionStore.baseUrl, accountId, deviceId, current.id, blobAccessKey)
                    }.onSuccess {
                        runtime = it
                        bindRuntime(it)
                        toast(getString(R.string.status_wiping_runtime))
                    }.onFailure {
                        toast(it.message ?: getString(R.string.status_error))
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
        if (current.isReadyForSession()) {
            connectRuntime(current)
            return
        }

        lifecycleScope.launch {
            runCatching {
                val accountId = sessionStore.accountId ?: throw IOException(getString(R.string.account_missing))
                val deviceId = sessionStore.deviceId ?: throw IOException(getString(R.string.device_missing))
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                api.startRuntime(sessionStore.baseUrl, accountId, deviceId, current.id, blobAccessKey)
                waitForRuntimeReady(accountId, deviceId, current.id)
            }.onSuccess {
                runtime = it
                bindRuntime(it)
                connectRuntime(it)
            }.onFailure {
                toast(it.message ?: getString(R.string.status_error))
            }
        }
    }

    private suspend fun waitForRuntimeReady(accountId: String, deviceId: String, runtimeId: String): RuntimeSummary {
        repeat(CONNECT_WAIT_ATTEMPTS) { attempt ->
            val loaded = api.listRuntimes(sessionStore.baseUrl, accountId, deviceId)
                .firstOrNull { it.id == runtimeId }
                ?: throw IOException(getString(R.string.runtime_missing_for_session))
            if (loaded.isReadyForSession()) {
                return loaded
            }
            if (attempt < CONNECT_WAIT_ATTEMPTS - 1) {
                delay(CONNECT_WAIT_DELAY_MS)
            }
        }
        throw IOException(getString(R.string.runtime_start_timeout))
    }

    private fun connectRuntime(runtime: RuntimeSummary) {
        activeSessionStore.loadForRuntime(runtime.id)?.let { storedSession ->
            lifecycleScope.launch {
                runCatching {
                    api.heartbeatSession(
                        baseUrl = storedSession.baseUrl,
                        accountId = storedSession.accountId,
                        deviceId = storedSession.deviceId,
                        sessionId = storedSession.sessionId,
                    )
                }.onSuccess {
                    activeSessionStore.touch(storedSession.sessionId)
                    appLogs.info("Returning to active session from controls for ${runtime.name}", "session")
                    startActivity(SessionActivity.createIntent(this@ControlsActivity, storedSession).addFlags(Intent.FLAG_ACTIVITY_REORDER_TO_FRONT))
                }.onFailure { error ->
                    activeSessionStore.clear()
                    appLogs.warn("Stored active session from controls was stale: ${error.message}", "session")
                    connectRuntime(runtime)
                }
            }
            return
        }

        val accountId = sessionStore.accountId ?: return
        val deviceId = sessionStore.deviceId ?: return
        lifecycleScope.launch {
            runCatching {
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                api.createSession(
                    baseUrl = sessionStore.baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    runtimeId = runtime.id,
                    maxSize = sessionMaxSize(),
                    bitRate = DEFAULT_SESSION_BIT_RATE,
                    blobAccessKey = blobAccessKey,
                )
            }.onSuccess { session ->
                val activeSession = ActiveSessionStore.ActiveSession(
                    accountId = accountId,
                    deviceId = deviceId,
                    baseUrl = sessionStore.baseUrl,
                    runtimeId = runtime.id,
                    runtimeName = runtime.name,
                    viewerAddress = session.viewerAddress,
                    relayHost = session.relayHost,
                    relayPort = session.relayPort,
                    relayTls = session.relayTls,
                    relayPath = session.relayPath,
                    relayToken = session.relayToken,
                    sessionId = session.sessionId,
                )
                activeSessionStore.save(activeSession)
                appLogs.info("Session created from controls for ${runtime.name}", "session")
                startActivity(
                    SessionActivity.createIntent(this@ControlsActivity, activeSession),
                )
            }.onFailure {
                appLogs.error(it.message ?: getString(R.string.status_error), "session")
                toast(it.message ?: getString(R.string.status_error))
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
                        api.deleteRuntime(sessionStore.baseUrl, accountId, deviceId, current.id)
                    }.onSuccess {
                        toast(getString(R.string.runtime_deleted))
                        finish()
                    }.onFailure {
                        toast(it.message ?: getString(R.string.status_error))
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
            connectionStatus.equals("online", ignoreCase = true) &&
            !hostId.isNullOrBlank()
    }

    companion object {
        private const val EXTRA_RUNTIME_ID = "extra_runtime_id"
        private const val DEFAULT_SESSION_BIT_RATE = 4_000_000
        private const val CONNECT_WAIT_ATTEMPTS = 45
        private const val CONNECT_WAIT_DELAY_MS = 1_000L

        fun createIntent(context: Context, runtimeId: String): Intent =
            Intent(context, ControlsActivity::class.java)
                .putExtra(EXTRA_RUNTIME_ID, runtimeId)
    }
}
