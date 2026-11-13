package io.virtroid.client.security

import android.app.Activity
import android.app.Dialog
import android.content.Context
import android.view.Window
import android.view.WindowManager
import androidx.appcompat.app.AlertDialog
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.data.AppSettingsStore

fun Activity.enableSecureWindow() {
    WindowCompat.setDecorFitsSystemWindows(window, false)
    WindowInsetsControllerCompat(window, window.decorView).apply {
        hide(WindowInsetsCompat.Type.statusBars() or WindowInsetsCompat.Type.navigationBars())
        systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
    }
    applyScreenCaptureProtection()
}

fun Activity.applyScreenCaptureProtection() {
    window.applyScreenCaptureProtection(this)
}

/** Keeps this window out of screenshots, recordings and casts while the app's setting asks for it. */
fun Window.applyScreenCaptureProtection(context: Context) {
    if (AppSettingsStore(context).blockScreenCapture) {
        setFlags(
            WindowManager.LayoutParams.FLAG_SECURE,
            WindowManager.LayoutParams.FLAG_SECURE,
        )
    } else {
        clearFlags(WindowManager.LayoutParams.FLAG_SECURE)
    }
}

/**
 * The app's dialogs. A dialog has a window of its own, which the activity's
 * FLAG_SECURE doesn't cover, so each one takes the screen-capture setting
 * itself before it is first shown. Every dialog goes through this class or
 * [ProtectedAlertDialogBuilder] (DialogProtectionTest checks).
 */
class ProtectedDialog(context: Context) : Dialog(context) {
    init {
        window?.applyScreenCaptureProtection(context)
    }
}

/** [MaterialAlertDialogBuilder] whose dialogs take the screen-capture setting, as [ProtectedDialog]. */
class ProtectedAlertDialogBuilder(context: Context) : MaterialAlertDialogBuilder(context) {
    override fun create(): AlertDialog = super.create().apply {
        window?.applyScreenCaptureProtection(context)
    }
}
