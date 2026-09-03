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
import android.media.MediaRecorder
import android.os.Build
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
    private var videoSize: Size = Size(1280, 720)
    private var mediaRecorder: MediaRecorder? = null
    private var recordingOutput: File? = null
    private var recordingVideo = false
    private var startingVideo = false
    private var recordVideoAudio = false
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
        recordVideoAudio = intent.getBooleanExtra(EXTRA_RECORD_AUDIO, false)

        binding.cameraCloseButton.setOnClickListener { finish() }
        binding.cameraShutterButton.setOnClickListener {
            if (recordingVideo) stopVideoRecording() else capturePhoto()
        }
        binding.cameraShutterButton.setOnLongClickListener {
            startVideoRecording()
            true
        }
        binding.cameraCaptureHint.text = getString(
            if (recordVideoAudio) R.string.session_camera_capture_hint
            else R.string.session_camera_capture_hint_no_audio,
        )
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
        videoSize = chooseVideoSize(configuration?.getOutputSizes(MediaRecorder::class.java).orEmpty())
        texture.setDefaultBufferSize(previewSize.width, previewSize.height)

        cameraThread = HandlerThread("virtroid-media-camera").also { it.start() }
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
        if (recordingVideo || startingVideo || !captureInProgress.compareAndSet(false, true)) return

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

    private fun startVideoRecording() {
        val camera = cameraDevice ?: return
        val preview = previewSurface ?: return
        if (recordingVideo || startingVideo || captureInProgress.get()) return

        startingVideo = true
        binding.cameraShutterButton.isEnabled = false
        binding.cameraProgress.isVisible = true
        val output = File.createTempFile("virtroid-video-", ".mp4", cacheDir)
        val recorder = runCatching { createVideoRecorder(output) }.getOrElse {
            output.delete()
            startingVideo = false
            fail(getString(R.string.session_video_capture_failed))
            return
        }
        recordingOutput = output
        mediaRecorder = recorder

        runCatching {
            captureSession?.stopRepeating()
            captureSession?.close()
            captureSession = null
            val recorderSurface = recorder.surface
            camera.createCaptureSession(
                SessionConfiguration(
                    SessionConfiguration.SESSION_REGULAR,
                    listOf(OutputConfiguration(preview), OutputConfiguration(recorderSurface)),
                    cameraExecutor,
                    object : CameraCaptureSession.StateCallback() {
                        override fun onConfigured(session: CameraCaptureSession) {
                            if (cameraDevice == null || !startingVideo) {
                                session.close()
                                return
                            }
                            captureSession = session
                            runCatching {
                                val request = camera.createCaptureRequest(CameraDevice.TEMPLATE_RECORD).apply {
                                    addTarget(preview)
                                    addTarget(recorderSurface)
                                    set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_VIDEO)
                                    set(CaptureRequest.CONTROL_AE_MODE, CaptureRequest.CONTROL_AE_MODE_ON)
                                }
                                session.setRepeatingRequest(request.build(), null, cameraHandler)
                                recorder.start()
                            }.onSuccess {
                                recordingVideo = true
                                startingVideo = false
                                runOnUiThread { showRecordingState() }
                            }.onFailure {
                                recordingFailed()
                            }
                        }

                        override fun onConfigureFailed(session: CameraCaptureSession) {
                            session.close()
                            recordingFailed()
                        }
                    },
                ),
            )
        }.onFailure {
            recordingFailed()
        }
    }

    @Suppress("DEPRECATION")
    private fun createVideoRecorder(output: File): MediaRecorder {
        val recorder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            MediaRecorder(this)
        } else {
            MediaRecorder()
        }
        if (recordVideoAudio) {
            recorder.setAudioSource(MediaRecorder.AudioSource.MIC)
        }
        recorder.setVideoSource(MediaRecorder.VideoSource.SURFACE)
        recorder.setOutputFormat(MediaRecorder.OutputFormat.MPEG_4)
        recorder.setOutputFile(output.absolutePath)
        recorder.setVideoEncoder(MediaRecorder.VideoEncoder.H264)
        recorder.setVideoEncodingBitRate(VIDEO_BIT_RATE)
        recorder.setVideoFrameRate(VIDEO_FRAME_RATE)
        recorder.setVideoSize(videoSize.width, videoSize.height)
        if (recordVideoAudio) {
            recorder.setAudioEncoder(MediaRecorder.AudioEncoder.AAC)
            recorder.setAudioEncodingBitRate(AUDIO_BIT_RATE)
            recorder.setAudioSamplingRate(AUDIO_SAMPLE_RATE)
        }
        recorder.setOrientationHint(jpegOrientation())
        recorder.setMaxDuration(MAX_VIDEO_DURATION_MS)
        recorder.setMaxFileSize(MAX_VIDEO_BYTES.toLong())
        recorder.setOnInfoListener { _, what, _ ->
            if (what == MediaRecorder.MEDIA_RECORDER_INFO_MAX_DURATION_REACHED ||
                what == MediaRecorder.MEDIA_RECORDER_INFO_MAX_FILESIZE_REACHED
            ) {
                runOnUiThread { stopVideoRecording() }
            }
        }
        recorder.prepare()
        return recorder
    }

    private fun showRecordingState() {
        binding.cameraProgress.isVisible = false
        binding.cameraShutterButton.isEnabled = true
        binding.cameraShutterButton.setImageResource(R.drawable.ic_stop)
        binding.cameraShutterButton.setBackgroundResource(R.drawable.bg_red_soft_circle)
        binding.cameraShutterButton.imageTintList = getColorStateList(R.color.v_danger)
        binding.cameraShutterButton.contentDescription = getString(R.string.session_camera_stop_video)
        binding.cameraCaptureHint.text = getString(R.string.session_camera_recording)
        binding.cameraRecordingStatus.isVisible = true
    }

    private fun stopVideoRecording() {
        if (!recordingVideo) return
        recordingVideo = false
        binding.cameraShutterButton.isEnabled = false
        binding.cameraProgress.isVisible = true
        runCatching { captureSession?.stopRepeating() }
        val stopped = runCatching { mediaRecorder?.stop() }.isSuccess
        releaseRecorder(deleteOutput = !stopped)
        val output = recordingOutput
        recordingOutput = null
        if (!stopped || output == null || !validVideoFile(output)) {
            output?.delete()
            fail(getString(R.string.session_video_capture_failed))
            return
        }
        val name = "Virtroid-" + PHOTO_TIME_FORMAT.format(Instant.now()) + ".mp4"
        setResult(
            Activity.RESULT_OK,
            Intent()
                .putExtra(EXTRA_CAPTURE_PATH, output.absolutePath)
                .putExtra(EXTRA_CAPTURE_NAME, name)
                .putExtra(EXTRA_CAPTURE_KIND, CAPTURE_KIND_VIDEO)
                .putExtra(EXTRA_CAPTURE_HAS_AUDIO, recordVideoAudio),
        )
        finish()
    }

    private fun validVideoFile(file: File): Boolean {
        if (!file.isFile || file.length() !in 12..MAX_VIDEO_BYTES.toLong()) return false
        return runCatching {
            file.inputStream().use { input ->
                val header = ByteArray(8)
                input.read(header) == header.size && String(header, 4, 4, Charsets.US_ASCII) == "ftyp"
            }
        }.getOrDefault(false)
    }

    private fun recordingFailed() {
        startingVideo = false
        recordingVideo = false
        releaseRecorder(deleteOutput = true)
        fail(getString(R.string.session_video_capture_failed))
    }

    private fun releaseRecorder(deleteOutput: Boolean) {
        runCatching { mediaRecorder?.reset() }
        runCatching { mediaRecorder?.release() }
        mediaRecorder = null
        if (deleteOutput) {
            recordingOutput?.delete()
            recordingOutput = null
        }
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
                        .putExtra(EXTRA_CAPTURE_NAME, name)
                        .putExtra(EXTRA_CAPTURE_KIND, CAPTURE_KIND_PHOTO),
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
        startingVideo = false
        if (recordingVideo) {
            recordingVideo = false
            runCatching { mediaRecorder?.stop() }
        }
        releaseRecorder(deleteOutput = true)
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

    private fun chooseVideoSize(sizes: Array<out Size>): Size {
        return sizes
            .filter { size ->
                size.width <= 1920 && size.height <= 1080 &&
                    size.width >= 640 && size.height >= 480
            }
            .minByOrNull { size ->
                val pixelsFrom1080p = kotlin.math.abs(size.width.toLong() * size.height - 1920L * 1080L)
                val aspectPenalty = kotlin.math.abs(size.width.toDouble() / size.height - 16.0 / 9.0)
                pixelsFrom1080p + (aspectPenalty * 1_000_000).toLong()
            }
            ?: sizes.firstOrNull()
            ?: Size(1280, 720)
    }

    companion object {
        const val EXTRA_CAPTURE_PATH = "capture_path"
        const val EXTRA_CAPTURE_NAME = "capture_name"
        const val EXTRA_CAPTURE_KIND = "capture_kind"
        const val EXTRA_CAPTURE_HAS_AUDIO = "capture_has_audio"
        const val CAPTURE_KIND_PHOTO = "photo"
        const val CAPTURE_KIND_VIDEO = "video"
        private const val EXTRA_RECORD_AUDIO = "record_audio"
        private const val MAX_CAPTURE_BYTES = 16 * 1024 * 1024
        private const val MAX_VIDEO_BYTES = 32 * 1024 * 1024
        private const val MAX_VIDEO_DURATION_MS = 30_000
        private const val VIDEO_BIT_RATE = 6_000_000
        private const val VIDEO_FRAME_RATE = 30
        private const val AUDIO_BIT_RATE = 128_000
        private const val AUDIO_SAMPLE_RATE = 48_000
        private val PHOTO_TIME_FORMAT = DateTimeFormatter.ofPattern("yyyyMMdd-HHmmss").withZone(ZoneOffset.UTC)

        fun createIntent(context: Context, recordAudio: Boolean): Intent =
            Intent(context, CameraCaptureActivity::class.java)
                .putExtra(EXTRA_RECORD_AUDIO, recordAudio)
    }
}
