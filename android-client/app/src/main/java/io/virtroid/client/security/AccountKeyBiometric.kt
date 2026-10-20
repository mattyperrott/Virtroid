package io.virtroid.client.security

import android.content.Context
import androidx.appcompat.app.AppCompatActivity
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import io.virtroid.client.ui.Notice
import io.virtroid.client.R
import kotlinx.coroutines.suspendCancellableCoroutine
import javax.crypto.Cipher
import kotlin.coroutines.resume

/**
 * An optional copy of the account master key that a fingerprint opens instead
 * of the password. The copy is sealed by an Android Keystore key that needs a
 * strong biometric check for every use and is destroyed when a fingerprint is
 * added to the phone. The account and device are bound in as associated data,
 * so the copy only ever opens as this account's key on this device. The
 * password still opens the key, and is the only way to recover the account.
 */
class AccountKeyBiometricStore(context: Context) {
    private val keys = BiometricVaultKeyStore(context, PREFS_NAME, KEY_ALIAS)

    fun isEnabled(): Boolean = keys.hasWrappedKey()

    fun encryptCipher(): Cipher? = keys.createEncryptCipher()

    /** Null when the copy is gone, including when new fingerprints destroyed its key. */
    fun decryptCipher(): Cipher? = keys.createDecryptCipher()

    fun save(cipher: Cipher, accountId: String, deviceId: String, blobAccessKey: String): Boolean {
        if (!IdentityCrypto.isValidBlobAccessKey(blobAccessKey)) return false
        val secret = blobAccessKey.toByteArray(Charsets.UTF_8)
        try {
            return keys.saveWrappedKey(cipher, secret, binding(accountId, deviceId))
        } finally {
            secret.fill(0)
        }
    }

    /** The account key, or null (and the copy dropped) when it does not open as this account's. */
    fun open(cipher: Cipher, accountId: String, deviceId: String): String? {
        val secret = keys.unwrapVaultKey(cipher, binding(accountId, deviceId))
        val blobAccessKey = secret?.toString(Charsets.UTF_8)
        secret?.fill(0)
        if (blobAccessKey == null || !IdentityCrypto.isValidBlobAccessKey(blobAccessKey)) {
            clear()
            return null
        }
        return blobAccessKey
    }

    fun clear() = keys.clear()

    private fun binding(accountId: String, deviceId: String): ByteArray =
        "$BINDING_LABEL\n$accountId\n$deviceId".toByteArray(Charsets.UTF_8)

    private companion object {
        const val PREFS_NAME = "virtroid-biometric-account-key"
        const val KEY_ALIAS = "virtroid_account_key_biometric_v1"
        const val BINDING_LABEL = "VIRTROID-ACCOUNT-KEY-BIOMETRIC-V1"
    }
}

/** Whether this phone has a strong biometric (such as an enrolled fingerprint) to use. */
fun Context.canUseStrongBiometric(): Boolean =
    BiometricManager.from(this).canAuthenticate(BiometricManager.Authenticators.BIOMETRIC_STRONG) ==
        BiometricManager.BIOMETRIC_SUCCESS

/**
 * Asks for a fingerprint and returns [cipher] once it may be used, or null
 * when the user cancels, picks [negativeLabel] or the check fails for good.
 * Failed touches leave the prompt up for another try.
 */
suspend fun AppCompatActivity.authenticateStrongBiometric(
    cipher: Cipher,
    title: String,
    subtitle: String,
    negativeLabel: String,
): Cipher? = suspendCancellableCoroutine { continuation ->
    val prompt = BiometricPrompt(
        this,
        ContextCompat.getMainExecutor(this),
        object : BiometricPrompt.AuthenticationCallback() {
            override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                if (continuation.isActive) continuation.resume(result.cryptoObject?.cipher)
            }

            override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                if (continuation.isActive) continuation.resume(null)
            }
        },
    )
    prompt.authenticate(
        BiometricPrompt.PromptInfo.Builder()
            .setTitle(title)
            .setSubtitle(subtitle)
            .setNegativeButtonText(negativeLabel)
            .setAllowedAuthenticators(BiometricManager.Authenticators.BIOMETRIC_STRONG)
            .build(),
        BiometricPrompt.CryptoObject(cipher),
    )
    continuation.invokeOnCancellation { prompt.cancelAuthentication() }
}

/**
 * Opens the account key with a fingerprint when fingerprint unlock is on, and
 * keeps it unlocked for the usual time. Null means ask for the password.
 */
suspend fun AppCompatActivity.unlockAccountKeyWithBiometric(
    accountId: String,
    deviceId: String,
    passwordStore: IdentityPasswordStore,
): String? {
    val store = AccountKeyBiometricStore(this)
    if (!store.isEnabled()) return null
    val cipher = store.decryptCipher()
    if (cipher == null) {
        if (!store.isEnabled()) {
            Notice.show(this, getString(R.string.account_key_biometric_reset))
        }
        return null
    }
    val authenticated = authenticateStrongBiometric(
        cipher = cipher,
        title = getString(R.string.account_key_biometric_unlock_title),
        subtitle = getString(R.string.account_key_biometric_unlock_subtitle),
        negativeLabel = getString(R.string.account_key_biometric_use_password),
    ) ?: return null
    val blobAccessKey = store.open(authenticated, accountId, deviceId)
    if (blobAccessKey == null) {
        Notice.show(this, getString(R.string.account_key_biometric_reset))
        return null
    }
    return passwordStore.cacheBlobAccessKey(accountId, deviceId, blobAccessKey)
}
