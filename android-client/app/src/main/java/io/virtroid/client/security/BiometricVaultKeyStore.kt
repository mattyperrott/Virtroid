package io.virtroid.client.security

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

class BiometricVaultKeyStore(context: Context) {
    private val appContext = context.applicationContext
    private val prefs = appContext.getSharedPreferences("virtroid-biometric-vault", Context.MODE_PRIVATE)

    fun hasWrappedKey(): Boolean {
        return prefs.getString(KEY_IV, null)?.isNotBlank() == true &&
            prefs.getString(KEY_CIPHERTEXT, null)?.isNotBlank() == true
    }

    fun createEncryptCipher(): Cipher? {
        return runCatching {
            Cipher.getInstance(AES_MODE).apply {
                init(Cipher.ENCRYPT_MODE, getOrCreateKey())
            }
        }.getOrElse {
            clear()
            null
        }
    }

    fun createDecryptCipher(): Cipher? {
        val iv = decode(KEY_IV) ?: return null
        return runCatching {
            Cipher.getInstance(AES_MODE).apply {
                init(Cipher.DECRYPT_MODE, getOrCreateKey(), GCMParameterSpec(GCM_TAG_BITS, iv))
            }
        }.getOrElse { error ->
            if (error is KeyPermanentlyInvalidatedException) {
                clear()
            }
            null
        }
    }

    fun saveWrappedKey(cipher: Cipher, vaultKey: ByteArray): Boolean {
        return runCatching {
            val ciphertext = cipher.doFinal(vaultKey)
            val iv = cipher.iv ?: return false
            prefs.edit()
                .putString(KEY_IV, Base64.encodeToString(iv, B64_FLAGS))
                .putString(KEY_CIPHERTEXT, Base64.encodeToString(ciphertext, B64_FLAGS))
                .apply()
            true
        }.getOrDefault(false)
    }

    fun unwrapVaultKey(cipher: Cipher): ByteArray? {
        val ciphertext = decode(KEY_CIPHERTEXT) ?: return null
        return runCatching { cipher.doFinal(ciphertext) }.getOrNull()
    }

    fun clear() {
        prefs.edit().clear().apply()
        runCatching {
            keyStore().deleteEntry(KEY_ALIAS)
        }
    }

    private fun decode(key: String): ByteArray? {
        val value = prefs.getString(key, null)?.takeIf { it.isNotBlank() } ?: return null
        return runCatching { Base64.decode(value, B64_FLAGS) }.getOrNull()
    }

    private fun getOrCreateKey(): SecretKey {
        keyStore().getKey(KEY_ALIAS, null)?.let { return it as SecretKey }

        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        val builder = KeyGenParameterSpec.Builder(
            KEY_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setUserAuthenticationRequired(true)
            .setInvalidatedByBiometricEnrollment(true)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            builder.setUserAuthenticationParameters(0, KeyProperties.AUTH_BIOMETRIC_STRONG)
        } else {
            @Suppress("DEPRECATION")
            builder.setUserAuthenticationValidityDurationSeconds(-1)
        }

        generator.init(builder.build())
        return generator.generateKey()
    }

    private fun keyStore(): KeyStore {
        return KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "virtroid_local_vault_biometric_v1"
        const val AES_MODE = "AES/GCM/NoPadding"
        const val GCM_TAG_BITS = 128
        const val KEY_IV = "wrapped_key_iv"
        const val KEY_CIPHERTEXT = "wrapped_key_ciphertext"
        const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
    }
}
