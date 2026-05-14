package io.virtroid.client

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Rect
import android.graphics.SurfaceTexture
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.Surface
import android.view.TextureView
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.Toast
import androidx.activity.addCallback
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.databinding.ScreenSessionViewerBinding
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPassword
import io.virtroid.client.viewer.ScrcpySessionHost
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.IOException
import java.time.LocalTime
import java.time.format.DateTimeFormatter
import java.util.Locale

class SessionActivity : AppCompatActivity() {
    private lateinit var binding: ScreenSessionViewerBinding
    private var sessionHost: ScrcpySessionHost? = null
    private val api = VirtroidApi()
    private var viewerAddress: String = ""
    private var relayHost: String = ""
    private var relayPort: Int = 0
    private var relayTls: Boolean = false
    private var relayPath: String = ""
    private var relayToken: String = ""
    private var viewerPublicKey: String = ""
    private var sessionId: String = ""
    private var runtimeName: String = ""
    private var runtimeId: String = ""
    private var accountId: String = ""
    private var deviceId: String = ""
    private var baseUrl: String = ""
    private var endingSession = false
    private var viewerSurface: Surface? = null
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private lateinit var activeSessionStore: ActiveSessionStore
    private lateinit var appSettings: AppSettingsStore
    private lateinit var appLogs: AppLogStore
    private var lastInteractionAtMs = System.currentTimeMillis()
    private var inactivityJob: Job? = null
    private var heartbeatJob: Job? = null
    private var sessionUnavailable = false
    private var heartbeatFailureCount = 0

