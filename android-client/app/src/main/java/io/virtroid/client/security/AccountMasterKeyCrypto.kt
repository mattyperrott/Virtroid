package io.virtroid.client.security

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.SecureRandom
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.security.spec.PKCS8EncodedKeySpec
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

data class AccountRecoveryCredential(
    val accountId: String,
    val version: Int,
    val kdfAlgorithm: String,
    val kdfSalt: String,
    val kdfIterations: Int,
    val envelopeAlgorithm: String,
    val envelopeIv: String,
    val envelopeCiphertext: String,
    val recoveryPublicKey: String,
    val blobKeyVerifier: String,
)

data class AccountRecoveryChallenge(
    val id: String,
    val accountId: String,
    val deviceId: String,
    val deviceName: String,
    val devicePublicKey: String,
    val nonce: String,
    val expiresAt: String,
)

data class AccountRecoveryBundle(
    val credential: AccountRecoveryCredential,
    val blobAccessKey: String,
)

data class UnlockedAccountRecovery(
    val blobAccessKey: String,
    val recoveryPrivateKey: String,
)

object AccountMasterKeyCrypto {
    const val VERSION = 2
    const val KDF_ALGORITHM = "PBKDF2_HMAC_SHA256_V2"
    const val ENVELOPE_ALGORITHM = "AES_GCM_256_V1"
    const val KDF_ITERATIONS = 600_000

    private const val KDF_KEY_BITS = 256
    private const val GCM_TAG_BITS = 128
    private const val ENVELOPE_CONTEXT = "VIRTROID-ACCOUNT-MASTER-KEY-ENVELOPE-V2"
    private const val PAYLOAD_CONTEXT = "VIRTROID-ACCOUNT-RECOVERY-PAYLOAD-V2"
    private const val RECOVERY_SIGNATURE_CONTEXT = "VIRTROID-ACCOUNT-RECOVERY-V1"
    private val b64Encoder = Base64.getEncoder()
    private val b64Decoder = Base64.getDecoder()
    private val b64UrlEncoder = Base64.getUrlEncoder().withoutPadding()
    private val b64UrlDecoder = Base64.getUrlDecoder()

