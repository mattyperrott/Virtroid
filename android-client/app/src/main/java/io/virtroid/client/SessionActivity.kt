package io.virtroid.client

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.ImageFormat
import android.graphics.Rect
import android.graphics.SurfaceTexture
import android.hardware.camera2.CameraCaptureSession
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CameraDevice
import android.hardware.camera2.CameraManager
import android.hardware.camera2.params.OutputConfiguration
import android.hardware.camera2.params.SessionConfiguration
import android.media.ImageReader
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.provider.OpenableColumns
import android.view.Gravity
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.Surface
import android.view.TextureView
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.Toast
import androidx.activity.addCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.api.RuntimeSummary
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenSessionViewerBinding
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.SnapshotRollbackGuard
import io.virtroid.client.security.SnapshotRollbackException
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.security.promptIdentityPassword
import io.virtroid.client.viewer.ScrcpySessionHost
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.time.LocalTime
import java.time.format.DateTimeFormatter
import java.util.Locale
import java.util.concurrent.Executor
import java.util.concurrent.atomic.AtomicBoolean

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
    private var audioEnabled: Boolean = false
    private var cameraMode: String = "disabled"
    private var fileMode: String = "upload-only"
    private var endingSession = false
    private var viewerSurface: Surface? = null
    private lateinit var identityPasswordStore: IdentityPasswordStore
    private lateinit var snapshotRollbackGuard: SnapshotRollbackGuard
    private lateinit var activeSessionStore: ActiveSessionStore
    private lateinit var sessionStore: SessionStore
    private lateinit var appSettings: AppSettingsStore
    private lateinit var appLogs: AppLogStore
    private var lastInteractionAtMs = System.currentTimeMillis()
    private var inactivityJob: Job? = null
    private var heartbeatJob: Job? = null
    private var viewerReconnectJob: Job? = null
    private var viewerReconnectDelayMs = VIEWER_RECONNECT_INITIAL_DELAY_MS
    private var sessionUnavailable = false
    private var heartbeatFailureCount = 0
    private var cameraDevice: CameraDevice? = null
    private var cameraCaptureSession: CameraCaptureSession? = null
    private var cameraImageReader: ImageReader? = null
    private var cameraThread: HandlerThread? = null
    private var cameraHandler: Handler? = null
    private var cameraPassthroughRunning = false
    private var lastCameraFrameAtMs = 0L
    private var cameraFailureCount = 0
    private val cameraFrameInFlight = AtomicBoolean(false)

    private val filePicker = registerForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        uri?.let(::importSelectedFile)
    }

    private val cameraPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) {
            startCameraPassthrough()
        } else {
            toast(getString(R.string.session_camera_permission_required))
        }
    }

    private val sessionCallback = object : ScrcpySessionHost.Callback {
        override fun onConnected(remoteWidth: Int, remoteHeight: Int) {
            runOnUiThread {
                appLogs.info("Session stream connected for $runtimeName", "session")
                viewerReconnectDelayMs = VIEWER_RECONNECT_INITIAL_DELAY_MS
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
        audioEnabled = intent.getBooleanExtra(EXTRA_AUDIO_ENABLED, false)
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
        identityPasswordStore = IdentityPasswordStore(this)
        snapshotRollbackGuard = SnapshotRollbackGuard(this)
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
        binding.sessionDisconnectButton.setOnClickListener { confirmSessionShutdown() }
        binding.sessionControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtimeId))
        }
        binding.sessionRetryButton.setOnClickListener {
            viewerReconnectDelayMs = VIEWER_RECONNECT_INITIAL_DELAY_MS
            retryViewerConnection()
        }
        val fileImportEnabled = fileMode.equals("upload-only", ignoreCase = true) ||
            fileMode.equals("bidirectional", ignoreCase = true)
        val cameraEnabled = cameraMode.equals("passthrough", ignoreCase = true)
        binding.sessionUploadButton.isVisible = fileImportEnabled
        binding.sessionCameraButton.isVisible = cameraEnabled
        binding.sessionOptionalActionsDivider.isVisible = fileImportEnabled || cameraEnabled
        binding.sessionUploadButton.setOnClickListener {
            filePicker.launch(arrayOf("*/*"))
        }
        binding.sessionCameraButton.setOnClickListener {
            if (cameraPassthroughRunning) {
                stopCameraPassthrough()
                toast(getString(R.string.session_camera_stopped))
            } else if (checkSelfPermission(Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED) {
                startCameraPassthrough()
            } else {
                cameraPermission.launch(Manifest.permission.CAMERA)
            }
        }
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
		stopCameraPassthrough()
        persistActiveSession()
        super.onStop()
    }

    override fun onDestroy() {
        inactivityJob?.cancel()
        heartbeatJob?.cancel()
        viewerReconnectJob?.cancel()
        stopCameraPassthrough()
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
            audioEnabled = audioEnabled,
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

    private fun importSelectedFile(uri: Uri) {
        val metadata = queryImportMetadata(uri)
        if (metadata.second != null && metadata.second!! > MAX_RUNTIME_FILE_IMPORT_BYTES) {
            toast(getString(R.string.session_file_too_large))
            return
        }
        binding.sessionUploadButton.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                val bytes = readImportBytes(uri)
                api.importRuntimeFile(
                    baseUrl = baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    runtimeId = runtimeId,
                    sessionId = sessionId,
                    fileName = metadata.first,
                    contentType = contentResolver.getType(uri).orEmpty(),
                    body = bytes,
                )
            }.onSuccess { result ->
                appLogs.info("Imported ${result.fileName} into active runtime", "session")
                toast(getString(R.string.session_file_imported, result.fileName))
            }.onFailure { error ->
                appLogs.warn("Active runtime file import failed: ${error.message}", "session")
                toast(error.virtroidDisplayMessage(this@SessionActivity))
            }
            binding.sessionUploadButton.isEnabled = true
        }
    }

    private fun queryImportMetadata(uri: Uri): Pair<String, Long?> {
        var name = "import-${System.currentTimeMillis()}"
        var size: Long? = null
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { cursor ->
            if (cursor.moveToFirst()) {
                val nameIndex = cursor.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                val sizeIndex = cursor.getColumnIndex(OpenableColumns.SIZE)
                if (nameIndex >= 0) {
                    name = cursor.getString(nameIndex)?.takeIf { it.isNotBlank() } ?: name
                }
                if (sizeIndex >= 0 && !cursor.isNull(sizeIndex)) {
                    size = cursor.getLong(sizeIndex)
                }
            }
        }
        return name.take(255) to size
    }

    private fun readImportBytes(uri: Uri): ByteArray {
        val stream = contentResolver.openInputStream(uri) ?: throw IOException("selected file is unavailable")
        return stream.use { input ->
            val output = ByteArrayOutputStream()
            val buffer = ByteArray(64 * 1024)
            var total = 0
            while (true) {
                val read = input.read(buffer)
                if (read < 0) break
                total += read
                if (total > MAX_RUNTIME_FILE_IMPORT_BYTES) {
                    throw IOException("selected file exceeds 32 MiB")
                }
                output.write(buffer, 0, read)
            }
            output.toByteArray()
        }
    }

    @SuppressLint("MissingPermission")
    private fun startCameraPassthrough() {
        if (cameraPassthroughRunning || !cameraMode.equals("passthrough", ignoreCase = true)) {
            return
        }
        runCatching { openCameraPassthrough() }
            .onFailure { error ->
                appLogs.warn("Camera passthrough setup failed: ${error.message}", "session")
                stopCameraPassthrough()
                toast(getString(R.string.session_camera_unavailable))
            }
    }

    @SuppressLint("MissingPermission")
    private fun openCameraPassthrough() {
        val manager = getSystemService(CameraManager::class.java)
        val cameraId = manager.cameraIdList.firstOrNull { id ->
            manager.getCameraCharacteristics(id).get(CameraCharacteristics.LENS_FACING) == CameraCharacteristics.LENS_FACING_BACK
        } ?: manager.cameraIdList.firstOrNull()
        if (cameraId == null) {
            toast(getString(R.string.session_camera_unavailable))
            return
        }
        val characteristics = manager.getCameraCharacteristics(cameraId)
        val sizes = characteristics.get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP)
            ?.getOutputSizes(ImageFormat.JPEG)
            .orEmpty()
        val size = sizes.minByOrNull { candidate ->
            kotlin.math.abs(candidate.width * candidate.height - CAMERA_TARGET_WIDTH * CAMERA_TARGET_HEIGHT)
        } ?: android.util.Size(CAMERA_TARGET_WIDTH, CAMERA_TARGET_HEIGHT)

        cameraThread = HandlerThread("virtroid-camera-passthrough").also { it.start() }
        cameraHandler = Handler(checkNotNull(cameraThread).looper)
        cameraImageReader = ImageReader.newInstance(size.width, size.height, ImageFormat.JPEG, 2).also { reader ->
            reader.setOnImageAvailableListener({ source ->
                val image = source.acquireLatestImage() ?: return@setOnImageAvailableListener
                val now = System.currentTimeMillis()
                try {
                    if (!cameraPassthroughRunning || now - lastCameraFrameAtMs < CAMERA_FRAME_INTERVAL_MS || !cameraFrameInFlight.compareAndSet(false, true)) {
                        return@setOnImageAvailableListener
                    }
                    val buffer = image.planes[0].buffer
                    val frame = ByteArray(buffer.remaining())
                    buffer.get(frame)
                    lastCameraFrameAtMs = now
                    lifecycleScope.launch {
                        runCatching {
                            api.sendRuntimeCameraFrame(baseUrl, accountId, deviceId, runtimeId, sessionId, frame)
                        }.onSuccess {
                            cameraFailureCount = 0
                        }.onFailure { error ->
                            cameraFailureCount++
                            if (cameraFailureCount >= CAMERA_FAILURE_LIMIT) {
                                appLogs.warn("Camera passthrough stopped after repeated node failures: ${error.message}", "session")
                                stopCameraPassthrough()
                                toast(error.virtroidDisplayMessage(this@SessionActivity))
                            }
                        }
                        cameraFrameInFlight.set(false)
                    }
                } finally {
                    image.close()
                }
            }, cameraHandler)
        }
        cameraPassthroughRunning = true
        binding.sessionCameraButton.isSelected = true
        manager.openCamera(cameraId, object : CameraDevice.StateCallback() {
            override fun onOpened(camera: CameraDevice) {
                cameraDevice = camera
                val surface = cameraImageReader?.surface ?: return
                val handler = cameraHandler ?: return
                val stateCallback = object : CameraCaptureSession.StateCallback() {
                    override fun onConfigured(session: CameraCaptureSession) {
                        cameraCaptureSession = session
                        val request = camera.createCaptureRequest(CameraDevice.TEMPLATE_RECORD).apply {
                            addTarget(surface)
                            set(android.hardware.camera2.CaptureRequest.CONTROL_AF_MODE, android.hardware.camera2.CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_PICTURE)
                        }.build()
                        session.setRepeatingRequest(request, null, cameraHandler)
                        runOnUiThread { toast(getString(R.string.session_camera_started)) }
                    }

                    override fun onConfigureFailed(session: CameraCaptureSession) {
                        runOnUiThread {
                            stopCameraPassthrough()
                            toast(getString(R.string.session_camera_unavailable))
                        }
                    }
                }
                camera.createCaptureSession(
                    SessionConfiguration(
                        SessionConfiguration.SESSION_REGULAR,
                        listOf(OutputConfiguration(surface)),
                        Executor { command -> handler.post(command) },
                        stateCallback,
                    ),
                )
            }

            override fun onDisconnected(camera: CameraDevice) {
                runOnUiThread { stopCameraPassthrough() }
            }

            override fun onError(camera: CameraDevice, error: Int) {
                runOnUiThread {
                    stopCameraPassthrough()
                    toast(getString(R.string.session_camera_unavailable))
                }
            }
        }, cameraHandler)
    }

    private fun stopCameraPassthrough() {
        cameraPassthroughRunning = false
        cameraFrameInFlight.set(false)
        runCatching { cameraCaptureSession?.stopRepeating() }
        cameraCaptureSession?.close()
        cameraCaptureSession = null
        cameraDevice?.close()
        cameraDevice = null
        cameraImageReader?.close()
        cameraImageReader = null
        cameraHandler = null
        cameraThread?.quitSafely()
        cameraThread = null
        if (::binding.isInitialized) runOnUiThread { binding.sessionCameraButton.isSelected = false }
    }

    private fun retryViewerConnection(delayMs: Long = 0L) {
        if (sessionUnavailable) {
            toast(getString(R.string.session_heartbeat_stale))
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
                api.issueSessionRelayToken(baseUrl, accountId, deviceId, runtimeId, sessionId)
            }.onSuccess { refresh ->
                relayToken = refresh.relayToken
                viewerPublicKey = refresh.viewerPublicKey
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

    private fun confirmSessionShutdown() {
        if (endingSession) {
            return
        }
        MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.session_shutdown_confirm_title))
            .setMessage(getString(R.string.session_shutdown_confirm_body))
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.session_disconnect)) { _, _ ->
                endSessionAndFinish()
            }
            .show()
    }

    private fun endSessionAndFinish(reason: String) {
        if (endingSession) {
            return
        }

        endingSession = true
        appLogs.info("Session shutdown requested: $reason", "session")

        binding.sessionSubtitleText.text = getString(R.string.session_secure_saving)
        binding.sessionStreamStatusText.text = getString(R.string.session_shutdown_status)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionRetryButton.isVisible = false
        setSessionActionsEnabled(false)

        val missingContext = missingShutdownContext()
        if (missingContext != null) {
            endingSession = false
            setSessionActionsEnabled(true)
            showEndingError(IOException(missingContext))
            return
        }

        lifecycleScope.launch {
            runCatching {
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                queueRuntimeStop(blobAccessKey)
                identityPasswordStore.saveConfigured(accountId, deviceId)
            }.onSuccess {
                appSettings.lastSessionEndReason = reason
                disconnectViewer()
                activeSessionStore.clear()
                if (appSettings.autoClearClipboard) {
                    appSettings.clearClipboard()
                    appLogs.info("Clipboard cleared after session end", "privacy")
                }
                appLogs.info("Session shutdown queued: $reason", "session")
                toast(getString(R.string.session_stop_pending))
                finishToRuntimeList(markRuntimeStopping = true)
            }.onFailure { error ->
                endingSession = false
                startSessionHeartbeat()
                startInactivityWatch()
                setSessionActionsEnabled(true)
                showEndingError(error)
            }
        }
    }

    private fun missingShutdownContext(): String? {
        val missing = buildList {
            if (runtimeId.isBlank()) add("runtime")
            if (accountId.isBlank()) add("account")
            if (deviceId.isBlank()) add("device")
            if (baseUrl.isBlank()) add("server")
        }
        if (missing.isEmpty()) {
            return null
        }
        return getString(R.string.session_shutdown_missing_context, missing.joinToString(", "))
    }

    private suspend fun queueRuntimeStop(blobAccessKey: String) {
        var needsDirectStop = true
        if (sessionId.isNotBlank()) {
            val endResult = runCatching {
                api.endSession(baseUrl, accountId, deviceId, runtimeId, sessionId, blobAccessKey)
            }
            endResult.onSuccess { runtime ->
                needsDirectStop = !runtime.isRuntimeStopQueued()
                if (needsDirectStop) {
                    appLogs.warn(
                        "Session end did not queue runtime stop (${runtime.status}/${runtime.desiredState}/${runtime.connectionStatus}); queueing direct stop",
                        "session",
                    )
                }
            }
            endResult.onFailure { error ->
                if (!error.isGoneSessionResponse()) {
                    throw error
                }
                appLogs.warn("Session was already gone; queueing runtime stop directly", "session")
            }
        }

        if (needsDirectStop) {
            api.stopRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey)
        }
        relayToken = ""
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
        val message = error.virtroidDisplayMessage(this)
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
                audioEnabled = audioEnabled,
                cameraMode = cameraMode,
                fileMode = fileMode,
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
        if (!endingSession && !sessionUnavailable && sessionHost == null && viewerSurface != null) {
            retryViewerConnection(viewerReconnectDelayMs)
        }
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
                    api.heartbeatSession(baseUrl, accountId, deviceId, runtimeId, sessionId)
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

    private fun RuntimeSummary.isRuntimeStopQueued(): Boolean {
        val desiredStopped = desiredState.equals("stopped", ignoreCase = true)
        val lifecycleStopping = status.equals("stopping", ignoreCase = true) ||
            status.equals("stopped", ignoreCase = true) ||
            status.equals("provisioned", ignoreCase = true)
        val connectivityStopping = connectionStatus.equals("disconnecting", ignoreCase = true) ||
            connectionStatus.equals("offline", ignoreCase = true) ||
            connectionStatus.equals("disconnected", ignoreCase = true) ||
            connectionStatus.isBlank()
        return desiredStopped && (lifecycleStopping || connectivityStopping || isStoppedForSession())
    }

    private fun RuntimeSummary.isStoppedForSession(): Boolean {
        val stopped = status.equals("stopped", ignoreCase = true)
        val desiredStopped = desiredState.equals("stopped", ignoreCase = true)
        val offline = connectionStatus.isBlank() ||
            connectionStatus.equals("offline", ignoreCase = true) ||
            connectionStatus.equals("disconnected", ignoreCase = true)
        return stopped && desiredStopped && offline
    }

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
        private const val EXTRA_AUDIO_ENABLED = "audio_enabled"
        private const val EXTRA_CAMERA_MODE = "camera_mode"
        private const val EXTRA_FILE_MODE = "file_mode"
        private const val SESSION_HEARTBEAT_INTERVAL_MS = 20_000L
        private const val HEARTBEAT_RETRY_VISIBLE_THRESHOLD = 2
        private const val VIEWER_RECONNECT_INITIAL_DELAY_MS = 1_000L
        private const val VIEWER_RECONNECT_MAX_DELAY_MS = 10_000L
        private const val REQUEST_SESSION_NOTIFICATION_PERMISSION = 4182
        private const val MAX_RUNTIME_FILE_IMPORT_BYTES = 32 * 1024 * 1024
        private const val CAMERA_TARGET_WIDTH = 640
        private const val CAMERA_TARGET_HEIGHT = 480
        private const val CAMERA_FRAME_INTERVAL_MS = 200L
        private const val CAMERA_FAILURE_LIMIT = 3
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
            audioEnabled: Boolean = false,
            cameraMode: String = "disabled",
            fileMode: String = "upload-only",
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
                audioEnabled = session.audioEnabled,
                cameraMode = session.cameraMode,
                fileMode = session.fileMode,
            )
        }
    }
}
