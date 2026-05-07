package io.virtroid.client

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
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.RuntimeUpdate
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenCreateSessionBinding
import io.virtroid.client.device.DeviceRuntimeProfile
import io.virtroid.client.security.enableSecureWindow
import kotlinx.coroutines.launch

class NewRuntimeActivity : AppCompatActivity() {
    private lateinit var binding: ScreenCreateSessionBinding
    private val api = VirtroidApi()
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore
    private var entitlement: EntitlementSummary? = null

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
        binding.provisionRuntimeButton.isEnabled = false
        binding.cameraPassthroughSwitch.isEnabled = false
        binding.switchCameraPassthroughLabel.text = getString(R.string.new_runtime_camera_passthrough_unavailable)
        loadEntitlement()
    }

    private fun loadEntitlement() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            renderEntitlement(null)
            return
        }

        lifecycleScope.launch {
            runCatching {
                api.getEntitlement(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess {
                entitlement = it
                renderEntitlement(it)
            }.onFailure {
                appLogs.warn("Runtime entitlement unavailable: ${it.message}", "runtime")
                renderEntitlement(null)
            }
        }
    }

    private fun renderEntitlement(entitlement: EntitlementSummary?) {
        binding.newRuntimeEntitlementText.text = if (entitlement == null) {
            getString(R.string.entitlement_unavailable)
        } else {
            getString(
                R.string.entitlement_detail,
                entitlement.runtimeCount,
                entitlement.runtimeLimit,
                entitlement.runtimeStartsRemainingToday,
            )
        }
        binding.provisionRuntimeButton.isEnabled = entitlement?.canCreateRuntime ?: false
    }

    private fun provisionRuntime() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            toast(getString(R.string.new_runtime_missing_access))
            return
        }
        entitlement?.createRuntimeBlockedMessage(this)?.let {
            toast(it)
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
                        cameraMode = "disabled",
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
                binding.provisionRuntimeButton.isEnabled = entitlement?.canCreateRuntime ?: false
                toast(it.virtroidDisplayMessage(this@NewRuntimeActivity))
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
