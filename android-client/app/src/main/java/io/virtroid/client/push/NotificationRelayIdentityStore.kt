package io.virtroid.client.push

import android.content.Context
import io.virtroid.client.security.KeystoreEncryptedPrefs

class NotificationRelayIdentityStore(context: Context) {
    private val prefs = KeystoreEncryptedPrefs(context.applicationContext, PREFS_NAME, PREFS_KEY_ALIAS)

    fun save(baseUrl: String, accountId: String, deviceId: String) {
        prefs.putStrings(
            mapOf(
                KEY_BASE_URL to baseUrl.trim().trimEnd('/'),
                KEY_ACCOUNT_ID to accountId.trim(),
                KEY_DEVICE_ID to deviceId.trim(),
            ),
        )
    }

    fun load(): RelayIdentity? {
        val baseUrl = prefs.getString(KEY_BASE_URL, null)?.trim()?.trimEnd('/').orEmpty()
        val accountId = prefs.getString(KEY_ACCOUNT_ID, null)?.trim().orEmpty()
        val deviceId = prefs.getString(KEY_DEVICE_ID, null)?.trim().orEmpty()
        if (!baseUrl.startsWith("https://") || accountId.isBlank() || deviceId.isBlank()) return null
        return RelayIdentity(baseUrl, accountId, deviceId)
    }

    fun clear() {
        prefs.clear(KEY_BASE_URL, KEY_ACCOUNT_ID, KEY_DEVICE_ID)
    }

    data class RelayIdentity(
        val baseUrl: String,
        val accountId: String,
        val deviceId: String,
    )

    private companion object {
        const val PREFS_NAME = "virtroid-notification-relay-identity"
        const val PREFS_KEY_ALIAS = "virtroid_notification_relay_identity_v1"
        const val KEY_BASE_URL = "base_url"
        const val KEY_ACCOUNT_ID = "account_id"
        const val KEY_DEVICE_ID = "device_id"
    }
}
