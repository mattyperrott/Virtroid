package io.virtroid.client.security

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import android.os.PersistableBundle
import io.virtroid.client.data.AppSettingsStore

/**
 * Copies [value], marked sensitive so the system hides its preview, and clears
 * it again after [clearAfterMs] if it is still on the clipboard, or as soon as
 * [ClipboardAutoClear] can once the user has left the app. Returns true when
 * the system shows its own copy confirmation (Android 13+), so the caller should
 * not add another.
 */
fun Context.copySensitiveToClipboard(
    label: String,
    value: String,
    clearAfterMs: Long = 30_000L,
): Boolean {
    val appContext = applicationContext
    val systemConfirms = Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU
    val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager ?: return systemConfirms
    val clip = ClipData.newPlainText(label, value)
    clip.description.extras = PersistableBundle().apply {
        putBoolean(
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                ClipDescription.EXTRA_IS_SENSITIVE
            } else {
                "android.content.extra.IS_SENSITIVE"
            },
            true,
        )
    }
    clipboard.setPrimaryClip(clip)
    if (AppSettingsStore(appContext).autoClearClipboard) {
        ClipboardAutoClear.get(appContext).track(clearAfterMs)
    }
    return systemConfirms
}
