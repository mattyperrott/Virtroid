package io.virtroid.client.security

import android.content.Context
import android.os.SystemClock

class IdentityPasswordStore(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = KeystoreEncryptedPrefs(
        appContext,
        "virtroid-identity",
        KEYSTORE_ALIAS,
    )
    private val vault = SecureLocalVault.get(appContext)

    fun isConfigured(accountId: String?, deviceId: String?): Boolean {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
        }
        val storedAccountId = when {
            vault.isUnlocked -> vault.getString(NAMESPACE, KEY_ACCOUNT_ID, null)
            vault.exists -> null
            else -> prefs.getString(KEY_ACCOUNT_ID, null)
        }
        val storedDeviceId = when {
            vault.isUnlocked -> vault.getString(NAMESPACE, KEY_DEVICE_ID, null)
            vault.exists -> null
            else -> prefs.getString(KEY_DEVICE_ID, null)
        }
        return !accountId.isNullOrBlank() &&
            !deviceId.isNullOrBlank() &&
            accountId == storedAccountId &&
            deviceId == storedDeviceId
    }

    fun saveConfigured(accountId: String, deviceId: String) {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            vault.putString(NAMESPACE, KEY_ACCOUNT_ID, accountId)
            vault.putString(NAMESPACE, KEY_DEVICE_ID, deviceId)
            prefs.clear(KEY_ACCOUNT_ID, KEY_DEVICE_ID)
            return
        }
        prefs.putStrings(
            mapOf(
                KEY_ACCOUNT_ID to accountId,
                KEY_DEVICE_ID to deviceId,
            ),
        )
    }

    fun clearConfigured() {
        if (vault.isUnlocked) {
            vault.clearNamespace(NAMESPACE)
        }
        prefs.clear(KEY_ACCOUNT_ID, KEY_DEVICE_ID)
        clearUnlocked()
    }

    fun migrateToVaultIfUnlocked() {
        if (!vault.isUnlocked) {
            return
        }
        val accountId = prefs.getString(KEY_ACCOUNT_ID, null) ?: return
        vault.putString(NAMESPACE, KEY_ACCOUNT_ID, accountId)
        vault.putString(NAMESPACE, KEY_DEVICE_ID, prefs.getString(KEY_DEVICE_ID, null))
        prefs.clear(KEY_ACCOUNT_ID, KEY_DEVICE_ID)
    }

    fun exportVaultToLegacyIfUnlocked() {
        if (!vault.isUnlocked) {
            return
        }
        prefs.putStrings(
            mapOf(
                KEY_ACCOUNT_ID to vault.getString(NAMESPACE, KEY_ACCOUNT_ID, null),
                KEY_DEVICE_ID to vault.getString(NAMESPACE, KEY_DEVICE_ID, null),
            ),
        )
    }

    fun unlock(accountId: String, deviceId: String, password: String): String {
        val blobAccessKey = IdentityCrypto.deriveBlobAccessKey(accountId, deviceId, password)
        unlockedAccountId = accountId
        unlockedDeviceId = deviceId
        unlockedBlobAccessKey = blobAccessKey
        unlockedAtElapsedMs = SystemClock.elapsedRealtime()
        return blobAccessKey
    }

    fun unlockedBlobAccessKey(accountId: String, deviceId: String): String? {
        if (unlockedAtElapsedMs == 0L || SystemClock.elapsedRealtime() - unlockedAtElapsedMs > UNLOCK_TTL_MS) {
            clearUnlocked()
            return null
        }
        return if (unlockedAccountId == accountId &&
            unlockedDeviceId == deviceId &&
            !unlockedBlobAccessKey.isNullOrBlank()
        ) {
            unlockedBlobAccessKey
        } else {
            null
        }
    }

    fun isUnlockedFor(accountId: String?, deviceId: String?): Boolean {
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            return false
        }
        return unlockedBlobAccessKey(accountId, deviceId) != null
    }

    fun clearUnlocked() {
        unlockedAccountId = null
        unlockedDeviceId = null
        unlockedBlobAccessKey = null
        unlockedAtElapsedMs = 0L
    }

    private companion object {
        const val KEY_ACCOUNT_ID = "identity_account_id"
        const val KEY_DEVICE_ID = "identity_device_id"
        const val KEYSTORE_ALIAS = "virtroid_identity_binding_prefs_v1"
        const val NAMESPACE = "identity_password"
        const val UNLOCK_TTL_MS = 10 * 60 * 1000L

        @Volatile
        var unlockedAccountId: String? = null

        @Volatile
        var unlockedDeviceId: String? = null

        @Volatile
        var unlockedBlobAccessKey: String? = null

        @Volatile
        var unlockedAtElapsedMs: Long = 0L
    }
}
