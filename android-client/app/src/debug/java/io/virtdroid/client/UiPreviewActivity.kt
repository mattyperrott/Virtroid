package io.virtdroid.client

import android.os.Bundle
import android.view.LayoutInflater
import android.view.WindowManager
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.core.view.isVisible
import io.virtdroid.client.databinding.RuntimeCardBinding

class UiPreviewActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        WindowInsetsControllerCompat(window, window.decorView).apply {
            hide(WindowInsetsCompat.Type.statusBars() or WindowInsetsCompat.Type.navigationBars())
            systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        }

        val screen = intent.getStringExtra(EXTRA_SCREEN)
        when (screen) {
            "privacy_security" -> {
                startActivity(PrivacySecurityActivity.createIntent(this))
                finish()
                return
            }
            "system_logs" -> {
                startActivity(SystemLogsActivity.createIntent(this))
                finish()
                return
            }
        }
        val layout = when (screen) {
            "identity_provisioning" -> R.layout.screen_identity_provisioning
            "pin_authentication" -> R.layout.screen_pin_authentication
            "my_runtimes" -> R.layout.screen_my_runtimes
            "create_session" -> R.layout.screen_create_session
            "account_identity" -> R.layout.screen_account_identity
            "fund_storage" -> R.layout.screen_fund_storage
            "send_usdt" -> R.layout.screen_send_usdt
            "session_controls" -> R.layout.screen_session_controls
            "session_viewer" -> R.layout.screen_session_viewer
            "privacy_security" -> R.layout.screen_privacy_security
            "system_logs" -> R.layout.screen_system_logs
            else -> R.layout.screen_my_runtimes
        }
        setContentView(layout)
        when (screen) {
            "my_runtimes" -> previewRuntimes()
        }
    }

    private fun previewRuntimes() {
        findViewById<TextView>(R.id.runtimeEmptyText)?.isVisible = false
        val container = findViewById<LinearLayout>(R.id.runtimeListContainer) ?: return
        container.removeAllViews()
        val inflater = LayoutInflater.from(this)
        container.addView(
            RuntimeCardBinding.inflate(inflater, container, false).apply {
                runtimeTitleText.text = getString(R.string.session_title_fallback)
                runtimeSubtitleText.text = "Online"
                runtimeStatusDot.setBackgroundResource(R.drawable.bg_dot_accent)
                runtimeStatOneValue.text = getString(R.string.runtime_stat_load_unknown)
                runtimeStatTwoValue.text = getString(R.string.runtime_stat_load_unknown)
                runtimeStatThreeValue.text = getString(R.string.runtime_stat_load_unknown)
                connectRuntimeButton.isVisible = true
                runtimeActionRow.isVisible = false
            }.root,
        )
        container.addView(
            RuntimeCardBinding.inflate(inflater, container, false).apply {
                runtimeTitleText.text = getString(R.string.session_title_fallback)
                runtimeSubtitleText.text = "Offline"
                runtimeStatusDot.setBackgroundResource(R.drawable.bg_dot_muted)
                runtimeIcon.setImageResource(R.drawable.ic_fingerprint)
                runtimeIcon.setColorFilter(getColor(R.color.v_text_muted))
                runtimeStatsRow.isVisible = false
                connectRuntimeButton.isVisible = false
                runtimeActionRow.isVisible = true
            }.root,
        )
    }

    companion object {
        private const val EXTRA_SCREEN = "screen"
    }
}
