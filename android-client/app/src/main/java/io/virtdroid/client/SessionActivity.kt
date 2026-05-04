package io.virtdroid.client

import android.content.Context
import android.content.Intent
import android.graphics.SurfaceTexture
import android.os.Bundle
import android.view.Gravity
import android.view.KeyEvent
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
import io.virtdroid.client.api.VirtdroidApi
import io.virtdroid.client.databinding.ScreenSessionViewerBinding
import io.virtdroid.client.security.IdentityPasswordStore
import io.virtdroid.client.security.enableSecureWindow
import io.virtdroid.client.security.promptIdentityPassword
import io.virtdroid.client.viewer.ScrcpySessionHost
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.io.IOException

class SessionActivity : AppCompatActivity() {
    private lateinit var binding: ScreenSessionViewerBinding
    private var sessionHost: ScrcpySessionHost? = null
    private val api = VirtdroidApi()
    private var viewerAddress: String = ""
    private var relayHost: String = ""
    private var relayPort: Int = 0
    private var relayTls: Boolean = false
    private var relayPath: String = ""
    private var relayToken: String = ""
    private var sessionId: String = ""
    private var runtimeName: String = ""
    private var runtimeId: String = ""
    private var accountId: String = ""
    private var deviceId: String = ""
    private var baseUrl: String = ""
    private var endingSession = false
    private var viewerSurface: Surface? = null
    private lateinit var identityPasswordStore: IdentityPasswordStore

