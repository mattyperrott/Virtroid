package io.virtroid.client

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.ImageFormat
import android.graphics.SurfaceTexture
import android.hardware.camera2.CameraCaptureSession
import android.hardware.camera2.CameraCharacteristics
import android.hardware.camera2.CameraDevice
import android.hardware.camera2.CameraManager
import android.hardware.camera2.CaptureRequest
import android.hardware.camera2.params.OutputConfiguration
import android.hardware.camera2.params.SessionConfiguration
import android.media.ImageReader
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.util.Size
import android.view.Surface
import android.view.TextureView
import android.view.WindowManager
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.isVisible
import io.virtroid.client.databinding.ScreenCameraCaptureBinding
import io.virtroid.client.security.enableSecureWindow
import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.concurrent.Executor
import java.util.concurrent.atomic.AtomicBoolean

class CameraCaptureActivity : AppCompatActivity() {
    private lateinit var binding: ScreenCameraCaptureBinding
    private var cameraThread: HandlerThread? = null
    private var cameraHandler: Handler? = null
    private var cameraDevice: CameraDevice? = null
    private var captureSession: CameraCaptureSession? = null
    private var imageReader: ImageReader? = null
    private var previewSurface: Surface? = null
    private var selectedCameraId: String = ""
    private var sensorOrientation: Int = 0
    private val captureInProgress = AtomicBoolean(false)

    private val cameraExecutor: Executor
        get() = Executor { command ->
            cameraHandler?.post(command) ?: runOnUiThread(command)
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        binding = ScreenCameraCaptureBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.cameraCloseButton.setOnClickListener { finish() }
        binding.cameraShutterButton.setOnClickListener { capturePhoto() }
        binding.cameraPreview.surfaceTextureListener = object : TextureView.SurfaceTextureListener {
            override fun onSurfaceTextureAvailable(texture: SurfaceTexture, width: Int, height: Int) {
                openCamera(texture)
            }

            override fun onSurfaceTextureSizeChanged(texture: SurfaceTexture, width: Int, height: Int) = Unit

            override fun onSurfaceTextureDestroyed(texture: SurfaceTexture): Boolean {
                closeCamera()
                return true
            }

            override fun onSurfaceTextureUpdated(texture: SurfaceTexture) = Unit
        }
    }

    override fun onResume() {
        super.onResume()
        if (binding.cameraPreview.isAvailable && cameraDevice == null) {
            openCamera(checkNotNull(binding.cameraPreview.surfaceTexture))
        }
    }

    override fun onPause() {
        closeCamera()
        super.onPause()
    }

    @SuppressLint("MissingPermission")
    private fun openCamera(texture: SurfaceTexture) {
        if (cameraDevice != null || cameraThread != null) return
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            finish()
            return
        }

        val manager = getSystemService(CameraManager::class.java)
        selectedCameraId = manager.cameraIdList.firstOrNull { id ->
            manager.getCameraCharacteristics(id).get(CameraCharacteristics.LENS_FACING) ==
                CameraCharacteristics.LENS_FACING_BACK
        } ?: manager.cameraIdList.firstOrNull().orEmpty()
        if (selectedCameraId.isBlank()) {
            fail(getString(R.string.session_camera_unavailable))
            return
        }

