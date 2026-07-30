package io.virtroid.client.security

import android.content.Context
import android.text.InputType
import android.widget.LinearLayout
import android.widget.TextView
import androidx.core.widget.doAfterTextChanged
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import io.virtroid.client.R

fun Context.showTypedConfirmation(
    title: String,
    message: String,
    confirmationPhrase: String,
    confirmLabel: String,
    onCancelled: () -> Unit = {},
    onConfirmed: () -> Unit,
) {
    val spacing = (24 * resources.displayMetrics.density).toInt()
    val content = LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setPadding(spacing, 0, spacing, 0)
    }
    content.addView(
        TextView(this).apply {
            text = "$message\n\n${getString(R.string.typed_confirmation_instruction, confirmationPhrase)}"
        },
        LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT,
            LinearLayout.LayoutParams.WRAP_CONTENT,
        ),
    )
    val input = TextInputEditText(this).apply {
        inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_CAP_CHARACTERS
        setSingleLine(true)
    }
    val inputLayout = TextInputLayout(this).apply {
        hint = confirmationPhrase
        addView(
            input,
            LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            ),
        )
    }
    content.addView(
        inputLayout,
        LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT,
            LinearLayout.LayoutParams.WRAP_CONTENT,
        ).apply {
            topMargin = spacing
        },
    )

    val dialog = MaterialAlertDialogBuilder(this)
        .setTitle(title)
        .setView(content)
        .setNegativeButton(getString(R.string.controls_cancel)) { _, _ -> onCancelled() }
        .setPositiveButton(confirmLabel, null)
        .create()
    dialog.setOnShowListener {
        val confirmButton = dialog.getButton(android.app.AlertDialog.BUTTON_POSITIVE)
        confirmButton.isEnabled = false
        input.doAfterTextChanged { value ->
            confirmButton.isEnabled = value?.toString() == confirmationPhrase
        }
        confirmButton.setOnClickListener {
            dialog.dismiss()
            onConfirmed()
        }
        input.requestFocus()
    }
    dialog.setOnCancelListener { onCancelled() }
    dialog.show()
}
