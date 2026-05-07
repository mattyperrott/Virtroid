package io.virtroid.client.security

import android.content.Context
import android.os.SystemClock

class IdentityPasswordStore(context: Context) {
    private val prefs = context.getSharedPreferences("virtroid-identity", Context.MODE_PRIVATE)

    fun isConfigured(accountId: String?, deviceId: String?): Boolean {
        val storedAccountId = prefs.getString(KEY_ACCOUNT_ID, null)
        val storedDeviceId = prefs.getString(KEY_DEVICE_ID, null)
        return !accountId.isNullOrBlank() &&
            !deviceId.isNullOrBlank() &&
            accountId == storedAccountId &&
            deviceId == storedDeviceId
    }

    fun saveConfigured(accountId: String, deviceId: String) {
        prefs.edit()
            .putString(KEY_ACCOUNT_ID, accountId)
            .putString(KEY_DEVICE_ID, deviceId)
            .apply()
    }

    fun clearConfigured() {
        prefs.edit()
            .remove(KEY_ACCOUNT_ID)
            .remove(KEY_DEVICE_ID)
            .apply()
        clearUnlocked()
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

    fun clearUnlocked() {
        unlockedAccountId = null
        unlockedDeviceId = null
        unlockedBlobAccessKey = null
        unlockedAtElapsedMs = 0L
    }

    private companion object {
        const val KEY_ACCOUNT_ID = "identity_account_id"
        const val KEY_DEVICE_ID = "identity_device_id"
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