        val characteristics = manager.getCameraCharacteristics(selectedCameraId)
        sensorOrientation = characteristics.get(CameraCharacteristics.SENSOR_ORIENTATION) ?: 0
        val configuration = characteristics.get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP)
        val previewSize = chooseSize(configuration?.getOutputSizes(SurfaceTexture::class.java).orEmpty(), 1920, 1080)
        val photoSize = chooseSize(configuration?.getOutputSizes(ImageFormat.JPEG).orEmpty(), 4096, 4096)
        texture.setDefaultBufferSize(previewSize.width, previewSize.height)

        cameraThread = HandlerThread("virtroid-photo-camera").also { it.start() }
        cameraHandler = Handler(checkNotNull(cameraThread).looper)
        imageReader = ImageReader.newInstance(photoSize.width, photoSize.height, ImageFormat.JPEG, 2).also { reader ->
            reader.setOnImageAvailableListener({ source ->
                val image = source.acquireLatestImage() ?: return@setOnImageAvailableListener
                val bytes = image.use {
                    val buffer = it.planes.first().buffer
                    ByteArray(buffer.remaining()).also(buffer::get)
                }
                if (!captureInProgress.compareAndSet(true, false)) return@setOnImageAvailableListener
                persistAndReturn(bytes)
            }, cameraHandler)
        }

        manager.openCamera(selectedCameraId, object : CameraDevice.StateCallback() {
            override fun onOpened(camera: CameraDevice) {
                cameraDevice = camera
                createPreviewSession(camera, texture)
            }

            override fun onDisconnected(camera: CameraDevice) {
                camera.close()
                cameraDevice = null
                fail(getString(R.string.session_camera_unavailable))
            }

            override fun onError(camera: CameraDevice, error: Int) {
                camera.close()
                cameraDevice = null
                fail(getString(R.string.session_camera_unavailable))
            }
        }, cameraHandler)
    }

    private fun createPreviewSession(camera: CameraDevice, texture: SurfaceTexture) {
        val readerSurface = imageReader?.surface ?: return
        val surface = Surface(texture)
        previewSurface = surface
        val callback = object : CameraCaptureSession.StateCallback() {
            override fun onConfigured(session: CameraCaptureSession) {
                if (cameraDevice == null) return
                captureSession = session
                val request = camera.createCaptureRequest(CameraDevice.TEMPLATE_PREVIEW).apply {
                    addTarget(surface)
                    set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_PICTURE)
                    set(CaptureRequest.CONTROL_AE_MODE, CaptureRequest.CONTROL_AE_MODE_ON)
                }
                session.setRepeatingRequest(request.build(), null, cameraHandler)
                runOnUiThread {
                    binding.cameraProgress.isVisible = false
                    binding.cameraShutterButton.isEnabled = true
                }
            }

            override fun onConfigureFailed(session: CameraCaptureSession) {
                fail(getString(R.string.session_camera_unavailable))
            }
        }
        camera.createCaptureSession(
            SessionConfiguration(
                SessionConfiguration.SESSION_REGULAR,
                listOf(OutputConfiguration(surface), OutputConfiguration(readerSurface)),
                cameraExecutor,
                callback,
            ),
        )
    }

    private fun capturePhoto() {
        val camera = cameraDevice ?: return
        val session = captureSession ?: return
        val target = imageReader?.surface ?: return
        if (!captureInProgress.compareAndSet(false, true)) return

        binding.cameraShutterButton.isEnabled = false
        binding.cameraProgress.isVisible = true
        val request = camera.createCaptureRequest(CameraDevice.TEMPLATE_STILL_CAPTURE).apply {
            addTarget(target)
            set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_PICTURE)
            set(CaptureRequest.CONTROL_AE_MODE, CaptureRequest.CONTROL_AE_MODE_ON_AUTO_FLASH)
            set(CaptureRequest.JPEG_ORIENTATION, jpegOrientation())
        }
        session.capture(request.build(), object : CameraCaptureSession.CaptureCallback() {}, cameraHandler)
    }

    private fun jpegOrientation(): Int {
        @Suppress("DEPRECATION")
        val rotation = windowManager.defaultDisplay.rotation
        val displayDegrees = when (rotation) {
            Surface.ROTATION_90 -> 90
            Surface.ROTATION_180 -> 180
            Surface.ROTATION_270 -> 270
            else -> 0
        }
        return (sensorOrientation + displayDegrees + 360) % 360
    }

    private fun persistAndReturn(bytes: ByteArray) {
        if (bytes.isEmpty() || bytes.size > MAX_CAPTURE_BYTES) {
            fail(getString(R.string.session_photo_too_large))
            return
        }
        val name = "Virtroid-" + PHOTO_TIME_FORMAT.format(Instant.now()) + ".jpg"
        val output = File.createTempFile("virtroid-photo-", ".jpg", cacheDir)
        runCatching {
            output.outputStream().use { it.write(bytes) }
        }.onSuccess {
            runOnUiThread {
                setResult(
                    Activity.RESULT_OK,
                    Intent()
                        .putExtra(EXTRA_CAPTURE_PATH, output.absolutePath)
                        .putExtra(EXTRA_CAPTURE_NAME, name),
                )
                finish()
            }
        }.onFailure {
            output.delete()
            fail(getString(R.string.session_camera_capture_failed))
        }
    }

    private fun closeCamera() {
        captureInProgress.set(false)
        runCatching { captureSession?.stopRepeating() }
        captureSession?.close()
        captureSession = null
        cameraDevice?.close()
        cameraDevice = null
        imageReader?.close()
        imageReader = null
        previewSurface?.release()
        previewSurface = null
        cameraHandler = null
        cameraThread?.quitSafely()
        cameraThread = null
    }

    private fun fail(message: String) {
        runOnUiThread {
            Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
            finish()
        }
    }

    private fun chooseSize(sizes: Array<out Size>, maxWidth: Int, maxHeight: Int): Size {
        return sizes
            .filter { it.width <= maxWidth && it.height <= maxHeight }
            .maxByOrNull { it.width.toLong() * it.height.toLong() }
            ?: sizes.minByOrNull { it.width.toLong() * it.height.toLong() }
            ?: Size(1280, 720)
    }

    companion object {
        const val EXTRA_CAPTURE_PATH = "capture_path"
        const val EXTRA_CAPTURE_NAME = "capture_name"
        private const val MAX_CAPTURE_BYTES = 16 * 1024 * 1024
        private val PHOTO_TIME_FORMAT = DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss").withZone(ZoneOffset.UTC)

        fun createIntent(context: Context): Intent = Intent(context, CameraCaptureActivity::class.java)
    }
}
