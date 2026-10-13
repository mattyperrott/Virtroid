package io.virtroid.client.security

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PersistableBundle
import io.virtroid.client.data.AppSettingsStore

/**
 * Copies [value], marked sensitive so the system hides its preview, and clears
 * it again after [clearAfterMs] if it is still on the clipboard. Returns true
 * when the system shows its own copy confirmation (Android 13+), so the caller
 * should not add another.
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
    if (!AppSettingsStore(appContext).autoClearClipboard) {
        return systemConfirms
    }
    Handler(Looper.getMainLooper()).postDelayed({
        val current = clipboard.primaryClip ?: return@postDelayed
        val currentLabel = current.description?.label?.toString().orEmpty()
        val currentText = current.getItemAt(0)?.coerceToText(appContext)?.toString().orEmpty()
        if (currentLabel == label && currentText == value) {
            // Clearing, unlike copying an empty clip, does not make Android 13+
            // show another "Copied" confirmation.
            clipboard.clearPrimaryClip()
        }
    }, clearAfterMs.coerceAtLeast(5_000L))
    return systemConfirms
}
