package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.os.Bundle
import android.os.SystemClock
import android.text.Editable
import android.text.TextWatcher
import android.util.LruCache
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.FrameLayout
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.api.AppCatalogEntry
import io.virtroid.client.api.AppCatalogPage
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenAppInstallBinding
import io.virtroid.client.security.CatalogIconPolicy
import io.virtroid.client.security.enableSecureWindow
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext
import java.io.IOException
import org.json.JSONArray
import org.json.JSONObject
import javax.net.ssl.HttpsURLConnection
import java.util.Locale
import kotlin.math.roundToInt
import io.virtroid.client.ui.Notice

/**
 * Browses the F-Droid catalog a page at a time and picks the apps a runtime
 * installs. For a new runtime it hands the picks back to the creation screen;
 * for an existing one it saves them to that runtime.
 */
class AppInstallActivity : AppCompatActivity() {
    private lateinit var binding: ScreenAppInstallBinding
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore
    private val api = VirtroidApi()
    private var runtimeId: String? = null
    private val loaded = mutableListOf<AppCatalogEntry>()
    private var nextOffset: Int? = null
    private var search = ""
    private val selected = linkedMapOf<String, AppCatalogEntry>()
    private val selectionIcons = mutableMapOf<String, MutableList<ImageView>>()
    private val selectionRows = mutableMapOf<String, MutableList<View>>()
    private var searchJob: Job? = null
    private var pageJob: Job? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenAppInstallBinding.inflate(layoutInflater)
        setContentView(binding.root)
        binding.appsCard.clipToOutline = true
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        appLogs = AppLogStore.get(this)
        runtimeId = intent.getStringExtra(EXTRA_RUNTIME_ID)?.takeIf { it.isNotBlank() }

