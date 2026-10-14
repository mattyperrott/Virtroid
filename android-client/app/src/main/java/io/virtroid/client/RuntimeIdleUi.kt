package io.virtroid.client

import android.app.Activity
import android.content.Context
import io.virtroid.client.api.RuntimeIdlePeriods
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
 * Asks how long the runtime keeps running with no viewer open. [extra] adds
 * actions below the choices, such as stopping now.
 */
internal fun Activity.showIdlePeriodPicker(
    selectedSeconds: Int,
    extra: (VirtroidSheet) -> Unit = {},
    onPick: (Int) -> Unit,
) {
    val sheet = VirtroidSheet(this)
        .title(getString(R.string.idle_period_title))
        .message(getString(R.string.idle_period_body))
    RuntimeIdlePeriods.SECONDS.forEach { seconds ->
        val selected = seconds == selectedSeconds
        sheet.row(
            icon = if (selected) R.drawable.ic_check else R.drawable.ic_radio_off,
            title = idlePeriodLabel(seconds),
            action = if (selected) getString(R.string.idle_period_current) else null,
            dimmed = !selected,
        ) { if (!selected) onPick(seconds) }
    }
    extra(sheet)
    sheet.note(getString(R.string.idle_period_note))
        .secondary(getString(R.string.controls_cancel))
        .show()
}
