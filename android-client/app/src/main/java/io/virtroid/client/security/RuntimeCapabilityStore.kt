package io.virtroid.client.security

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.util.UUID

class RuntimeCapabilityStore {
    fun rotate(runtimeId: String): String {
        clear(runtimeId)
        return publicKeyMaterial(runtimeId)
    }

    fun clear(runtimeId: String) {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        val alias = keyAlias(runtimeId)
        if (keyStore.containsAlias(alias)) {
            keyStore.deleteEntry(alias)
        }
    }

    fun publicKeyMaterial(runtimeId: String): String {
        val alias = keyAlias(runtimeId)
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        val existingCertificate = keyStore.getCertificate(alias)
        if (existingCertificate != null) {
            return Base64.encodeToString(existingCertificate.publicKey.encoded, Base64.NO_WRAP)
        }

        val keyPairGenerator = KeyPairGenerator.getInstance(
            KeyProperties.KEY_ALGORITHM_EC,
            ANDROID_KEYSTORE,
        )
        val spec = KeyGenParameterSpec.Builder(
            alias,
            KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY,
        )
            .setAlgorithmParameterSpec(java.security.spec.ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA512)
            .build()

        keyPairGenerator.initialize(spec)
        val keyPair = keyPairGenerator.generateKeyPair()
        return Base64.encodeToString(keyPair.public.encoded, Base64.NO_WRAP)
    }

    fun capabilityId(runtimeId: String, publicKeyMaterial: String = publicKeyMaterial(runtimeId)): String {
        val material = listOf(
            CAPABILITY_ID_CONTEXT,
            runtimeId.trim(),
            publicKeyMaterial.trim(),
        ).joinToString("\n")
        val digest = MessageDigest.getInstance("SHA-256")
            .digest(material.toByteArray(Charsets.UTF_8))
            .copyOf(16)
        return Base64.encodeToString(digest, B64_URL_FLAGS)
    }

    fun signedHeaders(
        method: String,
        requestUri: String,
        runtimeId: String,
        body: ByteArray,
    ): Map<String, String> {
        val publicKey = publicKeyMaterial(runtimeId)
        val capabilityId = capabilityId(runtimeId, publicKey)
        val timestamp = (System.currentTimeMillis() / 1000L).toString()
        val nonce = UUID.randomUUID().toString()
        val bodyHash = MessageDigest.getInstance("SHA-256")
            .digest(body)
            .let { Base64.encodeToString(it, B64_URL_FLAGS) }
        val canonical = listOf(
            SIGNATURE_CONTEXT,
            method.uppercase(),
            requestUri,
            capabilityId,
            timestamp,
            nonce,
            bodyHash,
        ).joinToString("\n")

        val signature = Signature.getInstance("SHA256withECDSA")
        signature.initSign(privateKey(runtimeId))
        signature.update(canonical.toByteArray(Charsets.UTF_8))
        val signed = Base64.encodeToString(signature.sign(), B64_URL_FLAGS)

        return mapOf(
            "X-Virtroid-Capability-ID" to capabilityId,
            "X-Virtroid-Capability-Timestamp" to timestamp,
            "X-Virtroid-Capability-Nonce" to nonce,
            "X-Virtroid-Capability-Body-SHA256" to bodyHash,
            "X-Virtroid-Capability-Signature" to signed,
        )
    }

    private fun privateKey(runtimeId: String): PrivateKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        return keyStore.getKey(keyAlias(runtimeId), null) as PrivateKey
    }

    private fun keyAlias(runtimeId: String): String {
        val cleanRuntimeId = runtimeId.trim().replace(Regex("""[^A-Za-z0-9_-]"""), "_")
        return "$KEY_ALIAS_PREFIX$cleanRuntimeId"
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS_PREFIX = "virtroid_runtime_capability_"
        const val CAPABILITY_ID_CONTEXT = "VIRTROID-RUNTIME-CAPABILITY-ID-V1"
        const val SIGNATURE_CONTEXT = "VIRTROID-CAPABILITY-SIGNATURE-V1"
        const val B64_URL_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
    }
}
