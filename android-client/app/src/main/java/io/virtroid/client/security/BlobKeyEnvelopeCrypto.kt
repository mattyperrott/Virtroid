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
    // The phone's challenge, and the VM's fresh report binding its in-memory
    // seal key to it; empty for a node that isn't attested.
    val sealNonce: String = "",
    val sealedAlgorithm: String = "",
    val sealAttestationJson: String = "",
    val confidentialRuntime: Boolean = false,
)

/** An envelope to send, and for sealed ones the runtime keys' verifier. */
data class SealedBlobKey(val envelope: JSONObject, val runtimeKeyVerifier: String?)

object BlobKeyEnvelopeCrypto {
    const val ALGORITHM = "P256_ECDH_HKDF_SHA256_AESGCM_V1"
    const val SEALED_ALGORITHM = "P256_ECDH_HKDF_SHA256_AESGCM_V2_SEALED"
    private const val SEALED_HKDF_INFO = "virtroid-blob-key-envelope-v2"
    private const val SNAPSHOT_KEY_CONTEXT = "VIRTROID-RUNTIME-SNAPSHOT-KEY-V2"
    private const val MANIFEST_MAC_CONTEXT = "VIRTROID-RUNTIME-MANIFEST-MAC-V3"
    private const val NAMESPACE_CONTEXT = "VIRTROID-RUNTIME-BLOB-NAMESPACE-V3"
    private const val NOTIFICATION_KEY_CONTEXT = "VIRTROID-RUNTIME-NOTIFICATION-KEY-V1"
    private const val VIEWER_KEY_CONTEXT = "VIRTROID-RUNTIME-VIEWER-KEY-V1"
    private const val AAD_CONTEXT = "VIRTROID-BLOB-KEY-ENVELOPE-V1"
    private const val HKDF_INFO = "virtroid-blob-key-envelope-v1"
    private const val GCM_TAG_BITS = 128
    private const val B64_STD_FLAGS = Base64.NO_WRAP
    private const val B64_URL_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE

    /**
     * Seals keys for the VM in [lease]. Given [sealPublicKey], the VM's own
     * in-memory key that the caller verified against a fresh report, it seals
     * only this runtime's keys, so the VM never holds the account key.
     * Otherwise it seals the account key to the node key, for nodes that
     * aren't attested.
     */
    fun encryptBlobAccessKey(blobAccessKey: String, lease: BlobKeyLease, sealPublicKey: String?): SealedBlobKey {
        val sealed = sealPublicKey != null
        val algorithm = if (sealed) SEALED_ALGORITHM else ALGORITHM
        if (sealed) {
            require(lease.sealedAlgorithm == SEALED_ALGORITHM) { "unsupported sealed blob-key algorithm" }
        } else {
            require(lease.algorithm == ALGORITHM) { "unsupported blob-key lease algorithm" }
        }

        val accountKey = Base64.decode(blobAccessKey.trim(), B64_URL_FLAGS)
        require(accountKey.size == 32) { "invalid blob access key length" }
        // Sealed: this runtime's keys, then the key its notifications are
        // sealed with for this account's phones.
        val plaintextKey = if (sealed) {
            runtimeKeys(accountKey, lease.runtimeId) + runtimeNotificationKey(accountKey, lease.runtimeId)
        } else {
            accountKey
        }

        val nodePublicKey = parseNodePublicKey(sealPublicKey ?: lease.nodePublicKey)
        val keyPairGenerator = KeyPairGenerator.getInstance("EC")
        keyPairGenerator.initialize(ECGenParameterSpec("secp256r1"), SecureRandom())
        val keyPair = keyPairGenerator.generateKeyPair()

        val agreement = KeyAgreement.getInstance("ECDH")
        agreement.init(keyPair.private)
        agreement.doPhase(nodePublicKey, true)
        val sharedSecret = agreement.generateSecret()

        val aad = envelopeAad(lease)
        val salt = MessageDigest.getInstance("SHA-256").digest(aad)
        val info = if (sealed) SEALED_HKDF_INFO else HKDF_INFO
        val wrappingKey = hkdfSha256(sharedSecret, salt, info.toByteArray(Charsets.UTF_8), 32)
        val iv = ByteArray(12).also { SecureRandom().nextBytes(it) }

        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(wrappingKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, iv))
        cipher.updateAAD(aad)
        val ciphertext = cipher.doFinal(plaintextKey)

        val envelope = JSONObject()
            .put("version", 1)
            .put("algorithm", algorithm)
            .put("lease_id", lease.leaseId)
            .put("operation", lease.operation)
            .put("runtime_id", lease.runtimeId)
            .put("host_id", lease.hostId)
            .put("ephemeral_public_key", Base64.encodeToString(keyPair.public.encoded, B64_STD_FLAGS))
            .put("iv", Base64.encodeToString(iv, B64_STD_FLAGS))
            .put("ciphertext", Base64.encodeToString(ciphertext, B64_STD_FLAGS))
        return SealedBlobKey(envelope, if (sealed) runtimeKeysVerifier(plaintextKey) else null)
    }

    /**
     * One runtime's keys, derived from the account key exactly as the VM
     * derives them: its snapshot key, manifest MAC key and blob namespace.
     */
    fun runtimeKeys(accountKey: ByteArray, runtimeId: String): ByteArray {
        val id = runtimeId.trim().toByteArray(Charsets.UTF_8)
        fun derive(context: String) = hmacSha256(accountKey, context.toByteArray(Charsets.UTF_8) + byteArrayOf(0) + id)
        return derive(SNAPSHOT_KEY_CONTEXT) + derive(MANIFEST_MAC_CONTEXT) + derive(NAMESPACE_CONTEXT)
    }

    /** The key a runtime's agent seals notifications with for the account's phones. */
    fun runtimeNotificationKey(accountKey: ByteArray, runtimeId: String): ByteArray =
        hmacSha256(
            accountKey,
            NOTIFICATION_KEY_CONTEXT.toByteArray(Charsets.UTF_8) + byteArrayOf(0) + runtimeId.trim().toByteArray(Charsets.UTF_8),
        )

    /**
     * The key this phone proves to the runtime's viewer it may watch and
     * control it. It comes from the runtime's snapshot key, which only the
     * account's phones and the attested VM hold, never the control plane.
     */
    fun runtimeViewerKey(accountKey: ByteArray, runtimeId: String): ByteArray {
        val snapshotKey = runtimeKeys(accountKey, runtimeId).copyOfRange(0, 32)
        return hmacSha256(snapshotKey, VIEWER_KEY_CONTEXT.toByteArray(Charsets.UTF_8)).also { snapshotKey.fill(0) }
    }

    /** runtimeViewerKey from the blob access key, as base64 for the session store. */
    fun runtimeViewerKey(blobAccessKey: String, runtimeId: String): String {
        val accountKey = Base64.decode(blobAccessKey.trim(), B64_URL_FLAGS)
        val key = runtimeViewerKey(accountKey, runtimeId)
        accountKey.fill(0)
        return Base64.encodeToString(key, Base64.NO_WRAP).also { key.fill(0) }
    }

    /** What the VM checks the runtime keys against once it opens them. */
    fun runtimeKeysVerifier(runtimeKeys: ByteArray): String =
        Base64.encodeToString(
            MessageDigest.getInstance("SHA-256").digest("virtroid-runtime-keys-verifier-v1:".toByteArray(Charsets.UTF_8) + runtimeKeys),
            B64_URL_FLAGS,
        )

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
