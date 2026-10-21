package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.text.Editable
import android.text.InputType
import android.text.TextWatcher
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.data.City
import io.virtroid.client.data.CityCatalog
import io.virtroid.client.databinding.ScreenLocationPickerBinding
import io.virtroid.client.security.RuntimeRegion
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.ui.Notice
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.Locale
import kotlin.math.roundToInt

/**
 * Picks where a runtime's Android believes it is, from the city list bundled
 * with the app or typed coordinates. Nothing leaves the phone while choosing;
 * the caller seals the result for the runtime's VM.
 */
class LocationPickerActivity : AppCompatActivity() {
    private lateinit var binding: ScreenLocationPickerBinding
    private var catalog: CityCatalog? = null
    private var searchJob: Job? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenLocationPickerBinding.inflate(layoutInflater)
        setContentView(binding.root)
        binding.locationOptionsCard.clipToOutline = true
        binding.locationResultsCard.clipToOutline = true
        WindowCompat.setDecorFitsSystemWindows(window, false)
        ViewCompat.setOnApplyWindowInsetsListener(binding.locationPickerRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }

        binding.locationBackButton.setOnClickListener { finish() }
        binding.locationSearchInput.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) = Unit
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {
                scheduleSearch(s?.toString().orEmpty())
            }
            override fun afterTextChanged(s: Editable?) = Unit
        })
        renderOptions()
        lifecycleScope.launch {
            catalog = withContext(Dispatchers.IO) { CityCatalog.get(this@LocationPickerActivity) }
            search("")
        }
    }

    override fun onDestroy() {
        searchJob?.cancel()
        super.onDestroy()
    }

    private fun renderOptions() {
        val card = binding.locationOptionsCard
        card.removeAllViews()
        card.addView(optionRow(R.drawable.ic_edit, getString(R.string.location_coordinates_title), getString(R.string.location_coordinates_subtitle)) {
            showCoordinatesDialog()
        })
        if (intent.getBooleanExtra(EXTRA_HAS_REGION, false)) {
            card.addView(divider())
            card.addView(optionRow(R.drawable.ic_close, getString(R.string.location_off_title), getString(R.string.location_off_subtitle)) {
                setResult(RESULT_OK, Intent().putExtra(EXTRA_CLEAR, true))
                finish()
            })
        }
    }

    private fun scheduleSearch(text: String) {
        searchJob?.cancel()
        searchJob = lifecycleScope.launch {
            delay(SEARCH_DEBOUNCE_MS)
            search(text)
        }
    }

    private suspend fun search(query: String) {
        val catalog = catalog ?: return
        val cities = withContext(Dispatchers.Default) { catalog.search(query) }
        val card = binding.locationResultsCard
        card.removeAllViews()
        cities.forEachIndexed { index, city ->
            if (index > 0) card.addView(divider())
            card.addView(cityRow(city))
        }
        card.isVisible = cities.isNotEmpty()
        binding.locationEmptyText.isVisible = cities.isEmpty()
        binding.locationEmptyText.text = getString(R.string.location_search_empty, query.trim())
    }

    private fun cityRow(city: City): View {
        val label = city.label()
        val detail = getString(R.string.location_city_detail, coordinates(city.latitude, city.longitude), city.timezone)
        return textRow(label, detail).apply {
            contentDescription = getString(R.string.accessibility_setting_value, label, detail)
            setOnClickListener { finishWith(RuntimeRegion(city.latitude, city.longitude, city.timezone, label)) }
        }
    }

    private fun showCoordinatesDialog() {
        val latitudeInput = coordinateInput(getString(R.string.location_latitude_hint))
        val longitudeInput = coordinateInput(getString(R.string.location_longitude_hint))
        val fields = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(24), dp(8), dp(24), 0)
            addView(latitudeInput)
            addView(longitudeInput)
        }
        val dialog = MaterialAlertDialogBuilder(this)
            .setTitle(getString(R.string.location_coordinates_title))
            .setMessage(getString(R.string.location_coordinates_body))
            .setView(fields)
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.controls_confirm), null)
            .create()
        dialog.setOnShowListener {
            dialog.getButton(android.content.DialogInterface.BUTTON_POSITIVE).setOnClickListener {
                val latitude = latitudeInput.text?.toString()?.trim()?.replace(',', '.')?.toDoubleOrNull()
                val longitude = longitudeInput.text?.toString()?.trim()?.replace(',', '.')?.toDoubleOrNull()
                val nearest = if (latitude != null && longitude != null) catalog?.nearest(latitude, longitude) else null
                if (latitude == null || longitude == null || latitude !in -90.0..90.0 || longitude !in -180.0..180.0 || nearest == null) {
                    Notice.show(this, getString(R.string.location_coordinates_invalid))
                    return@setOnClickListener
                }
                dialog.dismiss()
                // Typed coordinates take the timezone of the nearest city.
                val label = getString(R.string.location_coordinates_label, coordinates(latitude, longitude), nearest.name)
                finishWith(RuntimeRegion(latitude, longitude, nearest.timezone, label))
            }
        }
        dialog.show()
    }

    private fun finishWith(region: RuntimeRegion) {
        setResult(
            RESULT_OK,
            Intent()
                .putExtra(EXTRA_LATITUDE, region.latitude)
                .putExtra(EXTRA_LONGITUDE, region.longitude)
                .putExtra(EXTRA_TIMEZONE, region.timezone)
                .putExtra(EXTRA_LABEL, region.label),
        )
        finish()
    }

    private fun coordinateInput(hintText: String) = EditText(this).apply {
        hint = hintText
        inputType = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_FLAG_DECIMAL or InputType.TYPE_NUMBER_FLAG_SIGNED
        setTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface))
        setHintTextColor(ContextCompat.getColor(context, R.color.virtroid_on_surface_muted))
    }

    private fun optionRow(icon: Int, title: String, subtitle: String, onClick: () -> Unit): View =
        textRow(title, subtitle).apply {
            addView(
                ImageView(context).apply {
                    layoutParams = LinearLayout.LayoutParams(dp(40), dp(40)).apply { marginEnd = dp(16) }
                    setPadding(dp(10), dp(10), dp(10), dp(10))
                    setImageResource(icon)
                    setColorFilter(getColor(R.color.v_accent))
                    background = ContextCompat.getDrawable(context, R.drawable.bg_accent_soft_circle)
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                },
                0,
            )
            contentDescription = getString(R.string.accessibility_setting_value, title, subtitle)
            setOnClickListener { onClick() }
        }

    private fun textRow(title: String, subtitle: String) = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        setPadding(dp(22), dp(14), dp(22), dp(14))
        minimumHeight = dp(68)
        layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
        isClickable = true
        isFocusable = true
        foreground = ContextCompat.getDrawable(
            context,
            android.util.TypedValue().also { theme.resolveAttribute(android.R.attr.selectableItemBackground, it, true) }.resourceId,
        )
        addView(
            LinearLayout(context).apply {
                orientation = LinearLayout.VERTICAL
                layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f)
                addView(
                    TextView(context).apply {
                        text = title
                        setTextAppearance(R.style.VirtroidText_CardTitle)
                        maxLines = 1
                        ellipsize = android.text.TextUtils.TruncateAt.END
                    },
                )
                addView(
                    TextView(context).apply {
                        text = subtitle
                        setTextAppearance(R.style.VirtroidText_Body_Compact)
                        maxLines = 1
                        ellipsize = android.text.TextUtils.TruncateAt.END
                        setPadding(0, dp(4), 0, 0)
                    },
                )
            },
        )
    }

    private fun divider() = View(this).apply {
        layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(1)).apply {
            marginStart = dp(22)
            marginEnd = dp(22)
        }
        setBackgroundColor(getColor(R.color.v_border_soft))
    }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).roundToInt()

    /** A place for the runtime, or no location at all. */
    sealed interface Choice {
        data class Place(val region: RuntimeRegion) : Choice
        data object Off : Choice
    }

    companion object {
        private const val SEARCH_DEBOUNCE_MS = 150L
        private const val EXTRA_HAS_REGION = "has_region"
        private const val EXTRA_CLEAR = "clear"
        private const val EXTRA_LATITUDE = "latitude"
        private const val EXTRA_LONGITUDE = "longitude"
        private const val EXTRA_TIMEZONE = "timezone"
        private const val EXTRA_LABEL = "label"

        /** [hasRegion] offers turning the location off. */
        fun intent(context: Context, hasRegion: Boolean): Intent =
            Intent(context, LocationPickerActivity::class.java).putExtra(EXTRA_HAS_REGION, hasRegion)

        /** What the picker chose, or null if it was closed without a choice. */
        fun choice(data: Intent?): Choice? {
            data ?: return null
            if (data.getBooleanExtra(EXTRA_CLEAR, false)) return Choice.Off
            val timezone = data.getStringExtra(EXTRA_TIMEZONE) ?: return null
            return Choice.Place(
                RuntimeRegion(
                    latitude = data.getDoubleExtra(EXTRA_LATITUDE, 0.0),
                    longitude = data.getDoubleExtra(EXTRA_LONGITUDE, 0.0),
                    timezone = timezone,
                    label = data.getStringExtra(EXTRA_LABEL).orEmpty(),
                ),
            )
        }

        private fun coordinates(latitude: Double, longitude: Double): String =
            String.format(Locale.ROOT, "%.4f, %.4f", latitude, longitude)
    }
}
