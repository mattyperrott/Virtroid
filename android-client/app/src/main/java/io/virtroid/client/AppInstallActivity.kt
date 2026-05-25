package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.os.Bundle
import android.text.Editable
import android.text.TextWatcher
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import android.widget.FrameLayout
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.api.AppCatalogEntry
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSelectionStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenAppInstallBinding
import io.virtroid.client.security.enableSecureWindow
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.net.URL
import java.util.Locale
import kotlin.math.roundToInt

class AppInstallActivity : AppCompatActivity() {
    private lateinit var binding: ScreenAppInstallBinding
    private lateinit var sessionStore: SessionStore
    private lateinit var appSelectionStore: AppSelectionStore
    private lateinit var appLogs: AppLogStore
    private val api = VirtroidApi()
    private var catalog: List<AppCatalogEntry> = emptyList()
    private val selectedPackages = linkedSetOf<String>()
    private val iconCache = mutableMapOf<String, Bitmap>()
    private var searchJob: Job? = null
    private var loadedInitialSelections = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenAppInstallBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        appSelectionStore = AppSelectionStore(this)
        appLogs = AppLogStore.get(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.appInstallRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.buttonBack.setOnClickListener { finish() }
        binding.doneButton.setOnClickListener { saveSelections() }
        binding.appSearchInput.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                scheduleCatalogSearch(s?.toString().orEmpty())
            }
            override fun afterTextChanged(s: Editable?) = Unit
        })

        renderLoading()
        loadCatalog("")
    }

    override fun onDestroy() {
        searchJob?.cancel()
        super.onDestroy()
    }

    private fun scheduleCatalogSearch(search: String) {
        searchJob?.cancel()
        searchJob = lifecycleScope.launch {
            delay(260L)
            loadCatalog(search, showLoading = false)
        }
    }

    private fun loadCatalog(search: String, showLoading: Boolean = true) {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            binding.doneButton.isEnabled = false
            lifecycleScope.launch {
                runCatching {
                    api.listPublicAppCatalog(sessionStore.baseUrl, search)
                }.onSuccess { items ->
                    catalog = items
                    if (!loadedInitialSelections) {
                        selectedPackages.clear()
                        selectedPackages.addAll(appSelectionStore.pendingSelections())
                        loadedInitialSelections = true
                    }
                    binding.doneButton.isEnabled = true
                    renderCatalog()
                }.onFailure { error ->
                    binding.doneButton.isEnabled = true
                    appLogs.error("Public app catalog load failed: ${error.message}", "apps")
                    toast(error.virtroidDisplayMessage(this@AppInstallActivity))
                    renderError()
                }
            }
            return
        }

        binding.doneButton.isEnabled = false
        if (showLoading) {
            renderLoading()
        }
        lifecycleScope.launch {
            runCatching {
                api.listAppCatalog(sessionStore.baseUrl, accountId, deviceId, search)
            }.onSuccess { items ->
                catalog = items
                if (!loadedInitialSelections) {
                    selectedPackages.clear()
                    selectedPackages.addAll(items.filter { it.selected }.map { it.packageName })
                    loadedInitialSelections = true
                }
                binding.doneButton.isEnabled = true
                renderCatalog()
            }.onFailure { error ->
                binding.doneButton.isEnabled = true
                appLogs.error("App catalog load failed: ${error.message}", "apps")
                toast(error.virtroidDisplayMessage(this@AppInstallActivity))
                renderError()
            }
        }
    }

    private fun saveSelections() {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            appSelectionStore.savePendingSelections(selectedPackages)
            toast(getString(R.string.app_install_saved))
            finish()
            return
        }

        binding.doneButton.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                api.updateAppSelections(
                    baseUrl = sessionStore.baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    packageNames = selectedPackages,
                )
            }.onSuccess { items ->
                catalog = items
                selectedPackages.clear()
                selectedPackages.addAll(items.filter { it.selected }.map { it.packageName })
                appLogs.warn("Account app selections updated: ${selectedPackages.size} apps", "apps")
                toast(getString(R.string.app_install_saved))
                finish()
            }.onFailure { error ->
                binding.doneButton.isEnabled = true
                appLogs.error("App selection save failed: ${error.message}", "apps")
                toast(error.virtroidDisplayMessage(this@AppInstallActivity))
            }
        }
    }

    private fun renderLoading() {
        binding.recommendedList.removeAllViews()
        binding.allAvailableList.removeAllViews()
        binding.recommendedHeader.visibility = View.GONE
        binding.allAvailableHeader.visibility = View.GONE
        binding.appEmptyText.visibility = View.VISIBLE
        binding.appEmptyText.text = getString(R.string.app_install_loading)
    }

    private fun renderError() {
        binding.recommendedList.removeAllViews()
        binding.allAvailableList.removeAllViews()
        binding.recommendedHeader.visibility = View.GONE
        binding.allAvailableHeader.visibility = View.GONE
        binding.appEmptyText.visibility = View.VISIBLE
        binding.appEmptyText.text = getString(R.string.account_apps_catalog_unavailable)
    }

    private fun renderCatalog() {
        val query = binding.appSearchInput.text?.toString().orEmpty().trim().lowercase(Locale.US)
        val filtered = catalog.filter { app ->
            query.isBlank() ||
                app.displayName.lowercase(Locale.US).contains(query) ||
                app.packageName.lowercase(Locale.US).contains(query) ||
                app.summary.lowercase(Locale.US).contains(query)
        }
        val recommended = filtered.filter { it.recommended }
        val available = filtered.filterNot { it.recommended }

        binding.recommendedHeader.visibility = if (recommended.isNotEmpty()) View.VISIBLE else View.GONE
        binding.allAvailableHeader.visibility = if (available.isNotEmpty()) View.VISIBLE else View.GONE
        binding.recommendedCount.text = getString(R.string.app_install_count, recommended.size)
        binding.allAvailableCount.text = getString(R.string.app_install_count, available.size)
        binding.appEmptyText.visibility = if (filtered.isEmpty()) View.VISIBLE else View.GONE
        binding.appEmptyText.text = getString(R.string.app_install_empty)

        renderAppRows(binding.recommendedList, recommended)
        renderAppRows(binding.allAvailableList, available)
    }

    private fun renderAppRows(container: LinearLayout, apps: List<AppCatalogEntry>) {
        container.removeAllViews()
        apps.forEach { app ->
            container.addView(divider())
            container.addView(appRow(app))
        }
    }

    private fun appRow(app: AppCatalogEntry): View {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = android.view.Gravity.CENTER_VERTICAL
            setPadding(dp(22), dp(14), dp(22), dp(14))
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                dp(76),
            )
            isClickable = true
            isFocusable = true
            setOnClickListener {
                toggleSelection(app.packageName)
            }
        }

        val iconShell = FrameLayout(this).apply {
            layoutParams = LinearLayout.LayoutParams(dp(46), dp(46))
            background = getDrawable(R.drawable.bg_surface_light_24)
        }
        val iconPlaceholder = TextView(this).apply {
            layoutParams = FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
            gravity = android.view.Gravity.CENTER
            text = initials(app.displayName)
            setTextColor(getColor(R.color.v_accent))
            textSize = 14f
            typeface = android.graphics.Typeface.DEFAULT_BOLD
            includeFontPadding = false
        }
        val appIcon = ImageView(this).apply {
            layoutParams = FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
            scaleType = ImageView.ScaleType.CENTER_CROP
            visibility = View.GONE
        }
        iconShell.addView(iconPlaceholder)
        iconShell.addView(appIcon)
        row.addView(iconShell)
        loadAppIcon(app.iconUrl, appIcon, iconPlaceholder)

        val textColumn = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = android.view.Gravity.CENTER_VERTICAL
            layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.MATCH_PARENT, 1f).apply {
                marginStart = dp(18)
                marginEnd = dp(12)
            }
        }
        textColumn.addView(TextView(this).apply {
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            )
            text = app.displayName
            setTextColor(getColor(R.color.v_text_primary))
            textSize = 17f
            typeface = android.graphics.Typeface.DEFAULT_BOLD
            includeFontPadding = false
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.END
        })
        textColumn.addView(TextView(this).apply {
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            ).apply {
                topMargin = dp(8)
            }
            text = getString(
                R.string.app_install_version_size,
                app.versionName.ifBlank { "--" },
                formatSize(app.apkSizeBytes),
            )
            setTextColor(getColor(R.color.v_text_muted))
            textSize = 13f
            includeFontPadding = false
            maxLines = 1
            ellipsize = android.text.TextUtils.TruncateAt.END
        })
        row.addView(textColumn)

        row.addView(ImageView(this).apply {
            layoutParams = LinearLayout.LayoutParams(dp(34), dp(34))
            updateSelectionIcon(this, app.packageName in selectedPackages)
        })

        return row
    }

    private fun toggleSelection(packageName: String) {
        if (packageName in selectedPackages) {
            selectedPackages.remove(packageName)
        } else {
            selectedPackages.add(packageName)
        }
        renderCatalog()
    }

    private fun updateSelectionIcon(view: ImageView, selected: Boolean) {
        if (selected) {
            view.setImageResource(R.drawable.ic_check)
            view.setBackgroundResource(R.drawable.bg_accent_circle)
            view.setColorFilter(getColor(R.color.virtroid_on_primary))
            view.setPadding(dp(8), dp(8), dp(8), dp(8))
        } else {
            view.setImageResource(R.drawable.ic_radio_off)
            view.setBackgroundResource(R.drawable.bg_transparent)
            view.setColorFilter(getColor(R.color.v_border))
            view.setPadding(dp(5), dp(5), dp(5), dp(5))
        }
    }

    private fun loadAppIcon(iconUrl: String?, image: ImageView, placeholder: TextView) {
        val url = iconUrl?.takeIf { it.isNotBlank() } ?: return
        image.tag = url
        iconCache[url]?.let { bitmap ->
            image.setImageBitmap(bitmap)
            image.visibility = View.VISIBLE
            placeholder.visibility = View.GONE
            return
        }

        lifecycleScope.launch {
            val bitmap = withContext(Dispatchers.IO) {
                runCatching {
                    val connection = URL(url).openConnection().apply {
                        connectTimeout = 5000
                        readTimeout = 5000
                    }
                    connection.getInputStream().use(BitmapFactory::decodeStream)
                }.getOrNull()
            } ?: return@launch
            iconCache[url] = bitmap
            if (image.tag == url) {
                image.setImageBitmap(bitmap)
                image.visibility = View.VISIBLE
                placeholder.visibility = View.GONE
            }
        }
    }

    private fun divider(): View {
        return View(this).apply {
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                1,
            )
            setBackgroundColor(getColor(R.color.v_border))
        }
    }

    private fun initials(name: String): String {
        return name.split(" ", "-", "_")
            .mapNotNull { it.firstOrNull()?.uppercaseChar()?.toString() }
            .take(2)
            .joinToString("")
            .ifBlank { "A" }
    }

    private fun formatSize(bytes: Long): String {
        if (bytes <= 0L) {
            return "--"
        }
        val mb = bytes / (1024.0 * 1024.0)
        return if (mb >= 100) {
            "${mb.roundToInt()} MB"
        } else {
            String.format(Locale.US, "%.1f MB", mb)
        }
    }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).roundToInt()

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        fun createIntent(context: Context): Intent = Intent(context, AppInstallActivity::class.java)
    }
}
