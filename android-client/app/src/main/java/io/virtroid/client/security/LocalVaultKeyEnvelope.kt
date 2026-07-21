package io.virtroid.client.security

import java.util.Arrays
import javax.crypto.Cipher
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

internal object LocalVaultKeyEnvelope {
    data class Wrapped(val iv: ByteArray, val ciphertext: ByteArray)

    fun wrapHardwareSecretBoundDataKey(
        dataKey: ByteArray,
        secretKey: ByteArray,
        hardwareKey: SecretKey,
    ): Wrapped {
        val secretWrapped = wrapSecretOnlyDataKey(dataKey, secretKey)
        val inner = ByteArray(GCM_IV_BYTES + secretWrapped.ciphertext.size)
        return try {
            System.arraycopy(secretWrapped.iv, 0, inner, 0, GCM_IV_BYTES)
            System.arraycopy(secretWrapped.ciphertext, 0, inner, GCM_IV_BYTES, secretWrapped.ciphertext.size)
            encrypt(inner, hardwareKey)
        } finally {
            secretWrapped.clear()
            Arrays.fill(inner, 0)
        }
    }

    fun unwrapHardwareSecretBoundDataKey(
        wrapped: Wrapped,
        secretKey: ByteArray,
        hardwareKey: SecretKey,
    ): ByteArray? {
        val inner = decrypt(wrapped, hardwareKey) ?: return null
        return try {
            if (inner.size <= GCM_IV_BYTES) {
                null
            } else {
                val secretWrapped = Wrapped(
                    iv = inner.copyOfRange(0, GCM_IV_BYTES),
                    ciphertext = inner.copyOfRange(GCM_IV_BYTES, inner.size),
                )
                try {
                    unwrapSecretOnlyDataKey(secretWrapped, secretKey)
                } finally {
                    secretWrapped.clear()
                }
            }
        } finally {
            Arrays.fill(inner, 0)
        }
    }

    fun wrapSecretOnlyDataKey(dataKey: ByteArray, secretKey: ByteArray): Wrapped {
        return encrypt(dataKey, SecretKeySpec(secretKey, KEY_ALGORITHM))
    }

    fun unwrapSecretOnlyDataKey(wrapped: Wrapped, secretKey: ByteArray): ByteArray? {
        val candidate = decrypt(wrapped, SecretKeySpec(secretKey, KEY_ALGORITHM)) ?: return null
        return if (candidate.size == KEY_BYTES) {
            candidate
        } else {
            Arrays.fill(candidate, 0)
            null
        }
    }

    private fun encrypt(plaintext: ByteArray, key: SecretKey): Wrapped {
        val cipher = Cipher.getInstance(AES_MODE)
        cipher.init(Cipher.ENCRYPT_MODE, key)
        val ciphertext = cipher.doFinal(plaintext)
        val iv = cipher.iv ?: throw IllegalStateException("AES-GCM provider did not return an IV")
        require(iv.size == GCM_IV_BYTES) { "AES-GCM provider returned an invalid IV" }
        return Wrapped(iv = iv, ciphertext = ciphertext)
    }

    private fun decrypt(wrapped: Wrapped, key: SecretKey): ByteArray? {
        return runCatching {
            val cipher = Cipher.getInstance(AES_MODE)
            cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(GCM_TAG_BITS, wrapped.iv))
            cipher.doFinal(wrapped.ciphertext)
        }.getOrNull()
    }

    private fun Wrapped.clear() {
        Arrays.fill(iv, 0)
        Arrays.fill(ciphertext, 0)
    }

    private const val AES_MODE = "AES/GCM/NoPadding"
    private const val KEY_ALGORITHM = "AES"
    private const val GCM_IV_BYTES = 12
    private const val GCM_TAG_BITS = 128
    private const val KEY_BYTES = 32
}
