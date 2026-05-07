package io.virtroid.client.security

import android.app.Dialog
import android.graphics.Color
import android.graphics.drawable.ColorDrawable
import android.text.InputType
import android.view.WindowManager
import androidx.appcompat.app.AppCompatActivity
import io.virtroid.client.R
import io.virtroid.client.databinding.DialogIdentityPasswordSetupBinding
import io.virtroid.client.databinding.DialogSecureTextEntryBinding
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlin.coroutines.resume

private suspend fun AppCompatActivity.promptSecureEntry(
    title: String,
    body: String,
    fieldHint: String,
    inputType: Int,
): String? = suspendCancellableCoroutine { continuation ->
    val binding = DialogSecureTextEntryBinding.inflate(layoutInflater)
    binding.dialogTitleText.text = title
    binding.dialogBodyText.text = body
    binding.dialogInputLayout.hint = fieldHint
    binding.dialogInput.inputType = inputType

    val dialog = Dialog(this).apply {
        setContentView(binding.root)
        setCancelable(true)
        window?.setBackgroundDrawable(ColorDrawable(Color.TRANSPARENT))
        window?.setLayout(
            WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
        )
    }

    fun finishWith(value: String?) {
        if (continuation.isActive) {
            continuation.resume(value)
        }
        dialog.dismiss()
    }

    binding.dialogCancelButton.setOnClickListener { finishWith(null) }
    binding.dialogConfirmButton.setOnClickListener {
        finishWith(binding.dialogInput.text?.toString().orEmpty())
    }
    dialog.setOnCancelListener { finishWith(null) }
    dialog.show()
    binding.dialogInput.requestFocus()
    dialog.window?.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_STATE_VISIBLE)
    continuation.invokeOnCancellation { dialog.dismiss() }
}

suspend fun AppCompatActivity.promptIdentityPassword(
    title: String,
    hint: String,
): String? = promptSecureEntry(
    title = title,
    body = hint,
    fieldHint = getString(R.string.identity_password_field_hint),
    inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD,
)

suspend fun AppCompatActivity.promptPinCode(
    title: String,
    hint: String,
): String? = promptSecureEntry(
    title = title,
    body = hint,
    fieldHint = getString(R.string.lock_pin_field_hint),
    inputType = InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_VARIATION_PASSWORD,
)

suspend fun AppCompatActivity.promptPassphrase(
    title: String,
    hint: String,
): String? = promptSecureEntry(
    title = title,
    body = hint,
    fieldHint = getString(R.string.lock_passphrase_field_hint),
    inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD,
)

suspend fun AppCompatActivity.promptIdentityPasswordWithConfirmation(
    title: String,
    hint: String,
    confirmHint: String,
): String? {
    val first = promptIdentityPassword(title, hint)?.trim().orEmpty()
    if (first.isBlank()) {
        return null
    }
    val confirmation = promptIdentityPassword(title, confirmHint)?.trim().orEmpty()
    return if (first == confirmation) first else ""
}

suspend fun AppCompatActivity.promptIdentityPasswordSetup(): String? =
    suspendCancellableCoroutine { continuation ->
        val binding = DialogIdentityPasswordSetupBinding.inflate(layoutInflater)
        binding.passwordInput.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        binding.passwordConfirmInput.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD

        val dialog = Dialog(this).apply {
            setContentView(binding.root)
            setCancelable(true)
            window?.setBackgroundDrawable(ColorDrawable(Color.TRANSPARENT))
            window?.setLayout(
                WindowManager.LayoutParams.MATCH_PARENT,
                WindowManager.LayoutParams.WRAP_CONTENT,
            )
        }

        fun finishWith(value: String?) {
            if (continuation.isActive) {
                continuation.resume(value)
            }
            dialog.dismiss()
        }

        binding.passwordDialogCancelButton.setOnClickListener { finishWith(null) }
        binding.passwordDialogConfirmButton.setOnClickListener {
            val first = binding.passwordInput.text?.toString()?.trim().orEmpty()
            val confirmed = binding.passwordConfirmInput.text?.toString()?.trim().orEmpty()
            when {
                first.isBlank() -> binding.passwordInputLayout.error = getString(R.string.identity_password_required)
                first != confirmed -> binding.passwordConfirmInputLayout.error = getString(R.string.identity_password_mismatch)
                else -> finishWith(first)
            }
        }
        dialog.setOnCancelListener { finishWith(null) }
        dialog.show()
        binding.passwordInput.requestFocus()
        dialog.window?.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_STATE_VISIBLE)
        continuation.invokeOnCancellation { dialog.dismiss() }
    }