    private val sessionCallback = object : ScrcpySessionHost.Callback {
        override fun onConnected(remoteWidth: Int, remoteHeight: Int) {
            runOnUiThread {
                binding.sessionSubtitleText.text = getString(
                    R.string.session_secure_online_with_resolution,
                    remoteWidth,
                    remoteHeight,
                )
                binding.sessionStreamStatusText.text = getString(R.string.session_stream_receiving)
                applyRemoteSurfaceBounds(remoteWidth, remoteHeight)
                hideSystemBars()
            }
        }

        override fun onDisconnected(message: String) {
            runOnUiThread {
                binding.sessionSubtitleText.text = if (endingSession) {
                    getString(R.string.session_secure_saving)
                } else {
                    getString(R.string.session_failed_message_inline, message)
                }
                if (!endingSession) {
                    binding.sessionStreamStatusText.text = message
                    binding.sessionStreamProgress.isVisible = false
                    binding.sessionStreamStatusOverlay.isVisible = true
                    toast(getString(R.string.session_failed))
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
        sessionId = intent.getStringExtra(EXTRA_SESSION_ID).orEmpty()
        runtimeName = intent.getStringExtra(EXTRA_RUNTIME_NAME).orEmpty()
        runtimeId = intent.getStringExtra(EXTRA_RUNTIME_ID).orEmpty()
        accountId = intent.getStringExtra(EXTRA_ACCOUNT_ID).orEmpty()
        deviceId = intent.getStringExtra(EXTRA_DEVICE_ID).orEmpty()
        baseUrl = intent.getStringExtra(EXTRA_BASE_URL).orEmpty()
        identityPasswordStore = IdentityPasswordStore(this)

        if (relayHost.isBlank() || relayPort <= 0 || relayPath.isBlank() || relayToken.isBlank()) {
            toast(getString(R.string.session_missing_endpoint))
            finish()
            return
        }

        val title = runtimeName.ifBlank { getString(R.string.session_title_fallback) }
        binding.sessionTitleText.text = title
        binding.sessionSubtitleText.text = getString(R.string.session_connecting_short)
        binding.sessionDisconnectButton.setOnClickListener { endSessionAndFinish() }
        binding.sessionControlsButton.setOnClickListener {
            startActivity(ControlsActivity.createIntent(this, runtimeId))
        }
        binding.sessionUploadButton.isVisible = false
        binding.sessionCameraButton.isVisible = false
        binding.sessionOptionalActionsDivider.isVisible = false
        binding.sessionStreamStatusText.text = getString(R.string.session_stream_connecting)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        binding.sessionSurfaceView.isOpaque = true
        binding.sessionSurfaceView.setOnTouchListener { view, event ->
            sessionHost?.sendTouch(event, view.width, view.height) ?: false
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
                connectViewer(surface)
            }

            override fun onSurfaceTextureSizeChanged(
                surfaceTexture: SurfaceTexture,
                width: Int,
                height: Int,
            ) = Unit

            override fun onSurfaceTextureDestroyed(surfaceTexture: SurfaceTexture): Boolean {
                disconnectViewer()
                viewerSurface?.release()
                viewerSurface = null
                return true
            }

            override fun onSurfaceTextureUpdated(surfaceTexture: SurfaceTexture) {
                binding.sessionStreamStatusOverlay.isVisible = false
            }
        }
        if (binding.sessionSurfaceView.isAvailable) {
            val surface = Surface(binding.sessionSurfaceView.surfaceTexture)
            viewerSurface = surface
            connectViewer(surface)
        }
        onBackPressedDispatcher.addCallback(this) {
            endSessionAndFinish()
        }
    }

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
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

    override fun onDestroy() {
        disconnectViewer()
        super.onDestroy()
    }

    override fun onResume() {
        super.onResume()
        hideSystemBars()
    }

    private fun connectViewer(surface: Surface) {
        if (sessionHost != null) {
            return
        }

        binding.sessionSubtitleText.text = getString(R.string.session_connecting_short)
        binding.sessionStreamStatusText.text = getString(R.string.session_stream_connecting)
        binding.sessionStreamProgress.isVisible = true
        binding.sessionStreamStatusOverlay.isVisible = true
        sessionHost = ScrcpySessionHost(
            context = this,
            relayHost = relayHost,
            relayPort = relayPort,
            relayTls = relayTls,
            relayPath = relayPath,
            relayToken = relayToken,
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

    private fun endSessionAndFinish() {
        if (endingSession) {
            return
        }

        endingSession = true
        disconnectViewer()

        binding.sessionSubtitleText.text = getString(R.string.session_secure_saving)
        setSessionActionsEnabled(false)

        if (runtimeId.isBlank() || accountId.isBlank() || deviceId.isBlank() || baseUrl.isBlank()) {
            finish()
            return
        }

        lifecycleScope.launch {
            runCatching {
                if (sessionId.isNotBlank()) {
                    runCatching {
                        api.closeSession(baseUrl, accountId, deviceId, sessionId)
                    }
                    relayToken = ""
                }
                val blobAccessKey = requireBlobAccessKey(accountId, deviceId)
                api.stopRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey)
                waitForRuntimeStopped()
            }.onSuccess {
                toast(getString(R.string.session_ended))
                finish()
            }.onFailure { error ->
                if (error is StopTimeoutException) {
                    toast(getString(R.string.session_stop_pending))
                    finish()
                } else {
                    endingSession = false
                    setSessionActionsEnabled(true)
                    showEndingError(error)
                }
            }
        }
    }

    private fun sendSystemKey(keyCode: Int) {
        sessionHost?.sendKey(keyCode)
    }

    private suspend fun waitForRuntimeStopped() {
        repeat(STOP_WAIT_ATTEMPTS) { attempt ->
            val runtimes = api.listRuntimes(baseUrl, accountId, deviceId)
            val runtime = runtimes.firstOrNull { it.id == runtimeId }
                ?: return

            if (runtime.isStoppedForSession()) {
                return
            }

            val status = getString(
                R.string.status_waiting_runtime_stop,
                runtime.status.ifBlank { "stopping" },
                runtime.connectionStatus.ifBlank { "offline" },
            )
            binding.sessionSubtitleText.text = status

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
    }

    private fun hideSystemBars() {
        val controller = WindowInsetsControllerCompat(window, binding.sessionRoot)
        controller.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        controller.hide(WindowInsetsCompat.Type.systemBars())
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    private fun showEndingError(error: Throwable) {
        val message = error.message ?: getString(R.string.status_error)
        binding.sessionSubtitleText.text = message
        toast(message)
    }

    private fun setSessionActionsEnabled(isEnabled: Boolean) {
        binding.sessionDisconnectButton.isEnabled = isEnabled
        binding.sessionControlsButton.isEnabled = isEnabled
        binding.sessionUploadButton.isEnabled = isEnabled
        binding.sessionCameraButton.isEnabled = isEnabled
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

    private fun io.virtdroid.client.api.RuntimeSummary.isStoppedForSession(): Boolean {
        val stopped = status.equals("stopped", ignoreCase = true) ||
            desiredState.equals("stopped", ignoreCase = true)
        return stopped && !connectionStatus.equals("online", ignoreCase = true)
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
        private const val EXTRA_SESSION_ID = "session_id"
        private const val EXTRA_RUNTIME_ID = "runtime_id"
        private const val EXTRA_ACCOUNT_ID = "account_id"
        private const val EXTRA_DEVICE_ID = "device_id"
        private const val EXTRA_BASE_URL = "base_url"
        private const val STOP_WAIT_ATTEMPTS = 30
        private const val STOP_WAIT_DELAY_MS = 1_000L

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
                .putExtra(EXTRA_SESSION_ID, sessionId)
        }
    }
}
