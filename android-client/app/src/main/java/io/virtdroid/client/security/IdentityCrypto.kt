package io.virtdroid.client.security

import android.util.Base64
import java.security.MessageDigest
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.PBEKeySpec

object IdentityCrypto {
    private const val ITERATIONS = 210_000
    private const val KEY_BYTES = 32
    private const val KDF_LABEL = "virtdroid-identity-v1"
    private const val VERIFIER_LABEL = "virtdroid-blob-verifier-v1:"
    private const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE

    fun deriveBlobAccessKey(accountId: String, deviceId: String, password: String): String {
        val salt = "$KDF_LABEL:${accountId.trim()}:${deviceId.trim()}".toByteArray(Charsets.UTF_8)
        val spec = PBEKeySpec(password.toCharArray(), salt, ITERATIONS, KEY_BYTES * 8)
        val factory = SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256")
        val key = factory.generateSecret(spec).encoded
        return Base64.encodeToString(key, B64_FLAGS)
    }

    fun blobKeyVerifier(blobAccessKey: String): String {
        val rawKey = Base64.decode(blobAccessKey, B64_FLAGS)
        val digest = MessageDigest.getInstance("SHA-256")
            .digest(VERIFIER_LABEL.toByteArray(Charsets.UTF_8) + rawKey)
        return Base64.encodeToString(digest, B64_FLAGS)
    }
}
