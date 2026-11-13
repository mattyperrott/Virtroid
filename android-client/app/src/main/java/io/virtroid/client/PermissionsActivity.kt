package io.virtroid.client

import android.Manifest
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import androidx.activity.addCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.databinding.ScreenPermissionsBinding
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.ui.Notice
import io.virtroid.client.ui.SafeArea

/**
 * Whether Android has stopped showing its dialog for a permission, judged
 * after a request: still refused, no rationale to show, and asked for before.
 * A first request can't refuse it for good (that takes a second refusal), so
 * a first refusal with no rationale is the dialog being dismissed, which
 * Android doesn't count.
 */
internal fun androidWontAsk(granted: Boolean, showRationale: Boolean, requestedBefore: Boolean): Boolean =
    !granted && !showRationale && requestedBefore

class PermissionsActivity : AppCompatActivity() {
    private lateinit var binding: ScreenPermissionsBinding
    private lateinit var appSettings: AppSettingsStore
    private lateinit var appLogs: AppLogStore
    private var settingsMode = false

    // Permissions Android no longer shows its dialog for, as the last request
    // found: their buttons open Android settings instead of doing nothing.
    private val askedOut = mutableSetOf<String>()

    private val permissionRequest = registerForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions(),
    ) { results ->
        val requestedBefore = appSettings.permissionsRequested
        results.forEach { (permission, granted) ->
            appLogs.info("App permission ${permission.substringAfterLast('.')} granted=$granted", "permissions")
            if (androidWontAsk(granted, shouldShowRequestPermissionRationale(permission), permission in requestedBefore)) {
                askedOut += permission
            } else {
                askedOut -= permission
            }
        }
        appSettings.permissionsRequested = requestedBefore + results.keys
        if (results.keys.any(askedOut::contains)) {
            Notice.show(this, getString(R.string.permissions_android_wont_ask))
        }
        renderPermissions()
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenPermissionsBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        appSettings = AppSettingsStore(this)
        appLogs = AppLogStore.get(this)
        settingsMode = intent.getBooleanExtra(EXTRA_SETTINGS_MODE, false)

        ViewCompat.setOnApplyWindowInsetsListener(binding.permissionsRoot) { view, insets ->
            val bars = insets.getInsets(SafeArea.bars())
            view.updatePadding(left = bars.left, top = bars.top, right = bars.right, bottom = bars.bottom)
            insets
        }

        binding.permissionsBackButton.isVisible = settingsMode
        binding.permissionsBackSpacer.isVisible = settingsMode
        binding.permissionsBackButton.setOnClickListener { finish() }
        binding.notificationPermissionButton.setOnClickListener {
            requestPermissions(listOfNotNull(notificationPermission()))
        }
        binding.microphonePermissionButton.setOnClickListener {
            requestPermissions(listOf(Manifest.permission.RECORD_AUDIO))
        }
        binding.cameraPermissionButton.setOnClickListener {
            requestPermissions(listOf(Manifest.permission.CAMERA))
        }
        binding.grantPermissionsButton.setOnClickListener {
            val missing = missingPermissions()
            if (missing.isEmpty()) {
                completeSetup()
            } else {
                requestPermissions(missing)
            }
        }
        binding.continueWithoutPermissionsButton.setOnClickListener { completeSetup() }
        binding.openAndroidSettingsButton.setOnClickListener { openAndroidSettings() }
        onBackPressedDispatcher.addCallback(this) {
            if (settingsMode) finish() else completeSetup()
        }

        renderPermissions()
        appLogs.info("App permissions screen opened", "permissions")
    }

    override fun onResume() {
        super.onResume()
        if (::binding.isInitialized) {
            renderPermissions()
        }
    }

    private fun requestPermissions(permissions: List<String>) {
        val missing = permissions.filterNot(::isGranted)
        if (missing.isEmpty()) {
            renderPermissions()
            return
        }
        val askable = missing.filterNot(askedOut::contains)
        if (askable.isEmpty()) {
            openAndroidSettings()
            return
        }
        permissionRequest.launch(askable.toTypedArray())
    }

    private fun renderPermissions() {
        val notificationsGranted = notificationPermission()?.let(::isGranted) ?: true
        val microphoneAvailable = packageManager.hasSystemFeature(PackageManager.FEATURE_MICROPHONE)
        val cameraAvailable = packageManager.hasSystemFeature(PackageManager.FEATURE_CAMERA_ANY)
        val microphoneGranted = !microphoneAvailable || isGranted(Manifest.permission.RECORD_AUDIO)
        val cameraGranted = !cameraAvailable || isGranted(Manifest.permission.CAMERA)

        renderPermissionButton(
            binding.notificationPermissionButton,
            notificationsGranted,
            available = true,
            inSettings = notificationPermission()?.let(askedOut::contains) == true,
        )
        renderPermissionButton(
            binding.microphonePermissionButton,
            microphoneGranted,
            available = microphoneAvailable,
            inSettings = Manifest.permission.RECORD_AUDIO in askedOut,
        )
        renderPermissionButton(
            binding.cameraPermissionButton,
            cameraGranted,
            available = cameraAvailable,
            inSettings = Manifest.permission.CAMERA in askedOut,
        )

        val allGranted = notificationsGranted && microphoneGranted && cameraGranted
        binding.grantPermissionsButton.text = getString(
            if (allGranted) {
                if (settingsMode) R.string.permissions_done else R.string.permissions_continue
            } else {
                R.string.permissions_allow_remaining
            },
        )
        binding.continueWithoutPermissionsButton.isVisible = !allGranted
        binding.continueWithoutPermissionsButton.text = getString(
            if (settingsMode) R.string.permissions_done else R.string.permissions_not_now,
        )
    }

    private fun renderPermissionButton(
        button: com.google.android.material.button.MaterialButton,
        granted: Boolean,
        available: Boolean,
        inSettings: Boolean,
    ) {
        button.isEnabled = available && !granted
        button.text = getString(
            when {
                !available -> R.string.permissions_unavailable
                granted -> R.string.permissions_allowed
                inSettings -> R.string.permissions_in_settings
                else -> R.string.permissions_allow
            },
        )
    }

    private fun missingPermissions(): List<String> = buildList {
        notificationPermission()?.takeUnless(::isGranted)?.let(::add)
        if (packageManager.hasSystemFeature(PackageManager.FEATURE_MICROPHONE) &&
            !isGranted(Manifest.permission.RECORD_AUDIO)
        ) {
            add(Manifest.permission.RECORD_AUDIO)
        }
        if (packageManager.hasSystemFeature(PackageManager.FEATURE_CAMERA_ANY) &&
            !isGranted(Manifest.permission.CAMERA)
        ) {
            add(Manifest.permission.CAMERA)
        }
    }

    private fun notificationPermission(): String? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            Manifest.permission.POST_NOTIFICATIONS
        } else {
            null
        }

    private fun isGranted(permission: String): Boolean =
        checkSelfPermission(permission) == PackageManager.PERMISSION_GRANTED

    private fun openAndroidSettings() {
        startActivity(
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS).apply {
                data = Uri.fromParts("package", packageName, null)
            },
        )
    }

    private fun completeSetup() {
        appSettings.permissionsSetupCompleted = true
        if (settingsMode) {
            finish()
            return
        }
        startActivity(
            Intent(this, MainActivity::class.java)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
        )
        finish()
    }

    companion object {
        private const val EXTRA_SETTINGS_MODE = "io.virtroid.client.extra.PERMISSIONS_SETTINGS_MODE"

        fun createIntent(context: Context, settingsMode: Boolean = false): Intent =
            Intent(context, PermissionsActivity::class.java)
                .putExtra(EXTRA_SETTINGS_MODE, settingsMode)
    }
}