    fun create(accountId: String, password: String, existingBlobAccessKey: String? = null): AccountRecoveryBundle {
        require(accountId.isNotBlank()) { "account id is required" }
        require(password.isNotBlank()) { "blob encryption password is required" }

        val masterKeyBytes = existingBlobAccessKey?.let(::decodeBlobAccessKey)
            ?: ByteArray(32).also { SecureRandom().nextBytes(it) }
        val blobAccessKey = b64UrlEncoder.encodeToString(masterKeyBytes)

        val recoveryKeys = KeyPairGenerator.getInstance("EC").apply {
            initialize(ECGenParameterSpec("secp256r1"), SecureRandom())
        }.generateKeyPair()
        val salt = ByteArray(32).also { SecureRandom().nextBytes(it) }
        val iv = ByteArray(12).also { SecureRandom().nextBytes(it) }
        val saltEncoded = b64UrlEncoder.encodeToString(salt)
        val ivEncoded = b64UrlEncoder.encodeToString(iv)
        val wrappingKey = deriveWrappingKey(password, salt, KDF_ITERATIONS)

        val recoveryPrivateKeyBytes = recoveryKeys.private.encoded
        val plaintext = try {
            encodePayload(masterKeyBytes, recoveryPrivateKeyBytes)
        } finally {
            recoveryPrivateKeyBytes.fill(0)
        }
        val aad = envelopeAad(accountId, saltEncoded, KDF_ITERATIONS)
        val ciphertext = try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(wrappingKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, iv))
            cipher.updateAAD(aad)
            cipher.doFinal(plaintext)
        } finally {
            wrappingKey.fill(0)
            plaintext.fill(0)
            masterKeyBytes.fill(0)
        }

        return AccountRecoveryBundle(
            credential = AccountRecoveryCredential(
                accountId = accountId.trim(),
                version = VERSION,
                kdfAlgorithm = KDF_ALGORITHM,
                kdfSalt = saltEncoded,
                kdfIterations = KDF_ITERATIONS,
                envelopeAlgorithm = ENVELOPE_ALGORITHM,
                envelopeIv = ivEncoded,
                envelopeCiphertext = b64UrlEncoder.encodeToString(ciphertext),
                recoveryPublicKey = b64Encoder.encodeToString(recoveryKeys.public.encoded),
                blobKeyVerifier = IdentityCrypto.blobKeyVerifier(blobAccessKey),
            ),
            blobAccessKey = blobAccessKey,
        )
    }

    fun unlock(credential: AccountRecoveryCredential, password: String): UnlockedAccountRecovery {
        require(credential.version == VERSION) { "unsupported account recovery version" }
        require(credential.kdfAlgorithm == KDF_ALGORITHM) { "unsupported account recovery KDF" }
        require(credential.envelopeAlgorithm == ENVELOPE_ALGORITHM) { "unsupported account recovery envelope" }
        require(credential.kdfIterations in KDF_ITERATIONS..2_000_000) { "invalid account recovery KDF work factor" }

        val salt = b64UrlDecoder.decode(credential.kdfSalt)
        val iv = b64UrlDecoder.decode(credential.envelopeIv)
        val ciphertext = b64UrlDecoder.decode(credential.envelopeCiphertext)
        require(salt.size == 32 && iv.size == 12 && ciphertext.size in 64..4096) {
            "invalid account recovery envelope"
        }
        val wrappingKey = deriveWrappingKey(password, salt, credential.kdfIterations)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(wrappingKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, iv))
        cipher.updateAAD(envelopeAad(credential.accountId, credential.kdfSalt, credential.kdfIterations))
        val plaintext = try {
            cipher.doFinal(ciphertext)
        } catch (_: Exception) {
            throw IllegalArgumentException("blob encryption password is incorrect")
        } finally {
            wrappingKey.fill(0)
        }
        val payload = try {
            decodePayload(plaintext)
        } finally {
            plaintext.fill(0)
        }
        try {
            val blobAccessKey = b64UrlEncoder.encodeToString(payload.masterKey)
            require(
                MessageDigest.isEqual(
                    IdentityCrypto.blobKeyVerifier(blobAccessKey).toByteArray(Charsets.US_ASCII),
                    credential.blobKeyVerifier.toByteArray(Charsets.US_ASCII),
                ),
            ) { "account recovery key verifier mismatch" }
            val recoveryPrivateKey = b64Encoder.encodeToString(payload.recoveryPrivateKey)
            parseRecoveryPrivateKey(recoveryPrivateKey)
            return UnlockedAccountRecovery(blobAccessKey, recoveryPrivateKey)
        } finally {
            payload.masterKey.fill(0)
            payload.recoveryPrivateKey.fill(0)
        }
    }

    fun signRecoveryChallenge(challenge: AccountRecoveryChallenge, recoveryPrivateKey: String): String {
        val privateKey = parseRecoveryPrivateKey(recoveryPrivateKey)
        val signature = Signature.getInstance("SHA256withECDSA")
        signature.initSign(privateKey)
        signature.update(recoveryCanonical(challenge).toByteArray(Charsets.UTF_8))
        return b64UrlEncoder.encodeToString(signature.sign())
    }

    private fun recoveryCanonical(challenge: AccountRecoveryChallenge): String = listOf(
        RECOVERY_SIGNATURE_CONTEXT,
        challenge.id.trim(),
        challenge.nonce.trim(),
        challenge.accountId.trim(),
        challenge.deviceId.trim(),
        challenge.deviceName.trim(),
        challenge.devicePublicKey.trim(),
    ).joinToString("\n")

    private fun envelopeAad(accountId: String, salt: String, iterations: Int): ByteArray = listOf(
        ENVELOPE_CONTEXT,
        accountId.trim(),
        VERSION.toString(),
        KDF_ALGORITHM,
        salt,
        iterations.toString(),
        ENVELOPE_ALGORITHM,
    ).joinToString("\n").toByteArray(Charsets.UTF_8)

    private fun deriveWrappingKey(password: String, salt: ByteArray, iterations: Int): ByteArray {
        val passwordChars = password.toCharArray()
        val spec = PBEKeySpec(passwordChars, salt, iterations, KDF_KEY_BITS)
        return try {
            SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256").generateSecret(spec).encoded
        } finally {
            spec.clearPassword()
            passwordChars.fill('\u0000')
        }
    }

    private fun encodePayload(masterKey: ByteArray, recoveryPrivateKey: ByteArray): ByteArray {
        require(masterKey.size == 32) { "invalid account master key" }
        require(recoveryPrivateKey.size in 64..512) { "invalid recovery private key" }
        return ByteArrayOutputStream().use { output ->
            DataOutputStream(output).use { data ->
                data.writeUTF(PAYLOAD_CONTEXT)
                data.writeInt(VERSION)
                data.writeInt(masterKey.size)
                data.write(masterKey)
                data.writeInt(recoveryPrivateKey.size)
                data.write(recoveryPrivateKey)
            }
            output.toByteArray()
        }
    }

    private fun decodePayload(plaintext: ByteArray): RecoveryPayload {
        return DataInputStream(ByteArrayInputStream(plaintext)).use { input ->
            require(input.readUTF() == PAYLOAD_CONTEXT) { "invalid account recovery payload" }
            require(input.readInt() == VERSION) { "invalid account recovery payload" }
            val masterKeyLength = input.readInt()
            require(masterKeyLength == 32) { "invalid account recovery payload" }
            val masterKey = ByteArray(masterKeyLength).also(input::readFully)
            val recoveryKeyLength = input.readInt()
            require(recoveryKeyLength in 64..512) { "invalid account recovery payload" }
            val recoveryPrivateKey = ByteArray(recoveryKeyLength).also(input::readFully)
            require(input.read() == -1) { "invalid account recovery payload" }
            RecoveryPayload(masterKey, recoveryPrivateKey)
        }
    }

    private fun decodeBlobAccessKey(value: String): ByteArray {
        val decoded = b64UrlDecoder.decode(value.trim())
        require(decoded.size == 32) { "invalid account master key" }
        return decoded
    }

    private fun parseRecoveryPrivateKey(value: String) = KeyFactory.getInstance("EC")
        .generatePrivate(PKCS8EncodedKeySpec(b64Decoder.decode(value.trim())))

    private data class RecoveryPayload(
        val masterKey: ByteArray,
        val recoveryPrivateKey: ByteArray,
    )
}
