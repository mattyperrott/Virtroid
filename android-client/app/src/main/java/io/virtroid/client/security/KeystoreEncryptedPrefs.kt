package io.virtroid.client.security

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

internal class KeystoreEncryptedPrefs(
    context: Context,
    prefsName: String,
    private val keyAlias: String,
) {
    private val prefs = context.applicationContext.getSharedPreferences(prefsName, Context.MODE_PRIVATE)

    fun getString(key: String, defaultValue: String?): String? {
        val encrypted = prefs.getString(encryptedKey(key), null)
        if (!encrypted.isNullOrBlank()) {
            return decrypt(encrypted) ?: defaultValue
        }

        val legacy = prefs.getString(key, null) ?: return defaultValue
        putString(key, legacy)
        return legacy
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

    fun putStrings(values: Map<String, String?>) {
        val encryptedValues = values.mapValues { (_, value) -> value?.let(::encrypt) }
        val editor = prefs.edit()
        encryptedValues.forEach { (key, value) ->
            editor.remove(key)
            if (value == null) {
                editor.remove(encryptedKey(key))
            } else {
                editor.putString(encryptedKey(key), value)
            }
        }
        editor.apply()
    }

    fun clear(vararg keys: String) {
        val editor = prefs.edit()
        keys.forEach { key ->
            editor.remove(key)
            editor.remove(encryptedKey(key))
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
                return@runCatching null
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
        (keyStore.getKey(keyAlias, null) as? SecretKey)?.let { return it }

        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        val spec = KeyGenParameterSpec.Builder(
            keyAlias,
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

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val AES_MODE = "AES/GCM/NoPadding"
        const val GCM_IV_BYTES = 12
        const val GCM_TAG_BITS = 128
        const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
    }
}
