package io.virtroid.client.data

import android.content.Context
import io.virtroid.client.BuildConfig
import io.virtroid.client.security.KeystoreEncryptedPrefs
import io.virtroid.client.security.SecureLocalVault

class SessionStore(context: Context) {
    private val appContext = context.applicationContext
    private val securePrefs = KeystoreEncryptedPrefs(
        appContext,
        "virtroid-session",
        KEYSTORE_ALIAS,
    )
    private val vault = SecureLocalVault.get(appContext)

    fun hasAccess(): Boolean = !accountId.isNullOrBlank() && !deviceId.isNullOrBlank()

    var baseUrl: String
        get() {
            if (vault.isUnlocked) {
                migrateToVaultIfUnlocked()
                return vault.getString(NAMESPACE, KEY_BASE_URL, DEFAULT_BASE_URL).orEmpty()
            }
            return if (vault.exists) {
                DEFAULT_BASE_URL
            } else {
                normalizeDefault(securePrefs.getString(KEY_BASE_URL, DEFAULT_BASE_URL), DEFAULT_BASE_URL).orEmpty()
            }
        }
        set(value) {
            if (vault.isUnlocked) {
                migrateToVaultIfUnlocked()
                vault.putString(NAMESPACE, KEY_BASE_URL, value)
                securePrefs.clear(KEY_BASE_URL)
            } else {
                securePrefs.putString(KEY_BASE_URL, value)
            }
        }

    var accountId: String?
        get() {
            if (vault.isUnlocked) {
                migrateToVaultIfUnlocked()
                return vault.getString(NAMESPACE, KEY_ACCOUNT_ID, null)
            }
            return if (vault.exists) null else securePrefs.getString(KEY_ACCOUNT_ID, null)
        }
        set(value) {
            if (vault.isUnlocked) {
                migrateToVaultIfUnlocked()
                vault.putString(NAMESPACE, KEY_ACCOUNT_ID, value)
                securePrefs.clear(KEY_ACCOUNT_ID)
            } else {
                securePrefs.putString(KEY_ACCOUNT_ID, value)
            }
        }

    var deviceId: String?
        get() {
            if (vault.isUnlocked) {
                migrateToVaultIfUnlocked()
                return vault.getString(NAMESPACE, KEY_DEVICE_ID, null)
            }
            return if (vault.exists) null else securePrefs.getString(KEY_DEVICE_ID, null)
        }
        set(value) {
            if (vault.isUnlocked) {
                migrateToVaultIfUnlocked()
                vault.putString(NAMESPACE, KEY_DEVICE_ID, value)
                securePrefs.clear(KEY_DEVICE_ID)
            } else {
                securePrefs.putString(KEY_DEVICE_ID, value)
            }
        }

    fun saveBootstrap(accountId: String, deviceId: String) {
        if (vault.isUnlocked) {
            migrateToVaultIfUnlocked()
            vault.putString(NAMESPACE, KEY_ACCOUNT_ID, accountId)
            vault.putString(NAMESPACE, KEY_DEVICE_ID, deviceId)
            securePrefs.clear(KEY_ACCOUNT_ID)
            securePrefs.clear(KEY_DEVICE_ID)
            return
        }
        securePrefs.putStrings(
            mapOf(
                KEY_ACCOUNT_ID to accountId,
                KEY_DEVICE_ID to deviceId,
            ),
        )
    }

    fun clearLinkedIdentity() {
        if (vault.isUnlocked) {
            vault.remove(NAMESPACE, KEY_ACCOUNT_ID)
            vault.remove(NAMESPACE, KEY_DEVICE_ID)
            vault.remove(NAMESPACE, KEY_BASE_URL)
        }
        securePrefs.clear(KEY_ACCOUNT_ID)
        securePrefs.clear(KEY_DEVICE_ID)
        securePrefs.clear(KEY_BASE_URL)
    }

    fun migrateToVaultIfUnlocked() {
        if (!vault.isUnlocked) {
            return
        }
        normalizeDefault(securePrefs.getString(KEY_BASE_URL, null), null)
            ?.takeIf { it.isNotBlank() && !vault.contains(NAMESPACE, KEY_BASE_URL) }
            ?.let {
            vault.putString(NAMESPACE, KEY_BASE_URL, it)
        }
        securePrefs.getString(KEY_ACCOUNT_ID, null)
            ?.takeIf { it.isNotBlank() && !vault.contains(NAMESPACE, KEY_ACCOUNT_ID) }
            ?.let {
            vault.putString(NAMESPACE, KEY_ACCOUNT_ID, it)
        }
        securePrefs.getString(KEY_DEVICE_ID, null)
            ?.takeIf { it.isNotBlank() && !vault.contains(NAMESPACE, KEY_DEVICE_ID) }
            ?.let {
            vault.putString(NAMESPACE, KEY_DEVICE_ID, it)
        }
        securePrefs.clear(KEY_BASE_URL)
        securePrefs.clear(KEY_ACCOUNT_ID)
        securePrefs.clear(KEY_DEVICE_ID)
    }

    fun exportVaultToLegacyIfUnlocked() {
        if (!vault.isUnlocked) {
            return
        }
        securePrefs.putString(KEY_BASE_URL, vault.getString(NAMESPACE, KEY_BASE_URL, DEFAULT_BASE_URL))
        securePrefs.putString(KEY_ACCOUNT_ID, vault.getString(NAMESPACE, KEY_ACCOUNT_ID, null))
        securePrefs.putString(KEY_DEVICE_ID, vault.getString(NAMESPACE, KEY_DEVICE_ID, null))
    }

    private fun normalizeDefault(value: String?, defaultValue: String?): String? {
        return if (value in OLD_DEFAULT_BASE_URLS) defaultValue else value
    }

    private companion object {
        const val KEY_BASE_URL = "base_url"
        const val KEY_ACCOUNT_ID = "account_id"
        const val KEY_DEVICE_ID = "device_id"
        const val NAMESPACE = "session"
        val OLD_DEFAULT_BASE_URLS = setOf(
            "https://176.126.70.76",
            "http://176.126.70.76:8080",
        )
        val DEFAULT_BASE_URL = BuildConfig.DEFAULT_CONTROL_PLANE_URL
        const val KEYSTORE_ALIAS = "virtroid_session_prefs_v1"
    }
}
