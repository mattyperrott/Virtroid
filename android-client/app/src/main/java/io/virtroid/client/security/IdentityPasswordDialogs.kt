package io.virtroid.client.security

import android.app.Dialog
import android.graphics.Color
import android.text.InputType
import android.text.method.PasswordTransformationMethod
import android.view.WindowManager
import androidx.appcompat.app.AppCompatActivity
import androidx.core.graphics.drawable.toDrawable
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import io.virtroid.client.R
import io.virtroid.client.databinding.DialogIdentityPasswordSetupBinding
import io.virtroid.client.databinding.DialogIdentityRecoveryBinding
import io.virtroid.client.databinding.DialogSecureTextEntryBinding
import kotlinx.coroutines.suspendCancellableCoroutine
import java.util.UUID
import kotlin.coroutines.resume

data class IdentityRecoveryInput(
    val accountId: String,
    val password: String,
)

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
    binding.dialogInputLayout.configureAccessiblePasswordToggle(binding.dialogInput)

    val dialog = Dialog(this).apply {
        setContentView(binding.root)
        setCancelable(true)
        window?.setBackgroundDrawable(Color.TRANSPARENT.toDrawable())
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

suspend fun AppCompatActivity.promptIdentityPasswordSetup(): String? =
    suspendCancellableCoroutine { continuation ->
        val binding = DialogIdentityPasswordSetupBinding.inflate(layoutInflater)
        binding.passwordInput.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        binding.passwordConfirmInput.inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        binding.passwordInputLayout.configureAccessiblePasswordToggle(binding.passwordInput)
        binding.passwordConfirmInputLayout.configureAccessiblePasswordToggle(binding.passwordConfirmInput)

        val dialog = Dialog(this).apply {
            setContentView(binding.root)
            setCancelable(true)
            window?.setBackgroundDrawable(Color.TRANSPARENT.toDrawable())
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
            when (IdentityPasswordPolicy.violation(first)) {
                IdentityPasswordPolicy.Violation.EMPTY -> {
                    binding.passwordInputLayout.error = getString(R.string.identity_password_required)
                }
                IdentityPasswordPolicy.Violation.TOO_SHORT -> {
                    binding.passwordInputLayout.error = getString(
                        R.string.identity_password_too_short,
                        IdentityPasswordPolicy.MIN_LENGTH,
                    )
                }
                IdentityPasswordPolicy.Violation.TOO_LONG -> {
                    binding.passwordInputLayout.error = getString(
                        R.string.identity_password_too_long,
                        IdentityPasswordPolicy.MAX_LENGTH,
                    )
                }
                null -> when {
                    first != confirmed -> {
                        binding.passwordConfirmInputLayout.error = getString(R.string.identity_password_mismatch)
                    }
                    else -> finishWith(first)
                }
            }
        }
        dialog.setOnCancelListener { finishWith(null) }
        dialog.show()
        binding.passwordInput.requestFocus()
        dialog.window?.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_STATE_VISIBLE)
        continuation.invokeOnCancellation { dialog.dismiss() }
    }

suspend fun AppCompatActivity.promptIdentityRecovery(): IdentityRecoveryInput? =
    suspendCancellableCoroutine { continuation ->
        val binding = DialogIdentityRecoveryBinding.inflate(layoutInflater)
        binding.recoveryPasswordInput.inputType =
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD
        binding.recoveryPasswordInputLayout.configureAccessiblePasswordToggle(binding.recoveryPasswordInput)

        val dialog = Dialog(this).apply {
            setContentView(binding.root)
            setCancelable(true)
            window?.setBackgroundDrawable(Color.TRANSPARENT.toDrawable())
            window?.setLayout(
                WindowManager.LayoutParams.MATCH_PARENT,
                WindowManager.LayoutParams.WRAP_CONTENT,
            )
        }

        fun finishWith(value: IdentityRecoveryInput?) {
            if (continuation.isActive) {
                continuation.resume(value)
            }
            dialog.dismiss()
        }

        binding.recoveryCancelButton.setOnClickListener { finishWith(null) }
        binding.recoveryConfirmButton.setOnClickListener {
            val accountId = binding.recoveryAccountInput.text?.toString()?.trim().orEmpty()
            val password = binding.recoveryPasswordInput.text?.toString()?.trim().orEmpty()
            val normalizedAccountId = runCatching { UUID.fromString(accountId).toString() }.getOrNull()
            when {
                normalizedAccountId == null -> {
                    binding.recoveryAccountInputLayout.error =
                        getString(R.string.identity_recovery_account_invalid)
                }
                password.isBlank() -> {
                    binding.recoveryPasswordInputLayout.error =
                        getString(R.string.identity_password_required)
                }
                else -> finishWith(IdentityRecoveryInput(normalizedAccountId, password))
            }
        }
        dialog.setOnCancelListener { finishWith(null) }
        dialog.show()
        binding.recoveryAccountInput.requestFocus()
        dialog.window?.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_STATE_VISIBLE)
        continuation.invokeOnCancellation { dialog.dismiss() }
    }

private fun TextInputLayout.configureAccessiblePasswordToggle(input: TextInputEditText) {
    fun updateDescription() {
        val passwordIsVisible = input.transformationMethod == null
        endIconContentDescription = context.getString(
            if (passwordIsVisible) R.string.password_hide else R.string.password_show,
        )
    }

    setEndIconOnClickListener {
        val selectionStart = input.selectionStart
        val selectionEnd = input.selectionEnd
        input.transformationMethod = if (input.transformationMethod == null) {
            PasswordTransformationMethod.getInstance()
        } else {
            null
        }
        if (selectionStart >= 0 && selectionEnd >= 0) {
            input.setSelection(selectionStart, selectionEnd)
        }
        updateDescription()
    }
    updateDescription()
}
