package io.virtroid.client

import android.Manifest
import android.app.Activity
import android.app.KeyguardManager
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
import androidx.activity.addCallback
import androidx.activity.result.contract.ActivityResultContracts.StartActivityForResult
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenSessionViewerBinding
import io.virtroid.client.security.ConfidentialRuntimeGuard
import io.virtroid.client.security.SnapshotRollbackGuard
import io.virtroid.client.security.SnapshotRollbackException
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.ui.VirtroidMotion
import io.virtroid.client.viewer.ScrcpySessionHost
import io.virtroid.client.viewer.RelayLatencyProbe
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File
import io.virtroid.client.ui.Notice

class SessionActivity : AppCompatActivity() {
    private lateinit var binding: ScreenSessionViewerBinding
    private var sessionHost: ScrcpySessionHost? = null
    private val api = VirtroidApi()
    private var viewerAddress: String = ""
    private var relayHost: String = ""
    private var relayPort: Int = 0
    private var relayTls: Boolean = false
    private var relayPath: String = ""
    private var relayTlsPin: String = ""
    private var relayFallbackHost: String = ""
    private var relayFallbackPort: Int = 0
    private var relayToken: String = ""
    private var viewerPublicKey: String = ""
    private var viewerProtocol: Int = 1
    // Kept in the encrypted session store, not the intent, which Android may
    // save with the task.
    private var viewerKey: String = ""
    private var sessionId: String = ""
    private var runtimeName: String = ""
    private var runtimeId: String = ""
    private var accountId: String = ""
    private var deviceId: String = ""
    private var baseUrl: String = ""
    private var audioEnabled: Boolean = false
    private var cameraMode: String = "photo-import"
    private var fileMode: String = "upload-only"
    private var endingSession = false
    private var viewerSurface: Surface? = null
    private lateinit var snapshotRollbackGuard: SnapshotRollbackGuard
    private val confidentialRuntimeGuard by lazy { ConfidentialRuntimeGuard(this) }
    private lateinit var activeSessionStore: ActiveSessionStore
    private lateinit var sessionStore: SessionStore
    private lateinit var appSettings: AppSettingsStore
    private lateinit var appLogs: AppLogStore
    private var lastInteractionAtMs = System.currentTimeMillis()
    private var inactivityJob: Job? = null
    private var heartbeatJob: Job? = null
    private var viewerReconnectJob: Job? = null
    private var latencyJob: Job? = null
    private var viewerSurfaceResumeJob: Job? = null
    private var viewerReconnectDelayMs = VIEWER_RECONNECT_INITIAL_DELAY_MS
    private var sessionUnavailable = false
    private var heartbeatFailureCount = 0
    private var physicalMicrophoneEnabled = false
    private var microphoneFailureReported = false

    private val cameraCapture = registerForActivityResult(StartActivityForResult()) { result ->
        if (result.resultCode != Activity.RESULT_OK) {
            result.data?.getStringExtra(CameraCaptureActivity.EXTRA_CAPTURE_ERROR)?.let { toast(it) }
            return@registerForActivityResult
        }
        val path = result.data?.getStringExtra(CameraCaptureActivity.EXTRA_CAPTURE_PATH).orEmpty()
        val name = result.data?.getStringExtra(CameraCaptureActivity.EXTRA_CAPTURE_NAME).orEmpty()
        val kind = result.data?.getStringExtra(CameraCaptureActivity.EXTRA_CAPTURE_KIND)
            ?: CameraCaptureActivity.CAPTURE_KIND_PHOTO
        val hasAudio = result.data?.getBooleanExtra(CameraCaptureActivity.EXTRA_CAPTURE_HAS_AUDIO, false) == true
        if (path.isNotBlank() && name.isNotBlank()) {
            importCapturedMedia(path, name, kind, hasAudio)
        }
    }

