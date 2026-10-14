package io.virtroid.client

import android.app.Activity
import android.content.Context
import android.view.Gravity
import androidx.core.view.isVisible
import io.virtroid.client.api.RuntimeIdlePeriods
import io.virtroid.client.databinding.ViewIdlePeriodSliderBinding
import io.virtroid.client.ui.VirtroidSheet

/** "30 minutes", "1 hour", … "1 week" for a period from [RuntimeIdlePeriods]. */
internal fun Context.idlePeriodLabel(seconds: Int): String {
    val minutes = seconds / 60
    return when {
        seconds >= 7 * 24 * 3600 && seconds % (7 * 24 * 3600) == 0 ->
            resources.getQuantityString(R.plurals.idle_period_weeks, seconds / (7 * 24 * 3600), seconds / (7 * 24 * 3600))
        seconds >= 2 * 24 * 3600 && seconds % (24 * 3600) == 0 ->
            resources.getQuantityString(R.plurals.idle_period_days, seconds / (24 * 3600), seconds / (24 * 3600))
        minutes >= 60 && minutes % 60 == 0 ->
            resources.getQuantityString(R.plurals.idle_period_hours, minutes / 60, minutes / 60)
        else -> resources.getQuantityString(R.plurals.idle_period_minutes, minutes, minutes)
    }
}

/** Time left as "3d 4h", "2h 13m", "12m" or "under a minute". */
internal fun Context.idleCountdown(remainingMs: Long): String {
    val totalMinutes = remainingMs / 60_000L
    val days = totalMinutes / (24 * 60)
    val hours = (totalMinutes / 60) % 24
    val minutes = totalMinutes % 60
    return when {
        totalMinutes < 1 -> getString(R.string.idle_countdown_under_minute)
        days > 0 -> getString(R.string.idle_countdown_days_hours, days, hours)
        hours > 0 -> getString(R.string.idle_countdown_hours_minutes, (totalMinutes / 60), minutes)
        else -> getString(R.string.idle_countdown_minutes, minutes)
    }
}

/**
 * Sets [picker] to choose among [RuntimeIdlePeriods], starting at
 * [selectedSeconds], and reports each period chosen to [onChange].
 */
internal fun Context.bindIdlePeriodSlider(
    picker: ViewIdlePeriodSliderBinding,
    selectedSeconds: Int,
    onChange: (Int) -> Unit = {},
) {
    val periods = RuntimeIdlePeriods.SECONDS
    val start = periods.indexOf(selectedSeconds).takeIf { it >= 0 }
        ?: periods.indexOf(RuntimeIdlePeriods.DEFAULT_SECONDS)
    picker.idlePeriodMin.text = idlePeriodLabel(periods.first())
    picker.idlePeriodMax.text = idlePeriodLabel(periods.last())
    picker.idlePeriodValue.text = idlePeriodLabel(periods[start])
    picker.idlePeriodSlider.apply {
        valueTo = (periods.size - 1).toFloat()
        value = start.toFloat()
        setLabelFormatter { idlePeriodLabel(periods[it.toInt()]) }
        addOnChangeListener { _, value, fromUser ->
            val seconds = periods[value.toInt()]
            picker.idlePeriodValue.text = idlePeriodLabel(seconds)
            if (fromUser) onChange(seconds)
        }
    }
}

/**
 * Asks how long the runtime keeps running with no viewer open, on a slider.
 * [extra] adds actions below it, such as stopping now; [onSave] gets a
 * changed period.
 */
internal fun Activity.showIdlePeriodSheet(
    selectedSeconds: Int,
    extra: (VirtroidSheet) -> Unit = {},
    onSave: (Int) -> Unit,
) {
    val picker = ViewIdlePeriodSliderBinding.inflate(layoutInflater)
    var chosen = selectedSeconds
    bindIdlePeriodSlider(picker, selectedSeconds) { chosen = it }
    // The sheet's title says what it is; the slider shows only the period.
    picker.idlePeriodIcon.isVisible = false
    picker.idlePeriodIntro.isVisible = false
    picker.idlePeriodHeader.gravity = Gravity.CENTER
    picker.idlePeriodValue.textSize = 22f
    picker.root.setPadding(0, 0, 0, (12 * resources.displayMetrics.density).toInt())
    val sheet = VirtroidSheet(this)
        .title(getString(R.string.idle_period_title))
        .message(getString(R.string.idle_period_body))
        .content(picker.root)
    extra(sheet)
    sheet.note(getString(R.string.idle_period_note))
        .secondary(getString(R.string.controls_cancel))
        .primary(getString(R.string.idle_period_save)) { if (chosen != selectedSeconds) onSave(chosen) }
        .show()
}
