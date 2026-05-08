package io.virtroid.client.data

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import io.virtroid.client.BuildConfig
import io.virtroid.client.security.SecureLocalVault
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

class SessionStore(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext.getSharedPreferences("virtroid-session", Context.MODE_PRIVATE)
    private val securePrefs = SecureSessionPrefs(prefs)
    private val vault = SecureLocalVault.get(appContext)

    fun hasAccess(): Boolean = !accountId.isNullOrBlank() && !deviceId.isNullOrBlank()

    var baseUrl: String
        get() {
            if (vault.isUnlocked) {
                migrateToVaultIfUnlocked()
                return vault.getString(NAMESPACE, KEY_BASE_URL, DEFAULT_BASE_URL).orEmpty()
            }
            return if (vault.exists) DEFAULT_BASE_URL else securePrefs.getString(KEY_BASE_URL, DEFAULT_BASE_URL).orEmpty()
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
        securePrefs.getString(KEY_BASE_URL, null)
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

    private class SecureSessionPrefs(private val prefs: SharedPreferences) {
        fun getString(key: String, defaultValue: String?): String? {
            val encrypted = prefs.getString(encryptedKey(key), null)
            if (!encrypted.isNullOrBlank()) {
                return normalizeDefault(key, decrypt(encrypted), defaultValue)
            }

            val legacy = prefs.getString(key, null) ?: return defaultValue
            putString(key, legacy)
            prefs.edit().remove(key).apply()
            return normalizeDefault(key, legacy, defaultValue)
        }

        fun putString(key: String, value: String?) {
            val editor = prefs.edit().remove(key)
            if (value == null) {
                editor.remove(encryptedKey(key))
            } else {
                editor.putString(encryptedKey(key), encrypt(value))
            }
            editor.apply()
        }

        fun putStrings(values: Map<String, String>) {
            val editor = prefs.edit()
            values.forEach { (key, value) ->
                editor.remove(key)
                editor.putString(encryptedKey(key), encrypt(value))
            }
            editor.apply()
        }

        fun clear(key: String) {
            prefs.edit()
                .remove(key)
                .remove(encryptedKey(key))
                .apply()
        }

        private fun encrypt(value: String): String {
            val cipher = Cipher.getInstance(AES_MODE)
            cipher.init(Cipher.ENCRYPT_MODE, secretKey())
            val ciphertext = cipher.doFinal(value.toByteArray(Charsets.UTF_8))
            val payload = ByteArray(cipher.iv.size + ciphertext.size)
            System.arraycopy(cipher.iv, 0, payload, 0, cipher.iv.size)
            System.arraycopy(ciphertext, 0, payload, cipher.iv.size, ciphertext.size)
            return Base64.encodeToString(payload, B64_FLAGS)
        }

        private fun decrypt(encoded: String): String? {
            return runCatching {
                val payload = Base64.decode(encoded, B64_FLAGS)
                if (payload.size <= GCM_IV_BYTES) {
                    return null
                }
                val iv = payload.copyOfRange(0, GCM_IV_BYTES)
                val ciphertext = payload.copyOfRange(GCM_IV_BYTES, payload.size)
                val cipher = Cipher.getInstance(AES_MODE)
                cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(GCM_TAG_BITS, iv))
                String(cipher.doFinal(ciphertext), Charsets.UTF_8)
            }.getOrNull()
        }

        private fun secretKey(): SecretKey {
            val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
            (keyStore.getKey(KEYSTORE_ALIAS, null) as? SecretKey)?.let { return it }

            val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
            val spec = KeyGenParameterSpec.Builder(
                KEYSTORE_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build()
            generator.init(spec)
            return generator.generateKey()
        }

        private fun encryptedKey(key: String): String = "enc_$key"

        private fun normalizeDefault(key: String, value: String?, defaultValue: String?): String? {
            return if (key == KEY_BASE_URL && value in OLD_DEFAULT_BASE_URLS) {
                defaultValue
            } else {
                value
            }
        }
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
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEYSTORE_ALIAS = "virtroid_session_prefs_v1"
        const val AES_MODE = "AES/GCM/NoPadding"
        const val GCM_IV_BYTES = 12
        const val GCM_TAG_BITS = 128
        const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
    }
}