    private val sessionCallback = object : ScrcpySessionHost.Callback {
        override fun onConnected(remoteWidth: Int, remoteHeight: Int) {
            runOnUiThread {
                appLogs.info("Session stream connected for $runtimeName", "session")
                binding.sessionSubtitleText.text = getString(
                    R.string.session_secure_online_with_resolution,
                    remoteWidth,
                    remoteHeight,
                )
                binding.sessionStreamStatusText.text = getString(R.string.session_stream_receiving)
                binding.sessionRetryButton.isVisible = false
                applyRemoteSurfaceBounds(remoteWidth, remoteHeight)
                updateViewerGestureExclusion()
                hideSystemBars()
            }
        }

        override fun onFirstVideoFrame() {
            runOnUiThread {
                binding.sessionStreamStatusOverlay.isVisible = false
            }
        }

        override fun onDisconnected(message: String) {
            runOnUiThread {
                binding.sessionSubtitleText.text = if (endingSession) {
                    getString(R.string.session_secure_saving)
                } else if (sessionUnavailable) {
                    getString(R.string.session_heartbeat_stale)
                } else {
                    getString(R.string.session_failed_message_inline, message)
                }
                if (!endingSession) {
                    appLogs.error("Session stream disconnected: $message", "session")
                    binding.sessionStreamStatusText.text = if (sessionUnavailable) {
                        getString(R.string.session_heartbeat_stale)
                    } else {
                        message
                    }
                    binding.sessionStreamProgress.isVisible = false
                    binding.sessionStreamStatusOverlay.isVisible = true
                    binding.sessionRetryButton.isVisible = !sessionUnavailable
                    val failedHost = sessionHost
                    sessionHost = null
                    failedHost?.destroy()
                    toast(getString(R.string.session_failed))
                    checkSessionAfterStreamDisconnect(message)
                }
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenSessionViewerBinding.inflate(layoutInflater)
        setContentView(binding.root)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        viewerAddress = intent.getStringExtra(EXTRA_VIEWER_ADDRESS).orEmpty()
        relayHost = intent.getStringExtra(EXTRA_RELAY_HOST).orEmpty()
        relayPort = intent.getIntExtra(EXTRA_RELAY_PORT, 0)
        relayTls = intent.getBooleanExtra(EXTRA_RELAY_TLS, false)
        relayPath = intent.getStringExtra(EXTRA_RELAY_PATH).orEmpty()
        relayToken = intent.getStringExtra(EXTRA_RELAY_TOKEN).orEmpty()
        viewerPublicKey = intent.getStringExtra(EXTRA_VIEWER_PUBLIC_KEY).orEmpty()
        sessionId = intent.getStringExtra(EXTRA_SESSION_ID).orEmpty()
        runtimeName = intent.getStringExtra(EXTRA_RUNTIME_NAME).orEmpty()
        runtimeId = intent.getStringExtra(EXTRA_RUNTIME_ID).orEmpty()
        accountId = intent.getStringExtra(EXTRA_ACCOUNT_ID).orEmpty()
        deviceId = intent.getStringExtra(EXTRA_DEVICE_ID).orEmpty()
        baseUrl = intent.getStringExtra(EXTRA_BASE_URL).orEmpty()
        identityPasswordStore = IdentityPasswordStore(this)
        activeSessionStore = ActiveSessionStore(this)
        appSettings = AppSettingsStore(this)
        appLogs = AppLogStore.get(this)
        maybeRequestSessionNotificationPermission()
        persistActiveSession()

        if (relayHost.isBlank() || relayPort <= 0 || !relayTls || relayPath.isBlank() || relayToken.isBlank() || viewerPublicKey.isBlank()) {
            toast(getString(R.string.session_missing_endpoint))
            finish()
            return
        }

        val title = runtimeName.ifBlank { getString(R.string.session_title_fallback) }
        binding.sessionTitleText.text = title
        binding.sessionSubtitleText.text = getString(R.string.session_connecting_short)
        binding.sessionBackToAppButton.setOnClickListener { navigateBackToApp() }
        binding.sessionDisconnectButton.setOnClickListener { endSessionAndFinish() }
        binding.sessionControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtimeId))
        }
        binding.sessionRetryButton.setOnClickListener {
            retryViewerConnection()
        }
        binding.sessionUploadButton.isVisible = false
        binding.sessionCameraButton.isVisible = false
        binding.sessionOptionalActionsDivider.isVisible = false
        binding.sessionRetryButton.isVisible = false
        binding.sessionStreamStatusText.text = getString(R.string.session_stream_connecting)
        binding.sessionHeartbeatText.text = getString(R.string.session_heartbeat_pending)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionSurfaceView.isOpaque = true
        binding.sessionSurfaceView.setOnTouchListener { view, event ->
            sessionHost?.sendTouch(event, view.width, view.height) ?: false
        }
        binding.sessionSurfaceContainer.addOnLayoutChangeListener { _, _, _, _, _, _, _, _, _ ->
            updateViewerGestureExclusion()
        }
        ViewCompat.setOnApplyWindowInsetsListener(binding.sessionRoot) { _, insets ->
            val systemBars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            binding.sessionTopBar.updatePadding(
                left = 14 + systemBars.left,
                top = 10 + systemBars.top,
                right = 14 + systemBars.right,
                bottom = 10,
            )
            insets
        }
        binding.sessionSurfaceView.surfaceTextureListener = object : TextureView.SurfaceTextureListener {
            override fun onSurfaceTextureAvailable(surfaceTexture: SurfaceTexture, width: Int, height: Int) {
                val surface = Surface(surfaceTexture)
                viewerSurface = surface
                attachOrConnectViewer(surface, width, height)
            }

            override fun onSurfaceTextureSizeChanged(
                surfaceTexture: SurfaceTexture,
                width: Int,
                height: Int,
            ) = Unit

            override fun onSurfaceTextureDestroyed(surfaceTexture: SurfaceTexture): Boolean {
                if (endingSession || isFinishing) {
                    disconnectViewer()
                } else {
                    sessionHost?.pauseRendering()
                }
                viewerSurface?.release()
                viewerSurface = null
                return true
            }

            override fun onSurfaceTextureUpdated(surfaceTexture: SurfaceTexture) = Unit
        }
        if (binding.sessionSurfaceView.isAvailable) {
            val surface = Surface(binding.sessionSurfaceView.surfaceTexture)
            viewerSurface = surface
            attachOrConnectViewer(
                surface,
                binding.sessionSurfaceView.width,
                binding.sessionSurfaceView.height,
            )
        }
        onBackPressedDispatcher.addCallback(this) {
            navigateBackToApp()
        }
        startSessionHeartbeat()
        startInactivityWatch()
    }

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        markInteraction()
        if (event.action == KeyEvent.ACTION_DOWN &&
            event.keyCode != KeyEvent.KEYCODE_BACK &&
            event.keyCode != KeyEvent.KEYCODE_VOLUME_DOWN &&
            event.keyCode != KeyEvent.KEYCODE_VOLUME_UP
        ) {
            sessionHost?.sendKey(event.keyCode)
            return true
        }
        return super.dispatchKeyEvent(event)
    }

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        markInteraction()
        return super.dispatchTouchEvent(ev)
    }

    override fun onStop() {
        persistActiveSession()
        super.onStop()
    }

    override fun onDestroy() {
        inactivityJob?.cancel()
        heartbeatJob?.cancel()
        disconnectViewer()
        super.onDestroy()
    }

    override fun onResume() {
        super.onResume()
        hideSystemBars()
    }

    private fun attachOrConnectViewer(surface: Surface, width: Int, height: Int) {
        val host = sessionHost
        if (host == null) {
            connectViewer(surface)
            return
        }

        binding.sessionSubtitleText.text = getString(R.string.session_connecting_short)
        binding.sessionStreamStatusText.text = getString(R.string.session_stream_resuming)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionRetryButton.isVisible = false
        host.attachSurface(
            surface,
            width.takeIf { it > 0 } ?: resources.displayMetrics.widthPixels,
            height.takeIf { it > 0 } ?: resources.displayMetrics.heightPixels,
        )
    }

    private fun connectViewer(surface: Surface) {
        if (sessionHost != null) {
            return
        }

        binding.sessionSubtitleText.text = getString(R.string.session_connecting_short)
        binding.sessionStreamStatusText.text = getString(R.string.session_stream_connecting)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionRetryButton.isVisible = false
        appLogs.info("Opening viewer transport for $runtimeName", "session")
        sessionHost = ScrcpySessionHost(
            context = this,
            relayHost = relayHost,
            relayPort = relayPort,
            relayTls = relayTls,
            relayPath = relayPath,
            relayToken = relayToken,
            viewerPublicKey = viewerPublicKey,
            surface = surface,
            displayWidth = binding.sessionSurfaceView.width.takeIf { it > 0 }
                ?: resources.displayMetrics.widthPixels,
            displayHeight = binding.sessionSurfaceView.height.takeIf { it > 0 }
                ?: resources.displayMetrics.heightPixels,
            callback = sessionCallback,
        ).also { it.connect() }
    }

    private fun disconnectViewer() {
        sessionHost?.destroy()
        sessionHost = null
    }

    private fun retryViewerConnection() {
        if (sessionUnavailable) {
            toast(getString(R.string.session_heartbeat_stale))
            return
        }
        val surface = viewerSurface ?: return
        appLogs.info("Retrying viewer transport for $runtimeName", "session")
        disconnectViewer()
        binding.sessionSubtitleText.text = getString(R.string.session_connecting_short)
        binding.sessionStreamStatusText.text = getString(R.string.session_stream_reconnecting)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionRetryButton.isVisible = false
        connectViewer(surface)
    }

    private fun checkSessionAfterStreamDisconnect(message: String) {
        if (sessionUnavailable ||
            sessionId.isBlank() ||
            baseUrl.isBlank() ||
            accountId.isBlank() ||
            deviceId.isBlank()
        ) {
            return
        }

        lifecycleScope.launch {
            runCatching {
                api.getSessionState(baseUrl, accountId, deviceId, sessionId)
            }.onSuccess {
                if (it.canResumeRuntime(runtimeId)) {
                    heartbeatFailureCount = 0
                    activeSessionStore.touch(sessionId)
                    markHeartbeatHealthy()
                } else {
                    appLogs.warn("Session stream disconnected and backend state is ${it.effectiveStatus}", "session")
                    activeSessionStore.clear()
                    relayToken = ""
                    markSessionUnavailable()
                    disconnectViewer()
                }
            }.onFailure { error ->
                if (error.isGoneSessionResponse()) {
                    appLogs.warn("Session stream disconnected and backend session is gone: ${error.message}", "session")
                    activeSessionStore.clear()
                    relayToken = ""
                    markSessionUnavailable()
                    disconnectViewer()
                } else {
                    appLogs.warn("Session stream disconnected; backend session check will retry: $message / ${error.message}", "session")
                }
            }
        }
    }

    private fun endSessionAndFinish() {
        endSessionAndFinish(AppSettingsStore.SESSION_END_USER)
    }

    private fun endSessionAndFinish(reason: String) {
        if (endingSession) {
            return
        }

        endingSession = true
        disconnectViewer()
        appLogs.info("Session shutdown requested: $reason", "session")

        binding.sessionSubtitleText.text = getString(R.string.session_secure_saving)
        binding.sessionStreamStatusText.text = getString(R.string.session_shutdown_status)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionRetryButton.isVisible = false
        setSessionActionsEnabled(false)

        if (runtimeId.isBlank() || accountId.isBlank() || deviceId.isBlank() || baseUrl.isBlank()) {
            appSettings.lastSessionEndReason = reason
            activeSessionStore.clear()
            finishToRuntimeList(markRuntimeStopping = false)
            return
        }

        lifecycleScope.launch {
            runCatching {
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                if (sessionId.isNotBlank()) {
                    val endResult = runCatching {
                        api.endSession(baseUrl, accountId, deviceId, runtimeId, sessionId, blobAccessKey)
                    }
                    endResult.onFailure { error ->
                        if (!error.isGoneSessionResponse()) {
                            throw error
                        }
                        api.stopRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey)
                    }
                    relayToken = ""
                } else {
                    api.stopRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey)
                }
                identityPasswordStore.saveConfigured(accountId, deviceId)
                waitForRuntimeStopped()
            }.onSuccess {
                appSettings.lastSessionEndReason = reason
                activeSessionStore.clear()
                if (appSettings.autoClearClipboard) {
                    appSettings.clearClipboard()
                    appLogs.info("Clipboard cleared after session end", "privacy")
                }
                appLogs.info("Session ended: $reason", "session")
                toast(getString(R.string.session_ended))
                finishToRuntimeList(markRuntimeStopping = true)
            }.onFailure { error ->
                if (error is StopTimeoutException) {
                    appSettings.lastSessionEndReason = reason
                    activeSessionStore.clear()
                    appLogs.warn("Session shutdown still pending after timeout", "session")
                    toast(getString(R.string.session_stop_pending))
                    finishToRuntimeList(markRuntimeStopping = true)
                } else {
                    endingSession = false
                    setSessionActionsEnabled(true)
                    showEndingError(error)
                }
            }
        }
    }

    private fun finishToRuntimeList(markRuntimeStopping: Boolean) {
        val intent = if (markRuntimeStopping && runtimeId.isNotBlank()) {
            MainActivity.createRuntimeStoppingIntent(this, runtimeId)
        } else {
            Intent(this, MainActivity::class.java)
        }
        startActivity(
            intent.addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP),
        )
        finish()
    }

    private fun navigateBackToApp() {
        persistActiveSession()
        appLogs.info("Session moved to background without shutdown", "session")
        startActivity(
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_REORDER_TO_FRONT),
        )
    }

    private fun sendSystemKey(keyCode: Int) {
        sessionHost?.sendKey(keyCode)
    }

    private suspend fun waitForRuntimeStopped() {
        repeat(STOP_WAIT_ATTEMPTS) { attempt ->
            val state = runCatching {
                api.getRuntimeState(baseUrl, accountId, deviceId, runtimeId)
            }.getOrElse { error ->
                if (error.isGoneSessionResponse()) {
                    return
                }
                throw error
            }
            val runtime = state.runtime

            if (runtime.isStoppedForSession() || state.effectiveState.equals("deleted", ignoreCase = true)) {
                return
            }

            val status = getString(
                R.string.status_waiting_runtime_stop,
                state.effectiveState.ifBlank { runtime.status.ifBlank { "stopping" } },
                runtime.connectionStatus.ifBlank { "offline" },
            )
            binding.sessionSubtitleText.text = status
            binding.sessionStreamStatusText.text = status

            if (attempt < STOP_WAIT_ATTEMPTS - 1) {
                delay(STOP_WAIT_DELAY_MS)
            }
        }

        throw StopTimeoutException()
    }

    private fun applyRemoteSurfaceBounds(remoteWidth: Int, remoteHeight: Int) {
        val container = binding.sessionSurfaceContainer
        if (container.width == 0 || container.height == 0) {
            container.post { applyRemoteSurfaceBounds(remoteWidth, remoteHeight) }
            return
        }

        if (remoteWidth <= 0 || remoteHeight <= 0) {
            return
        }

        val containerWidth = container.width
        val containerHeight = container.height
        val remoteAspect = remoteWidth.toFloat() / remoteHeight.toFloat()
        val containerAspect = containerWidth.toFloat() / containerHeight.toFloat()

        val targetWidth: Int
        val targetHeight: Int
        if (remoteAspect > containerAspect) {
            targetWidth = containerWidth
            targetHeight = (containerWidth / remoteAspect).toInt()
        } else {
            targetHeight = containerHeight
            targetWidth = (containerHeight * remoteAspect).toInt()
        }

        val params = FrameLayout.LayoutParams(
            targetWidth.coerceAtLeast(1),
            targetHeight.coerceAtLeast(1),
            Gravity.CENTER,
        )
        binding.sessionSurfaceView.layoutParams = params
        updateViewerGestureExclusion()
    }

    private fun updateViewerGestureExclusion() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            return
        }
        val container = binding.sessionSurfaceContainer
        if (container.width <= 0 || container.height <= 0) {
            return
        }
        val rect = Rect(0, 0, container.width, container.height)
        ViewCompat.setSystemGestureExclusionRects(container, listOf(rect))
    }

    private fun hideSystemBars() {
        val controller = WindowInsetsControllerCompat(window, binding.sessionRoot)
        controller.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        controller.hide(WindowInsetsCompat.Type.systemBars())
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    private fun maybeRequestSessionNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) {
            return
        }
        if (checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) {
            return
        }
        requestPermissions(
            arrayOf(Manifest.permission.POST_NOTIFICATIONS),
            REQUEST_SESSION_NOTIFICATION_PERMISSION,
        )
    }

    private fun showEndingError(error: Throwable) {
        val message = error.message ?: getString(R.string.status_error)
        binding.sessionSubtitleText.text = message
        toast(message)
    }

    private fun setSessionActionsEnabled(isEnabled: Boolean) {
        binding.sessionBackToAppButton.isEnabled = isEnabled
        binding.sessionDisconnectButton.isEnabled = isEnabled
        binding.sessionControlsButton.isEnabled = isEnabled
        binding.sessionUploadButton.isEnabled = isEnabled
        binding.sessionCameraButton.isEnabled = isEnabled
    }

    private fun persistActiveSession() {
        if (runtimeId.isBlank() || relayToken.isBlank()) {
            return
        }
        activeSessionStore.save(
            ActiveSessionStore.ActiveSession(
                accountId = accountId,
                deviceId = deviceId,
                baseUrl = baseUrl,
                runtimeId = runtimeId,
                runtimeName = runtimeName,
                viewerAddress = viewerAddress,
                relayHost = relayHost,
                relayPort = relayPort,
                relayTls = relayTls,
                relayPath = relayPath,
                relayToken = relayToken,
                sessionId = sessionId,
                viewerPublicKey = viewerPublicKey,
            ),
        )
    }

    private fun markHeartbeatPending() {
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_amber)
        binding.sessionHeartbeatText.text = getString(R.string.session_heartbeat_pending)
    }

    private fun markHeartbeatHealthy() {
        heartbeatFailureCount = 0
        val heartbeatAt = LocalTime.now().format(HEARTBEAT_TIME_FORMAT)
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_accent)
        binding.sessionHeartbeatText.text = getString(R.string.session_heartbeat_ok, heartbeatAt)
    }

    private fun markHeartbeatRetrying() {
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_amber)
        binding.sessionHeartbeatText.text = getString(R.string.session_heartbeat_retrying)
    }

    private fun markSessionUnavailable() {
        sessionUnavailable = true
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_muted)
        binding.sessionHeartbeatText.text = getString(R.string.session_heartbeat_stale)
        binding.sessionSubtitleText.text = getString(R.string.session_heartbeat_stale)
        binding.sessionStreamStatusText.text = getString(R.string.session_heartbeat_stale)
        binding.sessionStreamProgress.isVisible = false
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionRetryButton.isVisible = false
    }

    private fun startSessionHeartbeat() {
        heartbeatJob?.cancel()
        if (baseUrl.isBlank() || accountId.isBlank() || deviceId.isBlank() || sessionId.isBlank()) {
            return
        }
        markHeartbeatPending()
        heartbeatJob = lifecycleScope.launch {
            while (isActive && !endingSession) {
                runCatching {
                    api.heartbeatSession(baseUrl, accountId, deviceId, sessionId)
                }.onSuccess {
                    heartbeatFailureCount = 0
                    activeSessionStore.touch(sessionId)
                    markHeartbeatHealthy()
                }.onFailure { error ->
                    if (error.isGoneSessionResponse()) {
                        appLogs.warn("Active session heartbeat returned a stale session: ${error.message}", "session")
                        activeSessionStore.clear()
                        relayToken = ""
                        markSessionUnavailable()
                        disconnectViewer()
                        return@launch
                    }
                    heartbeatFailureCount += 1
                    appLogs.warn("Active session heartbeat failed; keeping session handle for retry: ${error.message}", "session")
                    if (heartbeatFailureCount >= HEARTBEAT_RETRY_VISIBLE_THRESHOLD) {
                        markHeartbeatRetrying()
                    }
                }
                delay(SESSION_HEARTBEAT_INTERVAL_MS)
            }
        }
    }

    private fun markInteraction() {
        lastInteractionAtMs = System.currentTimeMillis()
    }

    private fun startInactivityWatch() {
        inactivityJob?.cancel()
        inactivityJob = lifecycleScope.launch {
            while (!endingSession) {
                delay(5_000L)
                val timeoutMs = appSettings.uiInactivityTimeoutMs
                if (System.currentTimeMillis() - lastInteractionAtMs >= timeoutMs) {
                    appLogs.warn("Session inactivity timeout reached", "session")
                    endSessionAndFinish(AppSettingsStore.SESSION_END_INACTIVITY)
                    return@launch
                }
            }
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

    private fun io.virtroid.client.api.RuntimeSummary.isStoppedForSession(): Boolean {
        val stopped = status.equals("stopped", ignoreCase = true)
        val desiredStopped = desiredState.equals("stopped", ignoreCase = true)
        val offline = connectionStatus.isBlank() ||
            connectionStatus.equals("offline", ignoreCase = true) ||
            connectionStatus.equals("disconnected", ignoreCase = true)
        return stopped && desiredStopped && offline
    }

    private class StopTimeoutException : IOException()

    companion object {
        private const val EXTRA_RUNTIME_NAME = "runtime_name"
        private const val EXTRA_VIEWER_ADDRESS = "viewer_address"
        private const val EXTRA_RELAY_HOST = "relay_host"
        private const val EXTRA_RELAY_PORT = "relay_port"
        private const val EXTRA_RELAY_TLS = "relay_tls"
        private const val EXTRA_RELAY_PATH = "relay_path"
        private const val EXTRA_RELAY_TOKEN = "relay_token"
        private const val EXTRA_VIEWER_PUBLIC_KEY = "viewer_public_key"
        private const val EXTRA_SESSION_ID = "session_id"
        private const val EXTRA_RUNTIME_ID = "runtime_id"
        private const val EXTRA_ACCOUNT_ID = "account_id"
        private const val EXTRA_DEVICE_ID = "device_id"
        private const val EXTRA_BASE_URL = "base_url"
        private const val STOP_WAIT_ATTEMPTS = 30
        private const val STOP_WAIT_DELAY_MS = 1_000L
        private const val SESSION_HEARTBEAT_INTERVAL_MS = 20_000L
        private const val HEARTBEAT_RETRY_VISIBLE_THRESHOLD = 2
        private const val REQUEST_SESSION_NOTIFICATION_PERMISSION = 4182
        private val HEARTBEAT_TIME_FORMAT = DateTimeFormatter.ofPattern("HH:mm:ss", Locale.US)

        fun createIntent(
            context: Context,
            accountId: String,
            deviceId: String,
            baseUrl: String,
            runtimeId: String,
            runtimeName: String,
            relayHost: String,
            relayPort: Int,
            relayTls: Boolean,
            relayPath: String,
            relayToken: String,
            viewerPublicKey: String,
            sessionId: String,
            viewerAddress: String,
        ): Intent {
            return Intent(context, SessionActivity::class.java)
                .putExtra(EXTRA_ACCOUNT_ID, accountId)
                .putExtra(EXTRA_DEVICE_ID, deviceId)
                .putExtra(EXTRA_BASE_URL, baseUrl)
                .putExtra(EXTRA_RUNTIME_ID, runtimeId)
                .putExtra(EXTRA_RUNTIME_NAME, runtimeName)
                .putExtra(EXTRA_VIEWER_ADDRESS, viewerAddress.ifBlank { "$relayHost:$relayPort" })
                .putExtra(EXTRA_RELAY_HOST, relayHost)
                .putExtra(EXTRA_RELAY_PORT, relayPort)
                .putExtra(EXTRA_RELAY_TLS, relayTls)
                .putExtra(EXTRA_RELAY_PATH, relayPath)
                .putExtra(EXTRA_RELAY_TOKEN, relayToken)
                .putExtra(EXTRA_VIEWER_PUBLIC_KEY, viewerPublicKey)
                .putExtra(EXTRA_SESSION_ID, sessionId)
        }

        fun createIntent(
            context: Context,
            session: ActiveSessionStore.ActiveSession,
        ): Intent {
            return createIntent(
                context = context,
                accountId = session.accountId,
                deviceId = session.deviceId,
                baseUrl = session.baseUrl,
                runtimeId = session.runtimeId,
                runtimeName = session.runtimeName,
                relayHost = session.relayHost,
                relayPort = session.relayPort,
                relayTls = session.relayTls,
                relayPath = session.relayPath,
                relayToken = session.relayToken,
                viewerPublicKey = session.viewerPublicKey,
                sessionId = session.sessionId,
                viewerAddress = session.viewerAddress,
            )
        }
    }
}
