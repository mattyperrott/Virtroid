package io.virtdroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtdroid.client.api.RuntimeUpdate
import io.virtdroid.client.api.VirtdroidApi
import io.virtdroid.client.data.AppLogStore
import io.virtdroid.client.data.SessionStore
import io.virtdroid.client.databinding.ScreenCreateSessionBinding
import io.virtdroid.client.device.DeviceRuntimeProfile
import io.virtdroid.client.security.enableSecureWindow
import kotlinx.coroutines.launch

class NewRuntimeActivity : AppCompatActivity() {
    private lateinit var binding: ScreenCreateSessionBinding
    private val api = VirtdroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenCreateSessionBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        appLogs = AppLogStore.get(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.newRuntimeRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 20 + bars.top,
                right = 24 + bars.right,
                bottom = 24 + bars.bottom,
            )
            insets
        }

        binding.newRuntimeCloseButton.setOnClickListener { finish() }
        binding.provisionRuntimeButton.setOnClickListener { provisionRuntime() }
    }

    private fun provisionRuntime() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            toast(getString(R.string.new_runtime_missing_access))
            return
        }

        val baseUrl = sessionStore.baseUrl
        val runtimeProfile = DeviceRuntimeProfile.from(this)
        val runtimeName = binding.sessionNameInput.text?.toString().orEmpty().trim()
        binding.provisionRuntimeButton.isEnabled = false
        appLogs.info("Runtime creation requested", "runtime")

        lifecycleScope.launch {
            runCatching {
                val runtime = api.createRuntime(
                    baseUrl = baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    name = runtimeName,
                    runtimeProfile = runtimeProfile,
                )
                api.updateRuntime(
                    baseUrl = baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    runtimeId = runtime.id,
                    update = RuntimeUpdate(
                        name = runtimeName.ifBlank { runtimeProfile.runtimeName },
                        androidImage = runtime.androidImage,
                        androidVersion = runtime.androidVersion,
                        widthPx = runtimeProfile.widthPx,
                        heightPx = runtimeProfile.heightPx,
                        densityDpi = runtimeProfile.densityDpi,
                        audioEnabled = binding.audioPassthroughSwitch.isChecked,
                        cameraMode = if (binding.cameraPassthroughSwitch.isChecked) {
                            "passthrough"
                        } else {
                            "disabled"
                        },
                        fileMode = "upload-only",
                        blobAutoSnapshot = true,
                        blobRetainDays = 7,
                    ),
                )
            }.onSuccess {
                appLogs.info("Runtime created", "runtime")
                toast(getString(R.string.runtime_created))
                setResult(RESULT_OK)
                finish()
            }.onFailure {
                appLogs.error(it.message ?: getString(R.string.status_error), "runtime")
                binding.provisionRuntimeButton.isEnabled = true
                toast(it.message ?: getString(R.string.status_error))
            }
        }
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        fun createIntent(context: Context): Intent = Intent(context, NewRuntimeActivity::class.java)
    }
}
