package io.virtroid.client.security

import android.content.Context
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.client.R

fun Context.showConfirmation(
    title: String,
    message: String,
    confirmLabel: String,
    onCancelled: () -> Unit = {},
    onConfirmed: () -> Unit,
) {
    val dialog = MaterialAlertDialogBuilder(this)
        .setTitle(title)
        .setMessage(message)
        .setNegativeButton(getString(R.string.controls_cancel)) { _, _ -> onCancelled() }
        .setPositiveButton(confirmLabel) { _, _ -> onConfirmed() }
        .create()
    dialog.setOnCancelListener { onCancelled() }
    dialog.show()
}
