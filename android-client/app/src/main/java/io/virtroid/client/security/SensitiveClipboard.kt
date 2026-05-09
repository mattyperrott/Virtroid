package io.virtroid.client.security

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.os.Handler
import android.os.Looper
import io.virtroid.client.data.AppSettingsStore

fun Context.copySensitiveToClipboard(
    label: String,
    value: String,
    clearAfterMs: Long = 30_000L,
) {
    val appContext = applicationContext
    val clipboard = getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager ?: return
    clipboard.setPrimaryClip(ClipData.newPlainText(label, value))
    if (!AppSettingsStore(appContext).autoClearClipboard) {
        return
    }
    Handler(Looper.getMainLooper()).postDelayed({
        val current = clipboard.primaryClip ?: return@postDelayed
        val currentLabel = current.description?.label?.toString().orEmpty()
        val currentText = current.getItemAt(0)?.coerceToText(appContext)?.toString().orEmpty()
        if (currentLabel == label && currentText == value) {
            clipboard.setPrimaryClip(ClipData.newPlainText("", ""))
        }
    }, clearAfterMs.coerceAtLeast(5_000L))
}
