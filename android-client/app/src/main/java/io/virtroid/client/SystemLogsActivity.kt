package io.virtroid.client

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.res.ColorStateList
import android.graphics.Typeface
import android.os.Bundle
import android.view.View
import android.view.animation.AlphaAnimation
import android.view.animation.Animation
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import io.virtroid.client.data.AppLogEntry
import io.virtroid.client.data.AppLogFilter
import io.virtroid.client.data.AppLogLevel
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.databinding.ScreenSystemLogsBinding
import io.virtroid.client.security.enableSecureWindow
import kotlinx.coroutines.launch
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

class SystemLogsActivity : AppCompatActivity() {
    private lateinit var binding: ScreenSystemLogsBinding
    private lateinit var appLogs: AppLogStore
    private var activeFilter = AppLogFilter.ALL
    private val timeFormatter = DateTimeFormatter.ofPattern("HH:mm:ss.SS")
        .withZone(ZoneId.systemDefault())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenSystemLogsBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        appLogs = AppLogStore.get(this)
        activeFilter = if (intent.getBooleanExtra(EXTRA_ERRORS_ONLY, false)) {
            AppLogFilter.ERRORS
        } else {
            AppLogFilter.ALL
        }

        ViewCompat.setOnApplyWindowInsetsListener(binding.systemLogsRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.systemLogsBackButton.setOnClickListener { finish() }
        binding.filterAllButton.setOnClickListener { setFilter(AppLogFilter.ALL) }
        binding.filterErrorsButton.setOnClickListener { setFilter(AppLogFilter.ERRORS) }
        binding.filterWarnButton.setOnClickListener { setFilter(AppLogFilter.WARN) }
        binding.copyLogsButton.setOnClickListener { copyLogs() }
        binding.systemLogsResolveButton.setOnClickListener {
            appLogs.markCriticalResolved()
            toast(getString(R.string.system_logs_acknowledged))
        }
        startLivePulse()
        renderFilterState()
        appLogs.info("System logs screen opened", "logs")
        appLogs.info("Live app log capture attached", "logs")
        renderLogs(appLogs.entries.value)

        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                appLogs.entries.collect { entries ->
                    renderLogs(entries)
                }
            }
        }
    }

    private fun setFilter(filter: AppLogFilter) {
        activeFilter = filter
        renderFilterState()
        renderLogs(appLogs.entries.value)
    }

    private fun renderFilterState() {
        val activeColor = getColor(R.color.v_text_primary)
        val inactiveMuted = getColor(R.color.v_text_muted)
        val danger = getColor(R.color.v_danger)
        val amber = getColor(R.color.v_amber)
        binding.filterAllButton.backgroundTintList = ColorStateList.valueOf(
            getColor(if (activeFilter == AppLogFilter.ALL) R.color.v_surface_light else R.color.v_surface),
        )
        binding.filterAllButton.setTextColor(if (activeFilter == AppLogFilter.ALL) activeColor else inactiveMuted)
        binding.filterErrorsButton.backgroundTintList = ColorStateList.valueOf(
            getColor(if (activeFilter == AppLogFilter.ERRORS) R.color.v_danger_card else R.color.v_danger_soft),
        )
        binding.filterErrorsButton.setTextColor(danger)
        binding.filterWarnButton.backgroundTintList = ColorStateList.valueOf(
            getColor(if (activeFilter == AppLogFilter.WARN) R.color.v_amber_soft else R.color.v_surface),
        )
        binding.filterWarnButton.setTextColor(amber)
    }

    private fun renderLogs(entries: List<AppLogEntry>) {
        val filtered = entries.filter { activeFilter.matches(it.level) }
        binding.logListContainer.removeAllViews()
        if (filtered.isEmpty()) {
            binding.logListContainer.addView(emptyText())
            return
        }
        filtered.forEach { entry ->
            binding.logListContainer.addView(logRow(entry))
        }
        binding.logScroll.post { binding.logScroll.fullScroll(View.FOCUS_DOWN) }
    }

    private fun emptyText(): TextView {
        return TextView(this).apply {
            text = getString(R.string.system_logs_empty)
            setTextColor(getColor(R.color.v_text_muted))
            textSize = 12f
            typeface = Typeface.MONOSPACE
            includeFontPadding = false
            setPadding(dp(6), dp(8), dp(6), dp(8))
        }
    }

    private fun logRow(entry: AppLogEntry): LinearLayout {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(dp(6), dp(6), dp(6), dp(6))
            val bottom = dp(6)
            layoutParams = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            ).apply { bottomMargin = bottom }
            when (entry.level) {
                AppLogLevel.WARN -> setBackgroundResource(R.drawable.bg_log_row_warning)
                AppLogLevel.ERROR,
                AppLogLevel.CRITICAL -> setBackgroundResource(R.drawable.bg_log_row_error)
                AppLogLevel.INFO -> Unit
            }
        }
        row.addView(
            TextView(this).apply {
                text = timeFormatter.format(Instant.ofEpochMilli(entry.timestampMs))
                setTextColor(levelColor(entry.level, timestamp = true))
                textSize = 11.5f
                typeface = Typeface.MONOSPACE
                includeFontPadding = false
                layoutParams = LinearLayout.LayoutParams(dp(84), LinearLayout.LayoutParams.WRAP_CONTENT)
            },
        )
        row.addView(
            TextView(this).apply {
                text = entry.message
                setTextColor(levelColor(entry.level, timestamp = false))
                textSize = 11.5f
                typeface = Typeface.MONOSPACE
                includeFontPadding = false
                setLineSpacing(0f, 1.14f)
                layoutParams = LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f)
            },
        )
        return row
    }

    private fun levelColor(level: AppLogLevel, timestamp: Boolean): Int {
        return when (level) {
            AppLogLevel.INFO -> getColor(if (timestamp) R.color.v_accent else R.color.v_text_primary)
            AppLogLevel.WARN -> getColor(R.color.v_amber)
            AppLogLevel.ERROR,
            AppLogLevel.CRITICAL -> getColor(R.color.v_danger)
        }
    }

    private fun copyLogs() {
        val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        clipboard.setPrimaryClip(ClipData.newPlainText("Virtroid logs", appLogs.exportText(activeFilter)))
        toast(getString(R.string.system_logs_copied))
    }

    private fun startLivePulse() {
        val animation = AlphaAnimation(0.35f, 1f).apply {
            duration = 650L
            repeatMode = Animation.REVERSE
            repeatCount = Animation.INFINITE
        }
        binding.liveCaptureDot.startAnimation(animation)
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

    companion object {
        private const val EXTRA_ERRORS_ONLY = "errors_only"

        fun createIntent(context: Context, errorsOnly: Boolean = false): Intent {
            return Intent(context, SystemLogsActivity::class.java)
                .putExtra(EXTRA_ERRORS_ONLY, errorsOnly)
        }
    }
}
