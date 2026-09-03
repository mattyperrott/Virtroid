package io.virtroid.client.security

import android.content.Context
import org.json.JSONObject
import java.security.AlgorithmParameters
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.PublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.PKCS8EncodedKeySpec
import java.security.spec.X509EncodedKeySpec
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.KeyAgreement
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

class PushEncryptionKeyStore(context: Context) {
    private val prefs = KeystoreEncryptedPrefs(context, PREFS_NAME, STORAGE_KEY_ALIAS)

    fun publicKeyMaterial(): String = keyPair().second.encoded.let(B64_ENCODER::encodeToString)

    fun decryptEnvelope(envelopeJson: String): String {
        val envelope = JSONObject(envelopeJson)
        require(envelope.getInt("v") == 1)
        val ephemeral = decodePublicKey(envelope.getString("ephemeral_public_key"))
        val nonce = B64_DECODER.decode(envelope.getString("nonce"))
        val ciphertext = B64_DECODER.decode(envelope.getString("ciphertext"))
        require(nonce.size == GCM_NONCE_BYTES && ciphertext.size > GCM_TAG_BYTES)

        val agreement = KeyAgreement.getInstance("ECDH")
        agreement.init(keyPair().first)
        agreement.doPhase(ephemeral, true)
        val sharedSecret = agreement.generateSecret()
        val key = hkdfSha256(sharedSecret, ENVELOPE_CONTEXT.toByteArray(Charsets.UTF_8), 32)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, nonce))
        cipher.updateAAD(ENVELOPE_CONTEXT.toByteArray(Charsets.UTF_8))
        return String(cipher.doFinal(ciphertext), Charsets.UTF_8)
    }

    fun clear() {
        prefs.clear(KEY_PRIVATE, KEY_PUBLIC)
    }

    private fun keyPair(): Pair<PrivateKey, PublicKey> {
        val privateMaterial = prefs.getString(KEY_PRIVATE, null)
        val publicMaterial = prefs.getString(KEY_PUBLIC, null)
        if (!privateMaterial.isNullOrBlank() && !publicMaterial.isNullOrBlank()) {
            return runCatching {
                val factory = KeyFactory.getInstance("EC")
                val privateKey = factory.generatePrivate(PKCS8EncodedKeySpec(B64_DECODER.decode(privateMaterial)))
                val publicKey = factory.generatePublic(X509EncodedKeySpec(B64_DECODER.decode(publicMaterial)))
                privateKey to publicKey
            }.getOrElse {
                prefs.clear(KEY_PRIVATE, KEY_PUBLIC)
                generateKeyPair()
            }
        }
        return generateKeyPair()
    }

    private fun generateKeyPair(): Pair<PrivateKey, PublicKey> {
        val generator = KeyPairGenerator.getInstance("EC")
        generator.initialize(ECGenParameterSpec("secp256r1"))
        val pair = generator.generateKeyPair()
        prefs.putStrings(
            mapOf(
                KEY_PRIVATE to B64_ENCODER.encodeToString(pair.private.encoded),
                KEY_PUBLIC to B64_ENCODER.encodeToString(pair.public.encoded),
            ),
        )
        return pair.private to pair.public
    }

    private fun decodePublicKey(material: String): PublicKey {
        // Force the provider to load P-256 parameters before decoding the key on
        // older Android releases whose EC provider initializes lazily.
        AlgorithmParameters.getInstance("EC").apply { init(ECGenParameterSpec("secp256r1")) }
        return KeyFactory.getInstance("EC").generatePublic(X509EncodedKeySpec(B64_DECODER.decode(material)))
    }

    private fun hkdfSha256(secret: ByteArray, info: ByteArray, length: Int): ByteArray {
        require(length in 1..32)
        val extract = Mac.getInstance("HmacSHA256")
        extract.init(SecretKeySpec(ByteArray(32), "HmacSHA256"))
        val prk = extract.doFinal(secret)
        val expand = Mac.getInstance("HmacSHA256")
        expand.init(SecretKeySpec(prk, "HmacSHA256"))
        expand.update(info)
        expand.update(1)
        return expand.doFinal().copyOf(length)
    }

    private companion object {
        const val PREFS_NAME = "virtroid-push-encryption"
        const val STORAGE_KEY_ALIAS = "virtroid_push_key_storage_v1"
        const val KEY_PRIVATE = "private_key"
        const val KEY_PUBLIC = "public_key"
        const val ENVELOPE_CONTEXT = "VIRTROID-PUSH-ENVELOPE-V1"
        const val GCM_NONCE_BYTES = 12
        const val GCM_TAG_BYTES = 16
        val B64_ENCODER: Base64.Encoder = Base64.getUrlEncoder().withoutPadding()
        val B64_DECODER: Base64.Decoder = Base64.getUrlDecoder()
    }
}