    private val sessionCallback = object : ScrcpySessionHost.Callback {
        override fun onConnected(remoteWidth: Int, remoteHeight: Int) {
            runOnUiThread {
                appLogs.info("Session stream connected for $runtimeName", "session")
                viewerReconnectDelayMs = VIEWER_RECONNECT_INITIAL_DELAY_MS
                binding.sessionSubtitleText.text = getString(R.string.session_connected)
                startLatencyProbe()
                binding.sessionStreamStatusText.text = getString(R.string.session_stream_receiving)
                binding.sessionRetryButton.isVisible = false
                applyRemoteSurfaceBounds(remoteWidth, remoteHeight)
                updateViewerGestureExclusion()
                hideSystemBars()
            }
        }

        override fun onFirstVideoFrame() {
            runOnUiThread {
                viewerSurfaceResumeJob?.cancel()
                viewerSurfaceResumeJob = null
                binding.sessionStreamStatusOverlay.isVisible = false
            }
        }

        override fun onPhysicalMicrophoneStateChanged(active: Boolean, detail: String) {
            runOnUiThread {
                when {
                    active -> {
                        microphoneFailureReported = false
                        appLogs.info("Physical microphone bridge active for runtime recording", "session")
                    }
                    detail == "inactive" -> {
                        appLogs.info("Physical microphone bridge stopped", "session")
                    }
                    detail == "runtime-unavailable" && !microphoneFailureReported -> {
                        microphoneFailureReported = true
                        appLogs.warn("Runtime microphone injection is unavailable on this Android image", "session")
                        toast(getString(R.string.session_microphone_runtime_unavailable))
                    }
                    detail != "disabled" && !microphoneFailureReported -> {
                        microphoneFailureReported = true
                        appLogs.warn("Physical microphone bridge could not start: $detail", "session")
                        toast(getString(R.string.session_microphone_bridge_failed))
                    }
                }
            }
        }

        override fun onDisconnected(message: String) {
            runOnUiThread {
                stopLatencyProbe()
                binding.sessionSubtitleText.text = if (endingSession) {
                    getString(R.string.session_secure_saving)
                } else if (sessionUnavailable) {
                    getString(R.string.session_heartbeat_stale)
                } else {
                    getString(R.string.session_failed_message_inline, message)
                }
                if (!endingSession) {
                    viewerSurfaceResumeJob?.cancel()
                    viewerSurfaceResumeJob = null
                    viewerReconnectDelayMs =
                        (viewerReconnectDelayMs * 2).coerceAtMost(VIEWER_RECONNECT_MAX_DELAY_MS)
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
        relayTlsPin = intent.getStringExtra(EXTRA_RELAY_TLS_PIN).orEmpty()
        relayFallbackHost = intent.getStringExtra(EXTRA_RELAY_FALLBACK_HOST).orEmpty()
        relayFallbackPort = intent.getIntExtra(EXTRA_RELAY_FALLBACK_PORT, 0)
        relayToken = intent.getStringExtra(EXTRA_RELAY_TOKEN).orEmpty()
        viewerPublicKey = intent.getStringExtra(EXTRA_VIEWER_PUBLIC_KEY).orEmpty()
        viewerProtocol = intent.getIntExtra(EXTRA_VIEWER_PROTOCOL, 1)
        sessionId = intent.getStringExtra(EXTRA_SESSION_ID).orEmpty()
        runtimeName = intent.getStringExtra(EXTRA_RUNTIME_NAME).orEmpty()
        runtimeId = intent.getStringExtra(EXTRA_RUNTIME_ID).orEmpty()
        accountId = intent.getStringExtra(EXTRA_ACCOUNT_ID).orEmpty()
        deviceId = intent.getStringExtra(EXTRA_DEVICE_ID).orEmpty()
        baseUrl = intent.getStringExtra(EXTRA_BASE_URL).orEmpty()
        audioEnabled = intent.getBooleanExtra(EXTRA_AUDIO_ENABLED, false)
        physicalMicrophoneEnabled = audioEnabled &&
            checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
        cameraMode = intent.getStringExtra(EXTRA_CAMERA_MODE).orEmpty().ifBlank { "disabled" }
        fileMode = intent.getStringExtra(EXTRA_FILE_MODE).orEmpty().ifBlank { "upload-only" }
        sessionStore = SessionStore(this)
        if (accountId.isBlank()) {
            accountId = sessionStore.accountId.orEmpty()
        }
        if (deviceId.isBlank()) {
            deviceId = sessionStore.deviceId.orEmpty()
        }
        if (baseUrl.isBlank()) {
            baseUrl = sessionStore.baseUrl
        }
        snapshotRollbackGuard = SnapshotRollbackGuard(this)
        activeSessionStore = ActiveSessionStore(this)
        viewerKey = activeSessionStore.loadForRuntime(runtimeId)?.viewerKey.orEmpty()
        appSettings = AppSettingsStore(this)
        appLogs = AppLogStore.get(this)
        if (viewerProtocol >= 2 && viewerKey.isBlank()) {
            // Opened before this phone kept viewer keys: a new session derives it.
            activeSessionStore.clear()
            toast(getString(R.string.session_viewer_key_missing))
            finish()
            return
        }
        persistActiveSession()

        if (relayHost.isBlank() || relayPort <= 0 || !relayTls || relayPath.isBlank() || relayToken.isBlank() || viewerPublicKey.isBlank()) {
            toast(getString(R.string.session_missing_endpoint))
            finish()
            return
        }
        liveSessionId = sessionId

        val title = runtimeDisplayName(runtimeName).ifBlank { getString(R.string.session_title_fallback) }
        binding.sessionTitleText.text = title
        binding.sessionSubtitleText.text = getString(R.string.session_connecting_short)
        binding.sessionBackToAppButton.setOnClickListener { navigateBackToApp() }
        binding.sessionControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtimeId))
        }
        binding.sessionRetryButton.setOnClickListener {
            viewerReconnectDelayMs = VIEWER_RECONNECT_INITIAL_DELAY_MS
            retryViewerConnection()
        }
        val cameraEnabled = cameraMode.equals("photo-import", ignoreCase = true)
        binding.sessionCameraButton.isVisible = cameraEnabled
        binding.sessionOptionalActionsDivider.isVisible = cameraEnabled
        binding.sessionCameraButton.setOnClickListener {
            val cameraGranted = checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED
            val audioGranted = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
            if (cameraGranted && audioGranted) {
                launchPhysicalCamera(recordAudio = true)
            } else if (cameraGranted) {
                toast(getString(R.string.session_camera_audio_permission_required))
                launchPhysicalCamera(recordAudio = false)
            } else {
                toast(getString(R.string.session_camera_permissions_setup_required))
            }
        }
        binding.sessionRetryButton.isVisible = false
        binding.sessionStreamStatusText.text = getString(R.string.session_stream_connecting)
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
                left = resources.getDimensionPixelSize(R.dimen.space_16) + systemBars.left,
                top = resources.getDimensionPixelSize(R.dimen.space_6) + systemBars.top,
                right = resources.getDimensionPixelSize(R.dimen.space_16) + systemBars.right,
                bottom = resources.getDimensionPixelSize(R.dimen.space_6),
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
        stopLatencyProbe()
        if (liveSessionId == sessionId) {
            liveSessionId = null
        }
        inactivityJob?.cancel()
        heartbeatJob?.cancel()
        viewerReconnectJob?.cancel()
        viewerSurfaceResumeJob?.cancel()
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
        viewerSurfaceResumeJob?.cancel()
        viewerSurfaceResumeJob = lifecycleScope.launch {
            delay(VIEWER_SURFACE_RESUME_TIMEOUT_MS)
            if (!endingSession && !sessionUnavailable && sessionHost === host &&
                binding.sessionStreamStatusOverlay.isVisible
            ) {
                appLogs.warn("Viewer surface resume did not render a frame before timeout", "session")
                binding.sessionStreamProgress.isVisible = false
                binding.sessionStreamStatusText.text = getString(R.string.session_stream_resume_delayed)
                binding.sessionRetryButton.isVisible = true
            }
            viewerSurfaceResumeJob = null
        }
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
            relayTlsPin = relayTlsPin,
            relayFallbackHost = relayFallbackHost,
            relayFallbackPort = relayFallbackPort,
            relayToken = relayToken,
            viewerPublicKey = viewerPublicKey,
            viewerProtocol = viewerProtocol,
            viewerKey = viewerKey,
            audioEnabled = audioEnabled,
            physicalMicrophoneEnabled = physicalMicrophoneEnabled,
            surface = surface,
            displayWidth = binding.sessionSurfaceView.width.takeIf { it > 0 }
                ?: resources.displayMetrics.widthPixels,
            displayHeight = binding.sessionSurfaceView.height.takeIf { it > 0 }
                ?: resources.displayMetrics.heightPixels,
            callback = sessionCallback,
        ).also { it.connect() }
    }

    private fun disconnectViewer() {
        viewerSurfaceResumeJob?.cancel()
        viewerSurfaceResumeJob = null
        sessionHost?.destroy()
        sessionHost = null
    }

    private fun launchPhysicalCamera(recordAudio: Boolean) {
        if (!cameraMode.equals("photo-import", ignoreCase = true)) {
            return
        }
        cameraCapture.launch(CameraCaptureActivity.createIntent(this, recordAudio))
    }

    private fun importCapturedMedia(path: String, name: String, kind: String, hasAudio: Boolean) {
        val media = File(path)
        val cacheRoot = cacheDir.canonicalFile
        val candidate = runCatching { media.canonicalFile }.getOrNull()
        val video = kind == CameraCaptureActivity.CAPTURE_KIND_VIDEO
        val maxBytes = if (video) MAX_RUNTIME_VIDEO_IMPORT_BYTES else MAX_RUNTIME_PHOTO_IMPORT_BYTES
        if (candidate == null || candidate.parentFile != cacheRoot || !candidate.isFile || candidate.length() !in 1..maxBytes.toLong()) {
            candidate?.delete()
            toast(getString(if (video) R.string.session_video_capture_failed else R.string.session_camera_capture_failed))
            return
        }

        binding.sessionCameraButton.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                val bytes = candidate.readBytes()
                if (video) {
                    api.importRuntimeVideo(
                        baseUrl = baseUrl,
                        accountId = accountId,
                        deviceId = deviceId,
                        runtimeId = runtimeId,
                        sessionId = sessionId,
                        fileName = name,
                        mp4 = bytes,
                    )
                } else {
                    api.importRuntimePhoto(
                        baseUrl = baseUrl,
                        accountId = accountId,
                        deviceId = deviceId,
                        runtimeId = runtimeId,
                        sessionId = sessionId,
                        fileName = name,
                        jpeg = bytes,
                    )
                }
            }.onSuccess { result ->
                val mediaKind = if (video) "video${if (hasAudio) " with sound" else " without sound"}" else "photo"
                appLogs.info("Imported physical-camera $mediaKind ${result.fileName} into active runtime", "session")
                toast(getString(if (video) R.string.session_video_imported else R.string.session_photo_imported, result.fileName))
            }.onFailure { error ->
                appLogs.warn("Physical-camera ${if (video) "video" else "photo"} import failed: ${error.message}", "session")
                toast(error.virtroidDisplayMessage(this@SessionActivity))
            }
            candidate.delete()
            binding.sessionCameraButton.isEnabled = true
        }
    }

    private fun retryViewerConnection(delayMs: Long = 0L) {
        if (sessionUnavailable) {
            return
        }
        if (viewerReconnectJob?.isActive == true) {
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
        viewerReconnectJob = lifecycleScope.launch {
            if (delayMs > 0L) {
                delay(delayMs)
            }
            runCatching {
                api.issueSessionRelayToken(baseUrl, accountId, deviceId, runtimeId, sessionId).also { refresh ->
                    confidentialRuntimeGuard.check(runtimeId, relayTlsPin, refresh.viewerPublicKey, refresh.attestation) {
                        api.fetchSnpMeasurements(baseUrl)
                    }
                }
            }.onSuccess { refresh ->
                relayToken = refresh.relayToken
                viewerPublicKey = refresh.viewerPublicKey
                viewerProtocol = refresh.viewerProtocol
                persistActiveSession()
                connectViewer(surface)
            }.onFailure { error ->
                appLogs.warn("Session relay token refresh failed: ${error.message}", "session")
                binding.sessionStreamProgress.isVisible = false
                binding.sessionStreamStatusOverlay.isVisible = true
                binding.sessionRetryButton.isVisible = true
                toast(error.virtroidDisplayMessage(this@SessionActivity))
            }
            viewerReconnectJob = null
        }
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
                api.getSessionState(baseUrl, accountId, deviceId, runtimeId, sessionId).also {
                    it.runtime?.let { runtime -> snapshotRollbackGuard.verifyAndRecord(accountId, runtime) }
                }
            }.onSuccess {
                if (it.canResumeRuntime(runtimeId)) {
                    heartbeatFailureCount = 0
                    activeSessionStore.touch(sessionId)
                    markHeartbeatHealthy()
                } else {
                    appLogs.warn("Session stream disconnected and backend state is ${it.effectiveStatus}", "session")
                    appSettings.recordSessionEnd(AppSettingsStore.SESSION_END_SERVER)
                    activeSessionStore.clear()
                    relayToken = ""
                    markSessionUnavailable()
                    disconnectViewer()
                }
            }.onFailure { error ->
                if (error is SnapshotRollbackException) {
                    appLogs.error(error.message ?: "Encrypted snapshot rollback detected", "security")
                    activeSessionStore.clear()
                    relayToken = ""
                    markSessionUnavailable()
                    disconnectViewer()
                    toast(error.virtroidDisplayMessage(this@SessionActivity))
                } else if (error.isGoneSessionResponse()) {
                    appLogs.warn("Session stream disconnected and backend session is gone: ${error.message}", "session")
                    appSettings.recordSessionEnd(AppSettingsStore.SESSION_END_SERVER)
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

    /**
     * Closes the viewer after the screen-idle time in Privacy & Security. The
     * runtime keeps running for its own inactivity period, counted from now,
     * so its apps keep receiving messages.
     */
    private fun closeViewerForInactivity() {
        if (endingSession) {
            return
        }
        endingSession = true
        heartbeatJob?.cancel()
        appSettings.recordSessionEnd(AppSettingsStore.SESSION_END_INACTIVITY)
        disconnectViewer()
        activeSessionStore.clear()
        relayToken = ""
        if (appSettings.autoClearClipboard) {
            appSettings.clearClipboard()
            appLogs.info("Clipboard cleared after the viewer closed", "privacy")
        }
        appLogs.info("Viewer closed after screen inactivity; the runtime keeps running", "session")
        toast(getString(R.string.session_closed_inactive))
        finishToRuntimeList(markRuntimeStopping = false)
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
            Gravity.TOP or Gravity.CENTER_HORIZONTAL,
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
        Notice.show(this, message)
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
                relayTlsPin = relayTlsPin,
                relayFallbackHost = relayFallbackHost,
                relayFallbackPort = relayFallbackPort,
                audioEnabled = audioEnabled,
                cameraMode = cameraMode,
                fileMode = fileMode,
                viewerProtocol = viewerProtocol,
                viewerKey = viewerKey,
            ),
        )
    }

    /**
     * Shows the round trip to wherever the stream is connected, refreshed while
     * the viewer is on screen.
     */
    private fun startLatencyProbe() {
        if (latencyJob?.isActive == true) {
            return
        }
        latencyJob = lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                while (true) {
                    val endpoint = sessionHost?.streamEndpoint()
                    val roundTripMs = endpoint?.let { withContext(Dispatchers.IO) { RelayLatencyProbe.measure(it) } }
                    showLatency(roundTripMs)
                    delay(LATENCY_PROBE_INTERVAL_MS)
                }
            }
        }
    }

    private fun stopLatencyProbe() {
        latencyJob?.cancel()
        latencyJob = null
        binding.sessionLatencyText.isVisible = false
    }

    private fun showLatency(roundTripMs: Long?) {
        if (roundTripMs == null) {
            binding.sessionLatencyText.isVisible = false
            return
        }
        binding.sessionLatencyText.text = getString(R.string.session_latency, roundTripMs)
        binding.sessionLatencyText.setTextColor(
            getColor(
                when {
                    roundTripMs <= LATENCY_FINE_MS -> R.color.v_text_muted
                    roundTripMs <= LATENCY_HIGH_MS -> R.color.v_amber
                    else -> R.color.v_danger
                },
            ),
        )
        binding.sessionLatencyText.isVisible = true
    }

    private fun markHeartbeatPending() {
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_amber)
        VirtroidMotion.setStatusPulse(binding.connectionDot, active = true)
    }

    private fun markHeartbeatHealthy() {
        heartbeatFailureCount = 0
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_accent)
        VirtroidMotion.setStatusPulse(binding.connectionDot, active = true)
        if (!endingSession && !sessionUnavailable && sessionHost == null && viewerSurface != null) {
            retryViewerConnection(viewerReconnectDelayMs)
        }
    }

    private fun markHeartbeatRetrying() {
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_amber)
        VirtroidMotion.setStatusPulse(binding.connectionDot, active = true)
    }

    private fun markSessionUnavailable() {
        sessionUnavailable = true
        binding.connectionDot.setBackgroundResource(R.drawable.bg_dot_muted)
        VirtroidMotion.setStatusPulse(binding.connectionDot, active = false)
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
                // The device key signs only while the phone is unlocked. Locked
                // for longer than the server waits, the viewer counts as
                // closed and the runtime's keep-running period starts.
                if (getSystemService(KeyguardManager::class.java)?.isDeviceLocked == true) {
                    delay(SESSION_HEARTBEAT_INTERVAL_MS)
                    continue
                }
                runCatching {
                    api.heartbeatSession(baseUrl, accountId, deviceId, runtimeId, sessionId)
                }.onSuccess {
                    heartbeatFailureCount = 0
                    activeSessionStore.touch(sessionId)
                    markHeartbeatHealthy()
                }.onFailure { error ->
                    if (error.isGoneSessionResponse()) {
                        appLogs.info("Viewer session ended while the app was away; the runtime keeps running", "session")
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
                    appLogs.info("Viewer closed after ${appSettings.uiInactivityLabel()} without input", "session")
                    closeViewerForInactivity()
                    return@launch
                }
            }
        }
    }

    companion object {
        /**
         * The session whose viewer screen exists right now, stream included.
         * Back leaves it running behind the runtime list, so returning to it
         * needs no new relay token (main thread only).
         */
        var liveSessionId: String? = null
            private set

        private const val EXTRA_RUNTIME_NAME = "runtime_name"
        private const val EXTRA_VIEWER_ADDRESS = "viewer_address"
        private const val EXTRA_RELAY_HOST = "relay_host"
        private const val EXTRA_RELAY_PORT = "relay_port"
        private const val EXTRA_RELAY_TLS = "relay_tls"
        private const val EXTRA_RELAY_PATH = "relay_path"
        private const val EXTRA_RELAY_TLS_PIN = "relay_tls_pin"
        private const val EXTRA_RELAY_FALLBACK_HOST = "relay_fallback_host"
        private const val EXTRA_RELAY_FALLBACK_PORT = "relay_fallback_port"
        private const val EXTRA_RELAY_TOKEN = "relay_token"
        private const val EXTRA_VIEWER_PUBLIC_KEY = "viewer_public_key"
        private const val EXTRA_VIEWER_PROTOCOL = "viewer_protocol"
        private const val EXTRA_SESSION_ID = "session_id"
        private const val EXTRA_RUNTIME_ID = "runtime_id"
        private const val EXTRA_ACCOUNT_ID = "account_id"
        private const val EXTRA_DEVICE_ID = "device_id"
        private const val EXTRA_BASE_URL = "base_url"
        private const val EXTRA_AUDIO_ENABLED = "audio_enabled"
        private const val EXTRA_CAMERA_MODE = "camera_mode"
        private const val EXTRA_FILE_MODE = "file_mode"
        private const val SESSION_HEARTBEAT_INTERVAL_MS = 20_000L
        private const val HEARTBEAT_RETRY_VISIBLE_THRESHOLD = 2
        private const val VIEWER_RECONNECT_INITIAL_DELAY_MS = 1_000L
        private const val VIEWER_RECONNECT_MAX_DELAY_MS = 10_000L
        private const val VIEWER_SURFACE_RESUME_TIMEOUT_MS = 5_000L
        private const val MAX_RUNTIME_PHOTO_IMPORT_BYTES = 16 * 1024 * 1024
        private const val MAX_RUNTIME_VIDEO_IMPORT_BYTES = 32 * 1024 * 1024
        private const val LATENCY_PROBE_INTERVAL_MS = 3_000L
        // Round trips up to these read as fine (grey), then high (amber),
        // then very high (red) for an interactive stream.
        private const val LATENCY_FINE_MS = 120L
        private const val LATENCY_HIGH_MS = 250L

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
            relayTlsPin: String = "",
            relayFallbackHost: String = "",
            relayFallbackPort: Int = 0,
            audioEnabled: Boolean = false,
            cameraMode: String = "photo-import",
            fileMode: String = "upload-only",
            viewerProtocol: Int = 1,
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
                .putExtra(EXTRA_RELAY_TLS_PIN, relayTlsPin)
                .putExtra(EXTRA_RELAY_FALLBACK_HOST, relayFallbackHost)
                .putExtra(EXTRA_RELAY_FALLBACK_PORT, relayFallbackPort)
                .putExtra(EXTRA_RELAY_TOKEN, relayToken)
                .putExtra(EXTRA_VIEWER_PUBLIC_KEY, viewerPublicKey)
                .putExtra(EXTRA_VIEWER_PROTOCOL, viewerProtocol)
                .putExtra(EXTRA_SESSION_ID, sessionId)
                .putExtra(EXTRA_AUDIO_ENABLED, audioEnabled)
                .putExtra(EXTRA_CAMERA_MODE, cameraMode)
                .putExtra(EXTRA_FILE_MODE, fileMode)
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
                relayTlsPin = session.relayTlsPin,
                relayFallbackHost = session.relayFallbackHost,
                relayFallbackPort = session.relayFallbackPort,
                audioEnabled = session.audioEnabled,
                cameraMode = session.cameraMode,
                fileMode = session.fileMode,
                viewerProtocol = session.viewerProtocol,
            )
        }
    }
}
