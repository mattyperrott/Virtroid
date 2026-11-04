package io.virtroid.client.security

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import io.virtroid.client.data.AppLogStore

private const val MIN_CLEAR_AFTER_MS = 5_000L

// Past this a clip that can't be read is cleared without knowing whose it is.
// Long enough to have pasted it elsewhere, and rarely in the way of a later copy.
internal const val CLIP_BACKSTOP_AFTER_MS = 10 * 60_000L

internal enum class ClipClearStep { CLEAR, WAIT, FORGET }

/**
 * A clip of ours waiting to be cleared. Each clip is known by the time Android
 * stamped on it, which tells it from a later copy of the same text without
 * keeping the text. [stamp] is null if the clip couldn't be read back. Times
 * are on the clock of [SystemClock.elapsedRealtime].
 */
internal data class PendingClipClear(
    val stamp: Long?,
    val clearAt: Long,
    val backstopAt: Long,
) {
    /**
     * What to do now. [currentStamp] is the clip Android shows, and null if the
     * clipboard is empty; it means nothing unless [readable].
     */
    fun next(readable: Boolean, currentStamp: Long?, now: Long): ClipClearStep = when {
        !readable -> if (now >= backstopAt) ClipClearStep.CLEAR else ClipClearStep.WAIT
        currentStamp == null || currentStamp != stamp -> ClipClearStep.FORGET
        now >= clearAt -> ClipClearStep.CLEAR
        else -> ClipClearStep.WAIT
    }

    companion object {
        fun after(copiedAt: Long, stamp: Long?, clearAfterMs: Long): PendingClipClear {
            val clearAt = copiedAt + clearAfterMs.coerceAtLeast(MIN_CLEAR_AFTER_MS)
            return PendingClipClear(stamp, clearAt, maxOf(clearAt, copiedAt + CLIP_BACKSTOP_AFTER_MS))
        }
    }
}

/**
 * Clears the last clip from [copySensitiveToClipboard], which may be long after
 * the app has lost sight of the clipboard: Android 10+ shows it only to an app
 * with input focus, while clearing it is always allowed. So a clip that is due
 * waits for one of the app's screens to regain focus, when it can be checked,
 * and goes unchecked only at the backstop, by an alarm in case Android has
 * killed the app or frozen its timers by then.
 *
 * The clip is only ever read through its description: reading the clip itself
 * would show "pasted from your clipboard" whenever it had become another app's.
 * Reads the Auto-clear setting nowhere: it can't be read while the vault is
 * locked, so the setting decides at the copy, and turning it off calls [cancel].
 * Main thread only.
 */
internal class ClipboardAutoClear private constructor(context: Context) {
    private val appContext = context.applicationContext
    private val clipboard = appContext.getSystemService(ClipboardManager::class.java)
    private val alarms = appContext.getSystemService(AlarmManager::class.java)
    private val prefs = appContext.getSharedPreferences("virtroid-clipboard-clear", Context.MODE_PRIVATE)
    private val main = Handler(Looper.getMainLooper())
    private val onTimer = Runnable { settle(focused = false) }

    /** Takes over from any earlier clip, which the copy has just replaced. */
    fun track(clearAfterMs: Long) {
        val pending = PendingClipClear.after(
            SystemClock.elapsedRealtime(),
            clipboard.primaryClipDescription?.timestamp,
            clearAfterMs,
        )
        save(pending)
        armTimers(pending)
        // No need to wake the phone: nothing can read the clipboard while it sleeps.
        alarms.setAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME, pending.backstopAt, backstopIntent())
    }

    fun cancel() {
        main.removeCallbacks(onTimer)
        alarms.cancel(backstopIntent())
        prefs.edit().clear().apply()
    }

    fun onFocusGained() = settle(focused = true)

    fun onBackstop() = settle(focused = false)

    private fun settle(focused: Boolean) {
        val pending = load() ?: return
        val description = clipboard.primaryClipDescription
        // Android answers an app it hides the clipboard from as if it were empty.
        val readable = description != null || focused || Build.VERSION.SDK_INT < Build.VERSION_CODES.Q
        when (pending.next(readable, description?.timestamp, SystemClock.elapsedRealtime())) {
            ClipClearStep.CLEAR -> {
                // Clearing, unlike copying an empty clip, does not make Android 13+
                // show another "Copied" confirmation.
                clipboard.clearPrimaryClip()
                cancel()
                AppLogStore.get(appContext).info(
                    if (readable) "Clipboard cleared" else "Clipboard cleared unchecked at the backstop",
                    "privacy",
                )
            }
            ClipClearStep.FORGET -> cancel()
            ClipClearStep.WAIT -> armTimers(pending)
        }
    }

    private fun armTimers(pending: PendingClipClear) {
        main.removeCallbacks(onTimer)
        val now = SystemClock.elapsedRealtime()
        listOf(pending.clearAt, pending.backstopAt).filter { it > now }.forEach {
            main.postDelayed(onTimer, it - now)
        }
    }

    private fun backstopIntent(): PendingIntent = PendingIntent.getBroadcast(
        appContext,
        0,
        Intent(appContext, ClipboardBackstopReceiver::class.java),
        PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
    )

    private fun save(pending: PendingClipClear) {
        prefs.edit().apply {
            clear()
            pending.stamp?.let { putLong(KEY_STAMP, it) }
            putLong(KEY_CLEAR_AT, pending.clearAt)
            putLong(KEY_BACKSTOP_AT, pending.backstopAt)
        }.apply()
    }

    private fun load(): PendingClipClear? {
        if (!prefs.contains(KEY_BACKSTOP_AT)) {
            return null
        }
        return PendingClipClear(
            stamp = if (prefs.contains(KEY_STAMP)) prefs.getLong(KEY_STAMP, 0L) else null,
            clearAt = prefs.getLong(KEY_CLEAR_AT, 0L),
            backstopAt = prefs.getLong(KEY_BACKSTOP_AT, 0L),
        )
    }

    companion object {
        private const val KEY_STAMP = "stamp"
        private const val KEY_CLEAR_AT = "clear_at"
        private const val KEY_BACKSTOP_AT = "backstop_at"

        @Volatile
        private var instance: ClipboardAutoClear? = null

        fun get(context: Context): ClipboardAutoClear =
            instance ?: synchronized(this) {
                instance ?: ClipboardAutoClear(context).also { instance = it }
            }
    }
}

class ClipboardBackstopReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        ClipboardAutoClear.get(context).onBackstop()
    }
}
