package io.virtdroid.client.security

import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.util.UUID

class DeviceIdentityStore {
    fun publicKeyMaterial(): String {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        val existingCertificate = keyStore.getCertificate(KEY_ALIAS)
        if (existingCertificate != null) {
            return Base64.encodeToString(existingCertificate.publicKey.encoded, Base64.NO_WRAP)
        }

        val keyPairGenerator = KeyPairGenerator.getInstance(
            KeyProperties.KEY_ALGORITHM_EC,
            ANDROID_KEYSTORE,
        )
        val spec = KeyGenParameterSpec.Builder(
            KEY_ALIAS,
            KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY,
        )
            .setAlgorithmParameterSpec(java.security.spec.ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA512)
            .build()

        keyPairGenerator.initialize(spec)
        val keyPair = keyPairGenerator.generateKeyPair()
        return Base64.encodeToString(keyPair.public.encoded, Base64.NO_WRAP)
    }

    fun defaultDeviceName(): String {
        return listOfNotNull(Build.MANUFACTURER, Build.MODEL)
            .joinToString(" ")
            .ifBlank { "Virtdroid Android" }
    }

    fun signedHeaders(
        method: String,
        requestUri: String,
        accountId: String,
        deviceId: String,
        body: ByteArray,
    ): Map<String, String> {
        publicKeyMaterial()
        val timestamp = (System.currentTimeMillis() / 1000L).toString()
        val nonce = UUID.randomUUID().toString()
        val bodyHash = MessageDigest.getInstance("SHA-256")
            .digest(body)
            .let { Base64.encodeToString(it, B64_URL_FLAGS) }
        val canonical = listOf(
            SIGNATURE_CONTEXT,
            method.uppercase(),
            requestUri,
            accountId.trim(),
            deviceId.trim(),
            timestamp,
            nonce,
            bodyHash,
        ).joinToString("\n")

        val signature = Signature.getInstance("SHA256withECDSA")
        signature.initSign(privateKey())
        signature.update(canonical.toByteArray(Charsets.UTF_8))
        val signed = Base64.encodeToString(signature.sign(), B64_URL_FLAGS)

        return mapOf(
            "X-Virtdroid-Account-ID" to accountId.trim(),
            "X-Virtdroid-Device-ID" to deviceId.trim(),
            "X-Virtdroid-Timestamp" to timestamp,
            "X-Virtdroid-Nonce" to nonce,
            "X-Virtdroid-Body-SHA256" to bodyHash,
            "X-Virtdroid-Signature" to signed,
        )
    }

    private fun privateKey(): PrivateKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        return keyStore.getKey(KEY_ALIAS, null) as PrivateKey
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "virtdroid_device_key"
        const val SIGNATURE_CONTEXT = "VIRTDROID-DEVICE-SIGNATURE-V1"
        const val B64_URL_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
    }
}
