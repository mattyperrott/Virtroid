package io.virtroid.client.security

import android.content.Context
import android.os.SystemClock
import android.util.Base64
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Arrays
import javax.crypto.Cipher
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.PBEKeySpec

class AppLockStore(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext.getSharedPreferences("virtroid-lock", Context.MODE_PRIVATE)
    private val appSettings = AppSettingsStore(appContext)
    private val appLogs = AppLogStore.get(appContext)
    private val biometricVaultKeyStore = BiometricVaultKeyStore(appContext)

    fun hasCredential(): Boolean = mode != null && !credentialHash.isNullOrBlank() && !salt.isNullOrBlank()

    fun saveCredential(mode: LockMode, secret: String) {
        val saltBytes = ByteArray(16).also { SecureRandom().nextBytes(it) }
        val salt = Base64.encodeToString(saltBytes, B64_FLAGS)
        val vaultSalt = newVaultSalt()
        val hash = deriveCredentialHash(saltBytes, secret)
        if (!SecureLocalVault.get(appContext).unlockOrCreate(secret, vaultSalt)) {
            throw IllegalStateException("could not initialize secure local vault")
        }
        prefs.edit()
            .putString(KEY_MODE, mode.value)
            .putString(KEY_SALT, salt)
            .putString(KEY_VAULT_SALT, vaultSalt)
            .putString(KEY_HASH, hash)
            .putString(KEY_KDF, KDF_VERSION)
            .putBoolean(KEY_ENABLED, true)
            .remove(KEY_FAILED_ATTEMPTS)
            .remove(KEY_LOCKED_UNTIL)
            .apply()
        biometricVaultKeyStore.clear()
        appSettings.biometricUnlockEnabled = false
        LocalVaultMigration.migrateUnlocked(appContext)
        markUnlocked()
        appLogs.info("App lock credential configured", "auth")
    }

    fun verify(secret: String): Boolean {
        val mode = mode ?: return false
        val salt = salt ?: return false
        val hash = credentialHash ?: return false
        if (isTemporarilyLocked()) {
            appLogs.warn("Unlock attempt blocked by temporary lockout", "auth")
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
            if (kdfVersion != KDF_VERSION) {
                saveCredential(mode, secret)
                return true
            }
            if (!unlockVault(secret)) {
                appLogs.error("Secure local vault could not be decrypted", "auth")
                return false
            }
            markUnlocked()
            LocalVaultMigration.migrateUnlocked(appContext)
        } else {
            recordFailure()
        }
        return matches && mode.value.isNotBlank()
    }

    fun isUnlocked(): Boolean = unlockedInProcess

    fun markUnlocked(): Boolean {
        val vault = SecureLocalVault.get(appContext)
        if (vault.exists && !vault.isUnlocked) {
            appLogs.warn("Unlock rejected because secure local vault is still locked", "auth")
            return false
        }
        unlockedInProcess = true
        prefs.edit().putLong(KEY_LAST_UNLOCK_AT, System.currentTimeMillis()).apply()
        appLogs.info("App unlock succeeded", "auth")
        return true
    }

    fun clearUnlocked() {
        unlockedInProcess = false
        SecureLocalVault.get(appContext).lock()
    }

    fun isEnabled(): Boolean {
        return prefs.getBoolean(KEY_ENABLED, hasCredential())
    }

    fun setEnabled(enabled: Boolean) {
        if (!enabled && SecureLocalVault.get(appContext).isUnlocked) {
            LocalVaultMigration.exportUnlockedToLegacy(appContext)
            SecureLocalVault.get(appContext).destroy()
        }
        if (!enabled) {
            biometricVaultKeyStore.clear()
            appSettings.biometricUnlockEnabled = false
        }
        prefs.edit().putBoolean(KEY_ENABLED, enabled).apply()
        if (!enabled) {
            clearUnlocked()
        }
    }

    fun clearCredential() {
        SecureLocalVault.get(appContext).destroy()
        biometricVaultKeyStore.clear()
        appSettings.biometricUnlockEnabled = false
        prefs.edit().clear().apply()
        clearUnlocked()
        appLogs.warn("App lock credential cleared with local identity reset", "auth")
    }

    fun shouldRequireUnlockOnLaunch(): Boolean {
        return isEnabled() && hasCredential() && !isUnlocked()
    }

    fun noteAppBackgrounded() {
        prefs.edit().putLong(KEY_LAST_BACKGROUND_AT, SystemClock.elapsedRealtime()).apply()
    }

    fun shouldLockAfterBackground(): Boolean {
        if (!isEnabled() || !hasCredential()) {
            return false
        }
        if (appSettings.requireUnlockOnResume) {
            return true
        }
        val lastBackground = prefs.getLong(KEY_LAST_BACKGROUND_AT, 0L)
        if (lastBackground <= 0L) {
            return false
        }
        val elapsed = SystemClock.elapsedRealtime() - lastBackground
        return elapsed >= appSettings.autoLockTimeoutMs
    }

    fun lockoutRemainingMs(): Long {
        return (prefs.getLong(KEY_LOCKED_UNTIL, 0L) - SystemClock.elapsedRealtime()).coerceAtLeast(0L)
    }

    fun lastUnlockAtMs(): Long {
        return prefs.getLong(KEY_LAST_UNLOCK_AT, 0L)
    }

    var mode: LockMode?
        get() = prefs.getString(KEY_MODE, null)?.let(LockMode::fromValue)
        set(value) = prefs.edit().putString(KEY_MODE, value?.value).apply()

    fun canUseBiometricUnlock(): Boolean = biometricVaultKeyStore.hasWrappedKey()

    fun biometricEncryptCipher(): Cipher? {
        val vault = SecureLocalVault.get(appContext)
        if (!vault.isUnlocked) {
            return null
        }
        return biometricVaultKeyStore.createEncryptCipher()
    }

    fun saveBiometricVaultKey(cipher: Cipher): Boolean {
        val key = SecureLocalVault.get(appContext).currentKeyCopy() ?: return false
        return try {
            val saved = biometricVaultKeyStore.saveWrappedKey(cipher, key)
            if (saved) {
                appSettings.biometricUnlockEnabled = true
                appLogs.info("Biometric local vault unlock enabled", "auth")
            }
            saved
        } finally {
            Arrays.fill(key, 0)
        }
    }

    fun biometricDecryptCipher(): Cipher? {
        return if (canUseBiometricUnlock()) {
            biometricVaultKeyStore.createDecryptCipher()
        } else {
            null
        }
    }

    fun unlockWithBiometric(cipher: Cipher?): Boolean {
        cipher ?: return false
        val key = biometricVaultKeyStore.unwrapVaultKey(cipher) ?: return false
        return try {
            if (!SecureLocalVault.get(appContext).unlockWithKey(key)) {
                appLogs.error("Biometric vault key could not decrypt local vault", "auth")
                false
            } else {
                clearFailures()
                markUnlocked()
            }
        } finally {
            Arrays.fill(key, 0)
        }
    }

    fun clearBiometricUnlock() {
        biometricVaultKeyStore.clear()
        appSettings.biometricUnlockEnabled = false
        appLogs.info("Biometric local vault unlock disabled", "auth")
    }

    private val credentialHash: String?
        get() = prefs.getString(KEY_HASH, null)

    private val salt: String?
        get() = prefs.getString(KEY_SALT, null)

    private val kdfVersion: String?
        get() = prefs.getString(KEY_KDF, null)

    private fun unlockVault(secret: String): Boolean {
        val vaultSalt = existingOrNewVaultSalt()
        val vault = SecureLocalVault.get(appContext)
        return vault.unlockOrCreate(secret, vaultSalt)
    }

    private fun existingOrNewVaultSalt(): String {
        prefs.getString(KEY_VAULT_SALT, null)?.takeIf { it.isNotBlank() }?.let { return it }
        val salt = newVaultSalt()
        prefs.edit().putString(KEY_VAULT_SALT, salt).apply()
        return salt
    }

    private fun newVaultSalt(): String {
        val bytes = ByteArray(16).also { SecureRandom().nextBytes(it) }
        return Base64.encodeToString(bytes, B64_FLAGS)
    }

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
        appLogs.warn("App unlock failed ($attempts/${appSettings.failedAttemptsThreshold})", "auth")
        if (attempts >= appSettings.failedAttemptsThreshold) {
            editor
                .putInt(KEY_FAILED_ATTEMPTS, 0)
                .putLong(KEY_LOCKED_UNTIL, SystemClock.elapsedRealtime() + LOCKOUT_MS)
            appLogs.error("App lockout applied after failed unlock attempts", "auth")
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
        const val KEY_ENABLED = "lock_enabled"
        const val KEY_SALT = "lock_salt"
        const val KEY_VAULT_SALT = "lock_vault_salt"
        const val KEY_HASH = "lock_hash"
        const val KEY_KDF = "lock_kdf"
        const val KEY_FAILED_ATTEMPTS = "lock_failed_attempts"
        const val KEY_LOCKED_UNTIL = "lock_locked_until"
        const val KEY_LAST_BACKGROUND_AT = "lock_last_background_at"
        const val KEY_LAST_UNLOCK_AT = "lock_last_unlock_at"
        const val KDF_VERSION = "pbkdf2-sha256-v1"
        const val PBKDF2_ITERATIONS = 210_000
        const val KEY_BYTES = 32
        const val LOCKOUT_MS = 30_000L
        const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE

        @Volatile
        var unlockedInProcess: Boolean = false
    }
}

private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
