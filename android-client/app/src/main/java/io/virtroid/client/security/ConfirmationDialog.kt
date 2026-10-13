package io.virtroid.client.security

import android.app.Activity
import android.content.Context
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.R
import io.virtroid.client.ui.VirtroidSheet

/** Asks to confirm an action in the app's own dialog; [destructive] makes the confirm button red. */
fun Context.showConfirmation(
    title: String,
    message: String,
    confirmLabel: String,
    destructive: Boolean = false,
    onCancelled: () -> Unit = {},
    onConfirmed: () -> Unit,
) {
    if (this is Activity) {
        VirtroidSheet(this)
            .title(title)
            .message(message)
            .secondary(getString(R.string.controls_cancel), onCancelled)
            .primary(confirmLabel, destructive = destructive, onClick = onConfirmed)
            .onCancel(onCancelled)
            .show()
        return
    }
    val dialog = MaterialAlertDialogBuilder(this)
        .setTitle(title)
        .setMessage(message)
        .setNegativeButton(getString(R.string.controls_cancel)) { _, _ -> onCancelled() }
        .setPositiveButton(confirmLabel) { _, _ -> onConfirmed() }
        .create()
    dialog.setOnCancelListener { onCancelled() }
    dialog.show()
}
