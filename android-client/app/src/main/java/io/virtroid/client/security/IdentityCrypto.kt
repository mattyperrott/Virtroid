package io.virtroid.client.security

import java.security.MessageDigest
import java.util.Base64
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.PBEKeySpec

object IdentityCrypto {
    private const val ITERATIONS = 210_000
    private const val KEY_BYTES = 32
    private const val KDF_LABEL = "virtroid-identity-v1"
    private const val VERIFIER_LABEL = "virtroid-blob-verifier-v1:"
    private val b64Encoder = Base64.getUrlEncoder().withoutPadding()
    private val b64Decoder = Base64.getUrlDecoder()

    fun deriveBlobAccessKey(accountId: String, deviceId: String, password: String): String {
        val salt = "$KDF_LABEL:${accountId.trim()}:${deviceId.trim()}".toByteArray(Charsets.UTF_8)
        val passwordChars = password.toCharArray()
        val spec = PBEKeySpec(passwordChars, salt, ITERATIONS, KEY_BYTES * 8)
        return try {
            val key = SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256").generateSecret(spec).encoded
            try {
                b64Encoder.encodeToString(key)
            } finally {
                key.fill(0)
            }
        } finally {
            spec.clearPassword()
            passwordChars.fill('\u0000')
        }
    }

    fun blobKeyVerifier(blobAccessKey: String): String {
        val rawKey = b64Decoder.decode(blobAccessKey)
        val digest = MessageDigest.getInstance("SHA-256")
            .digest(VERIFIER_LABEL.toByteArray(Charsets.UTF_8) + rawKey)
        return b64Encoder.encodeToString(digest)
    }

    fun isValidBlobAccessKey(blobAccessKey: String): Boolean = runCatching {
        b64Decoder.decode(blobAccessKey.trim()).size == KEY_BYTES
    }.getOrDefault(false)
}