        ViewCompat.setOnApplyWindowInsetsListener(binding.appInstallRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.appInstallSubtitle.text = if (runtimeId == null) {
            getString(R.string.app_install_subtitle_new)
        } else {
            getString(
                R.string.app_install_subtitle_runtime,
                intent.getStringExtra(EXTRA_RUNTIME_NAME).orEmpty().ifBlank { getString(R.string.app_install_subtitle) },
            )
        }
        binding.buttonBack.setOnClickListener { finish() }
        binding.doneButton.setOnClickListener { finishSelection() }
        binding.loadMoreButton.setOnClickListener { loadPage(append = true) }
        binding.appSearchInput.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                scheduleSearch(s?.toString().orEmpty())
            }
            override fun afterTextChanged(s: Editable?) = Unit
        })

        decodePicks(intent.getStringExtra(EXTRA_PICKS)).forEach { selected[it.packageName] = it }
        renderLoading()
        loadPage(append = false)
        runtimeId?.let(::loadRuntimeSelections)
    }

    override fun onDestroy() {
        searchJob?.cancel()
        pageJob?.cancel()
        super.onDestroy()
    }

    private fun scheduleSearch(text: String) {
        searchJob?.cancel()
        searchJob = lifecycleScope.launch {
            delay(SEARCH_DEBOUNCE_MS)
            search = text.trim()
            loadPage(append = false)
        }
    }

    /** Loads the first page of the current search, or the next one with [append]. */
    private fun loadPage(append: Boolean) {
        val offset = if (append) nextOffset ?: return else 0
        val query = search
        val baseUrl = sessionStore.baseUrl
        pageJob?.cancel()
        binding.loadMoreButton.isEnabled = false
        binding.loadMoreButton.text = getString(R.string.app_install_loading_more)
        pageJob = lifecycleScope.launch {
            runCatching {
                cachedFirstPage(baseUrl, query, offset) ?: api.listAppCatalogPage(baseUrl, query, offset).also {
                    if (query.isEmpty() && offset == 0) firstPageCache[baseUrl] = SystemClock.elapsedRealtime() to it
                }
            }.onSuccess { page ->
                if (!append) loaded.clear()
                loaded.addAll(page.items.filter { app -> loaded.none { it.packageName == app.packageName } })
                nextOffset = page.nextOffset
                renderCatalog()
            }.onFailure { error ->
                if (error is kotlinx.coroutines.CancellationException) return@launch
                appLogs.error("App catalog load failed: ${error.message}", "apps")
                if (append) {
                    toast(error.virtroidDisplayMessage(this@AppInstallActivity))
                    renderCatalog()
                } else {
                    renderError()
                }
            }
        }
    }

    private fun cachedFirstPage(baseUrl: String, query: String, offset: Int): AppCatalogPage? {
        if (query.isNotEmpty() || offset != 0) return null
        val (storedAt, page) = firstPageCache[baseUrl] ?: return null
        return page.takeIf { SystemClock.elapsedRealtime() - storedAt < FIRST_PAGE_CACHE_MS }
    }

    private fun loadRuntimeSelections(runtimeId: String) {
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) return
        binding.doneButton.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                api.getRuntimeApps(sessionStore.baseUrl, accountId, deviceId, runtimeId)
            }.onSuccess { apps ->
                selected.clear()
                apps.forEach { selected[it.packageName] = it }
                binding.doneButton.isEnabled = true
                renderCatalog()
            }.onFailure { error ->
                appLogs.error("Runtime apps load failed: ${error.message}", "apps")
                toast(error.virtroidDisplayMessage(this@AppInstallActivity))
                renderError()
            }
        }
    }

    private fun finishSelection() {
        val runtimeId = runtimeId
        if (runtimeId == null) {
            setResult(RESULT_OK, Intent().putExtra(EXTRA_PICKS, encodePicks(selected.values)))
            finish()
            return
        }
        val accountId = sessionStore.accountId.orEmpty()
        val deviceId = sessionStore.deviceId.orEmpty()
        if (accountId.isBlank() || deviceId.isBlank()) {
            finish()
            return
        }
        binding.doneButton.isEnabled = false
        lifecycleScope.launch {
            runCatching {
                api.updateRuntimeApps(sessionStore.baseUrl, accountId, deviceId, runtimeId, selected.keys)
            }.onSuccess { apps ->
                appLogs.info("Runtime app selections updated: ${apps.size} apps", "apps")
                toast(getString(R.string.app_install_saved))
                setResult(RESULT_OK)
                finish()
            }.onFailure { error ->
                binding.doneButton.isEnabled = true
                appLogs.error("App selection save failed: ${error.message}", "apps")
                toast(error.virtroidDisplayMessage(this@AppInstallActivity))
            }
        }
    }

    private fun renderLoading() {
        binding.selectedList.removeAllViews()
        binding.recommendedList.removeAllViews()
        binding.allAvailableList.removeAllViews()
        binding.selectedHeader.isVisible = false
        binding.recommendedHeader.isVisible = false
        binding.allAvailableHeader.isVisible = false
        binding.loadMoreButton.isVisible = false
        binding.appEmptyText.isVisible = true
        binding.appEmptyText.text = getString(R.string.app_install_loading)
    }

    private fun renderError() {
        binding.recommendedList.removeAllViews()
        binding.allAvailableList.removeAllViews()
        binding.recommendedHeader.isVisible = false
        binding.allAvailableHeader.isVisible = false
        binding.loadMoreButton.isVisible = false
        binding.appEmptyText.isVisible = true
        binding.appEmptyText.text = getString(R.string.account_apps_catalog_unavailable)
        renderSelected()
    }

    private fun renderCatalog() {
        val recommended = loaded.filter { it.recommended }
        val available = loaded.filterNot { it.recommended }

        binding.recommendedHeader.isVisible = recommended.isNotEmpty()
        binding.allAvailableHeader.isVisible = available.isNotEmpty()
        binding.recommendedCount.text = resources.getQuantityString(
            R.plurals.app_install_count,
            recommended.size,
            recommended.size,
        )
        binding.allAvailableCount.text = resources.getQuantityString(
            if (nextOffset != null) R.plurals.app_install_count_more else R.plurals.app_install_count,
            available.size,
            available.size,
        )
        binding.appEmptyText.isVisible = loaded.isEmpty()
        binding.appEmptyText.text = getString(R.string.app_install_empty)
        binding.loadMoreButton.isVisible = nextOffset != null
        binding.loadMoreButton.isEnabled = true
        binding.loadMoreButton.text = getString(R.string.app_install_load_more)

        selectionIcons.clear()
        selectionRows.clear()
        renderSelected()
        renderAppRows(binding.recommendedList, recommended)
        renderAppRows(binding.allAvailableList, available)
    }

    private fun renderSelected() {
        val apps = selected.values.toList()
        binding.selectedHeader.isVisible = apps.isNotEmpty()
        binding.selectedCount.text = getString(R.string.app_install_selected_count, apps.size, MAX_RUNTIME_APPS)
        selectionIcons.values.forEach { icons -> icons.removeAll { it.parent?.parent == binding.selectedList } }
        selectionRows.values.forEach { rows -> rows.removeAll { it.parent == binding.selectedList } }
        renderAppRows(binding.selectedList, apps)
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
                toggleSelection(app)
            }
        }
        selectionRows.getOrPut(app.packageName) { mutableListOf() }.add(row)
        updateSelectionAccessibility(row, app, app.packageName in selected)

        val iconShell = FrameLayout(this).apply {
            layoutParams = LinearLayout.LayoutParams(dp(46), dp(46))
            background = ContextCompat.getDrawable(context, R.drawable.bg_surface_light_24)
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
            selectionIcons.getOrPut(app.packageName) { mutableListOf() }.add(this)
            updateSelectionIcon(this, app.packageName in selected)
        })

        return row
    }

    private fun toggleSelection(app: AppCatalogEntry) {
        val packageName = app.packageName
        if (packageName in selected) {
            selected.remove(packageName)
        } else {
            if (selected.size >= MAX_RUNTIME_APPS) {
                toast(getString(R.string.app_install_limit_reached, MAX_RUNTIME_APPS))
                return
            }
            selected[packageName] = app
        }
        val isSelected = packageName in selected
        selectionIcons[packageName]?.forEach { updateSelectionIcon(it, isSelected) }
        selectionRows[packageName]?.forEach { updateSelectionAccessibility(it, app, isSelected) }
        renderSelected()
    }

    private fun updateSelectionAccessibility(view: View, app: AppCatalogEntry, selected: Boolean) {
        val state = getString(
            if (selected) R.string.app_install_selected else R.string.app_install_not_selected,
        )
        view.contentDescription = getString(
            R.string.app_install_accessibility_selection,
            app.displayName,
            state,
        )
        ViewCompat.setStateDescription(view, state)
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
        iconCache.get(url)?.let { bitmap ->
            image.setImageBitmap(bitmap)
            image.visibility = View.VISIBLE
            placeholder.visibility = View.GONE
            return
        }

        lifecycleScope.launch {
            val bitmap = withContext(Dispatchers.IO) {
                iconDownloadSemaphore.withPermit {
                    runCatching { downloadCatalogIcon(url) }.getOrNull()
                }
            } ?: return@launch
            iconCache.put(url, bitmap)
            if (image.tag == url) {
                image.setImageBitmap(bitmap)
                image.visibility = View.VISIBLE
                placeholder.visibility = View.GONE
            }
        }
    }

    private fun downloadCatalogIcon(rawUrl: String): Bitmap {
        val url = CatalogIconPolicy.validatedUrl(rawUrl)
            ?: throw IOException("Catalog icon URL is not allowlisted")
        val connection = (url.openConnection() as? HttpsURLConnection)
            ?: throw IOException("Catalog icon connection is not HTTPS")
        return try {
            connection.instanceFollowRedirects = false
            connection.connectTimeout = ICON_CONNECT_TIMEOUT_MS
            connection.readTimeout = ICON_READ_TIMEOUT_MS
            connection.setRequestProperty("Accept", "image/png, image/jpeg, image/webp")

            if (connection.responseCode != HttpsURLConnection.HTTP_OK) {
                throw IOException("Catalog icon request returned HTTP ${connection.responseCode}")
            }
            val contentLength = connection.contentLengthLong
            if (!CatalogIconPolicy.allowsContent(connection.contentType, contentLength)) {
                throw IOException("Catalog icon response metadata was rejected")
            }
            val encoded = connection.inputStream.use { input ->
                val expectedLength = contentLength.toInt()
                val bytes = ByteArray(expectedLength)
                var offset = 0
                while (offset < expectedLength) {
                    val read = input.read(bytes, offset, expectedLength - offset)
                    if (read < 0) {
                        throw IOException("Catalog icon response ended before Content-Length")
                    }
                    offset += read
                }
                if (input.read() != -1) {
                    throw IOException("Catalog icon response exceeded Content-Length")
                }
                bytes
            }

            val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeByteArray(encoded, 0, encoded.size, bounds)
            if (!CatalogIconPolicy.allowsDecodedBitmap(bounds.outWidth, bounds.outHeight)) {
                throw IOException("Catalog icon dimensions exceeded policy")
            }
            val bitmap = BitmapFactory.decodeByteArray(
                encoded,
                0,
                encoded.size,
                BitmapFactory.Options().apply { inPreferredConfig = Bitmap.Config.ARGB_8888 },
            ) ?: throw IOException("Catalog icon could not be decoded")
            if (!CatalogIconPolicy.allowsDecodedBitmap(
                    bitmap.width,
                    bitmap.height,
                    bitmap.allocationByteCount,
                )
            ) {
                bitmap.recycle()
                throw IOException("Decoded catalog icon exceeded memory policy")
            }
            bitmap
        } finally {
            connection.disconnect()
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
        Notice.show(this, message)
    }

    companion object {
        private const val ICON_CONNECT_TIMEOUT_MS = 5_000
        private const val ICON_READ_TIMEOUT_MS = 5_000
        private const val ICON_DOWNLOAD_CONCURRENCY = 4
        private const val SEARCH_DEBOUNCE_MS = 260L
        private const val FIRST_PAGE_CACHE_MS = 10 * 60_000L
        /** What the server lets one runtime select. */
        const val MAX_RUNTIME_APPS = 32

        private const val EXTRA_RUNTIME_ID = "runtime_id"
        private const val EXTRA_RUNTIME_NAME = "runtime_name"
        const val EXTRA_PICKS = "app_picks"

        // Kept for the app's lifetime, so reopening the catalog needs no
        // downloads for what was already seen.
        private val iconCache = object : LruCache<String, Bitmap>(CatalogIconPolicy.MAX_CACHE_BYTES) {
            override fun sizeOf(key: String, value: Bitmap): Int = value.allocationByteCount.coerceAtLeast(1)
        }
        private val iconDownloadSemaphore = Semaphore(ICON_DOWNLOAD_CONCURRENCY)
        private val firstPageCache = java.util.concurrent.ConcurrentHashMap<String, Pair<Long, AppCatalogPage>>()

        /** Picks apps for a runtime not yet created; the result carries [EXTRA_PICKS]. */
        fun pickIntent(context: Context, picks: Collection<AppCatalogEntry>): Intent =
            Intent(context, AppInstallActivity::class.java).putExtra(EXTRA_PICKS, encodePicks(picks))

        /** Edits the apps selected for an existing runtime. */
        fun runtimeIntent(context: Context, runtimeId: String, runtimeName: String): Intent =
            Intent(context, AppInstallActivity::class.java)
                .putExtra(EXTRA_RUNTIME_ID, runtimeId)
                .putExtra(EXTRA_RUNTIME_NAME, runtimeName)

        fun encodePicks(picks: Collection<AppCatalogEntry>): String = JSONArray().apply {
            picks.forEach { app ->
                put(
                    JSONObject()
                        .put("package_name", app.packageName)
                        .put("display_name", app.displayName)
                        .put("summary", app.summary)
                        .put("icon_url", app.iconUrl.orEmpty())
                        .put("version_name", app.versionName)
                        .put("apk_size_bytes", app.apkSizeBytes)
                        .put("recommended", app.recommended),
                )
            }
        }.toString()

        fun decodePicks(raw: String?): List<AppCatalogEntry> {
            val array = runCatching { JSONArray(raw.orEmpty()) }.getOrNull() ?: return emptyList()
            return (0 until array.length()).mapNotNull { index ->
                val item = array.optJSONObject(index) ?: return@mapNotNull null
                val packageName = item.optString("package_name").takeIf { it.isNotBlank() } ?: return@mapNotNull null
                AppCatalogEntry(
                    packageName = packageName,
                    source = "fdroid",
                    displayName = item.optString("display_name").ifBlank { packageName },
                    summary = item.optString("summary"),
                    iconUrl = item.optString("icon_url").takeIf { it.isNotBlank() },
                    versionName = item.optString("version_name"),
                    versionCode = 0L,
                    apkSizeBytes = item.optLong("apk_size_bytes", 0L),
                    minSdk = 0,
                    nativeCode = null,
                    recommended = item.optBoolean("recommended", false),
                    selected = true,
                    catalogUpdatedAt = null,
                )
            }
        }
    }
}
