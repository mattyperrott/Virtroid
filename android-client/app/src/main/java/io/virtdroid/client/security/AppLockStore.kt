package io.virtdroid.client.security

import android.content.Context
import android.os.SystemClock
import android.util.Base64
import java.security.MessageDigest
import java.security.SecureRandom
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.PBEKeySpec

class AppLockStore(context: Context) {
    private val prefs = context.getSharedPreferences("virtdroid-lock", Context.MODE_PRIVATE)

    fun hasCredential(): Boolean = mode != null && !credentialHash.isNullOrBlank() && !salt.isNullOrBlank()

    fun saveCredential(mode: LockMode, secret: String) {
        val saltBytes = ByteArray(16).also { SecureRandom().nextBytes(it) }
        val salt = Base64.encodeToString(saltBytes, B64_FLAGS)
        val hash = deriveCredentialHash(saltBytes, secret)
        prefs.edit()
            .putString(KEY_MODE, mode.value)
            .putString(KEY_SALT, salt)
            .putString(KEY_HASH, hash)
            .putString(KEY_KDF, KDF_VERSION)
            .remove(KEY_FAILED_ATTEMPTS)
            .remove(KEY_LOCKED_UNTIL)
            .apply()
        unlockedInProcess = true
    }

    fun verify(secret: String): Boolean {
        val mode = mode ?: return false
        val salt = salt ?: return false
        val hash = credentialHash ?: return false
        if (isTemporarilyLocked()) {
            return false
        }

        val matches = if (kdfVersion == KDF_VERSION) {
            val saltBytes = runCatching { Base64.decode(salt, B64_FLAGS) }.getOrNull() ?: return false
            constantTimeEquals(deriveCredentialHash(saltBytes, secret), hash)
        } else {
            constantTimeEquals(legacyDigest(salt, secret), hash)
        }

        if (matches) {
            clearFailures()
            unlockedInProcess = true
            if (kdfVersion != KDF_VERSION) {
                saveCredential(mode, secret)
            }
        } else {
            recordFailure()
        }
        return matches && mode.value.isNotBlank()
    }

    fun isUnlocked(): Boolean = unlockedInProcess

    fun markUnlocked() {
        unlockedInProcess = true
    }

    fun clearUnlocked() {
        unlockedInProcess = false
    }

    var mode: LockMode?
        get() = prefs.getString(KEY_MODE, null)?.let(LockMode::fromValue)
        set(value) = prefs.edit().putString(KEY_MODE, value?.value).apply()

    private val credentialHash: String?
        get() = prefs.getString(KEY_HASH, null)

    private val salt: String?
        get() = prefs.getString(KEY_SALT, null)

    private val kdfVersion: String?
        get() = prefs.getString(KEY_KDF, null)

    private fun deriveCredentialHash(salt: ByteArray, secret: String): String {
        val spec = PBEKeySpec(secret.toCharArray(), salt, PBKDF2_ITERATIONS, KEY_BYTES * 8)
        val key = SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256")
            .generateSecret(spec)
            .encoded
        return Base64.encodeToString(key, B64_FLAGS)
    }

    private fun legacyDigest(saltHex: String, secret: String): String {
        val bytes = MessageDigest.getInstance("SHA-256")
            .digest((saltHex + ":" + secret).toByteArray(Charsets.UTF_8))
        return bytes.toHex()
    }

    private fun constantTimeEquals(left: String, right: String): Boolean {
        return MessageDigest.isEqual(
            left.toByteArray(Charsets.UTF_8),
            right.toByteArray(Charsets.UTF_8),
        )
    }

    private fun isTemporarilyLocked(): Boolean {
        val lockedUntil = prefs.getLong(KEY_LOCKED_UNTIL, 0L)
        return lockedUntil > SystemClock.elapsedRealtime()
    }

    private fun recordFailure() {
        val attempts = prefs.getInt(KEY_FAILED_ATTEMPTS, 0) + 1
        val editor = prefs.edit().putInt(KEY_FAILED_ATTEMPTS, attempts)
        if (attempts >= MAX_FAILED_ATTEMPTS) {
            editor
                .putInt(KEY_FAILED_ATTEMPTS, 0)
                .putLong(KEY_LOCKED_UNTIL, SystemClock.elapsedRealtime() + LOCKOUT_MS)
        }
        editor.apply()
    }

    private fun clearFailures() {
        prefs.edit()
            .remove(KEY_FAILED_ATTEMPTS)
            .remove(KEY_LOCKED_UNTIL)
            .apply()
    }

    enum class LockMode(val value: String) {
        PIN("pin"),
        PASSPHRASE("passphrase");

        companion object {
            fun fromValue(value: String): LockMode? = entries.firstOrNull { it.value == value }
        }
    }

    private companion object {
        const val KEY_MODE = "lock_mode"
        const val KEY_SALT = "lock_salt"
        const val KEY_HASH = "lock_hash"
        const val KEY_KDF = "lock_kdf"
        const val KEY_FAILED_ATTEMPTS = "lock_failed_attempts"
        const val KEY_LOCKED_UNTIL = "lock_locked_until"
        const val KDF_VERSION = "pbkdf2-sha256-v1"
        const val PBKDF2_ITERATIONS = 210_000
        const val KEY_BYTES = 32
        const val MAX_FAILED_ATTEMPTS = 5
        const val LOCKOUT_MS = 30_000L
        const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE

        @Volatile
        var unlockedInProcess: Boolean = false
    }
}

private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
