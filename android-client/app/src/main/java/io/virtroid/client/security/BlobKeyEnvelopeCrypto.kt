package io.virtroid.client.security

import android.util.Base64
import org.json.JSONObject
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.SecureRandom
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.X509EncodedKeySpec
import javax.crypto.Cipher
import javax.crypto.KeyAgreement
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

data class BlobKeyLease(
    val leaseId: String,
    val runtimeId: String,
    val hostId: String,
    val operation: String,
    val algorithm: String,
    val nodePublicKey: String,
)

object BlobKeyEnvelopeCrypto {
    const val ALGORITHM = "P256_ECDH_HKDF_SHA256_AESGCM_V1"
    private const val AAD_CONTEXT = "VIRTROID-BLOB-KEY-ENVELOPE-V1"
    private const val HKDF_INFO = "virtroid-blob-key-envelope-v1"
    private const val GCM_TAG_BITS = 128
    private const val B64_STD_FLAGS = Base64.NO_WRAP
    private const val B64_URL_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE

    fun encryptBlobAccessKey(blobAccessKey: String, lease: BlobKeyLease): JSONObject {
        require(lease.algorithm == ALGORITHM) { "unsupported blob-key lease algorithm" }

        val plaintextKey = Base64.decode(blobAccessKey.trim(), B64_URL_FLAGS)
        require(plaintextKey.size == 32) { "invalid blob access key length" }

        val nodePublicKey = parseNodePublicKey(lease.nodePublicKey)
        val keyPairGenerator = KeyPairGenerator.getInstance("EC")
        keyPairGenerator.initialize(ECGenParameterSpec("secp256r1"), SecureRandom())
        val keyPair = keyPairGenerator.generateKeyPair()

        val agreement = KeyAgreement.getInstance("ECDH")
        agreement.init(keyPair.private)
        agreement.doPhase(nodePublicKey, true)
        val sharedSecret = agreement.generateSecret()

        val aad = envelopeAad(lease)
        val salt = MessageDigest.getInstance("SHA-256").digest(aad)
        val wrappingKey = hkdfSha256(sharedSecret, salt, HKDF_INFO.toByteArray(Charsets.UTF_8), 32)
        val iv = ByteArray(12).also { SecureRandom().nextBytes(it) }

        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(wrappingKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, iv))
        cipher.updateAAD(aad)
        val ciphertext = cipher.doFinal(plaintextKey)

        return JSONObject()
            .put("version", 1)
            .put("algorithm", ALGORITHM)
            .put("lease_id", lease.leaseId)
            .put("operation", lease.operation)
            .put("runtime_id", lease.runtimeId)
            .put("host_id", lease.hostId)
            .put("ephemeral_public_key", Base64.encodeToString(keyPair.public.encoded, B64_STD_FLAGS))
            .put("iv", Base64.encodeToString(iv, B64_STD_FLAGS))
            .put("ciphertext", Base64.encodeToString(ciphertext, B64_STD_FLAGS))
    }

    private fun parseNodePublicKey(publicKeyB64: String): ECPublicKey {
        val publicKeyDer = Base64.decode(publicKeyB64.trim(), B64_STD_FLAGS)
        val parsed = KeyFactory.getInstance("EC").generatePublic(X509EncodedKeySpec(publicKeyDer))
        return parsed as? ECPublicKey ?: throw IllegalArgumentException("node key must be EC")
    }

    private fun envelopeAad(lease: BlobKeyLease): ByteArray {
        return listOf(
            AAD_CONTEXT,
            lease.leaseId.trim(),
            lease.operation.trim(),
            lease.runtimeId.trim(),
            lease.hostId.trim(),
        ).joinToString("\n").toByteArray(Charsets.UTF_8)
    }

    private fun hkdfSha256(secret: ByteArray, salt: ByteArray, info: ByteArray, length: Int): ByteArray {
        val prk = hmacSha256(salt, secret)
        var previous = ByteArray(0)
        val output = ArrayList<Byte>(length)
        var counter = 1
        while (output.size < length) {
            val mac = Mac.getInstance("HmacSHA256")
            mac.init(SecretKeySpec(prk, "HmacSHA256"))
            mac.update(previous)
            mac.update(info)
            mac.update(counter.toByte())
            previous = mac.doFinal()
            previous.forEach { output.add(it) }
            counter += 1
        }
        return output.take(length).toByteArray()
    }

    private fun hmacSha256(key: ByteArray, payload: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(key, "HmacSHA256"))
        return mac.doFinal(payload)
    }
}
