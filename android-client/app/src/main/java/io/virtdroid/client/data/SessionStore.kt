package io.virtdroid.client.data

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import io.virtdroid.client.BuildConfig
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

class SessionStore(context: Context) {
    private val prefs = context.getSharedPreferences("virtdroid-session", Context.MODE_PRIVATE)
    private val securePrefs = SecureSessionPrefs(prefs)

    fun hasAccess(): Boolean = !accountId.isNullOrBlank() && !deviceId.isNullOrBlank()

    var baseUrl: String
        get() = securePrefs.getString(KEY_BASE_URL, DEFAULT_BASE_URL).orEmpty()
        set(value) = securePrefs.putString(KEY_BASE_URL, value)

    var accountId: String?
        get() = securePrefs.getString(KEY_ACCOUNT_ID, null)
        set(value) = securePrefs.putString(KEY_ACCOUNT_ID, value)

    var deviceId: String?
        get() = securePrefs.getString(KEY_DEVICE_ID, null)
        set(value) = securePrefs.putString(KEY_DEVICE_ID, value)

    fun saveBootstrap(accountId: String, deviceId: String) {
        securePrefs.putStrings(
            mapOf(
                KEY_ACCOUNT_ID to accountId,
                KEY_DEVICE_ID to deviceId,
            ),
        )
    }

    fun clearLinkedIdentity() {
        securePrefs.putString(KEY_ACCOUNT_ID, null)
        securePrefs.putString(KEY_DEVICE_ID, null)
        securePrefs.putString(KEY_BASE_URL, null)
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
            return if (key == KEY_BASE_URL && value == OLD_DEFAULT_BASE_URL) {
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
        const val OLD_DEFAULT_BASE_URL = "https://176.126.70.76"
        val DEFAULT_BASE_URL = BuildConfig.DEFAULT_CONTROL_PLANE_URL
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEYSTORE_ALIAS = "virtdroid_session_prefs_v1"
        const val AES_MODE = "AES/GCM/NoPadding"
        const val GCM_IV_BYTES = 12
        const val GCM_TAG_BITS = 128
        const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
    }
}
