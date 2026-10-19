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
    /** Argon2id memory (version 3). */
    val kdfMemoryKiB: Int = 0,
    /** Hash of the version 3 login key; sent when registering or rewrapping. */
    val authVerifier: String = "",
)

/** How a recovering phone derives its keys before it may fetch the envelope. */
data class AccountRecoveryParameters(
    val version: Int,
    val kdfAlgorithm: String,
    val kdfSalt: String,
    val kdfIterations: Int,
    val kdfMemoryKiB: Int,
)

/** Keys derived from the identity password for a version 3 credential. */
class AccountRecoveryKeys internal constructor(internal val wrappingKey: ByteArray, val loginKey: String)

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

/** The account's keys wrapped under a new password, with the server's proof. */
data class RewrappedAccountRecovery(
    val credential: AccountRecoveryCredential,
    val proof: String,
    val blobAccessKey: String,
)

/**
 * The account key and recovery key, wrapped under the identity password.
 * Version 3 derives keys with Argon2id: one wraps the envelope, the other is a
 * login key whose hash the server keeps, so it hands the envelope only to
 * whoever knows the password and nobody can take it away to guess offline.
 * Version 2 (PBKDF2) envelopes still open, and are rewrapped as version 3.
 */
object AccountMasterKeyCrypto {
    const val VERSION = 3
    const val KDF_ALGORITHM = "ARGON2ID_V3"
    const val ARGON_PASSES = 3
    const val ARGON_MEMORY_KIB = 64 * 1024
    const val LEGACY_VERSION = 2
    const val LEGACY_KDF_ALGORITHM = "PBKDF2_HMAC_SHA256_V2"
    const val LEGACY_KDF_ITERATIONS = 600_000
    const val ENVELOPE_ALGORITHM = "AES_GCM_256_V1"

    private const val KDF_KEY_BITS = 256
    private const val GCM_TAG_BITS = 128
    private const val PAYLOAD_VERSION = 2
    private const val LEGACY_ENVELOPE_CONTEXT = "VIRTROID-ACCOUNT-MASTER-KEY-ENVELOPE-V2"
    private const val ENVELOPE_CONTEXT = "VIRTROID-ACCOUNT-MASTER-KEY-ENVELOPE-V3"
    private const val LOGIN_CONTEXT = "VIRTROID-ACCOUNT-RECOVERY-LOGIN-V3\n"
    private const val PAYLOAD_CONTEXT = "VIRTROID-ACCOUNT-RECOVERY-PAYLOAD-V2"
    private const val RECOVERY_SIGNATURE_CONTEXT = "VIRTROID-ACCOUNT-RECOVERY-V1"
    private const val LEGACY_REWRAP_SIGNATURE_CONTEXT = "VIRTROID-ACCOUNT-RECOVERY-REWRAP-V1"
    private const val REWRAP_SIGNATURE_CONTEXT = "VIRTROID-ACCOUNT-RECOVERY-REWRAP-V2"
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
        val recoveryPrivateKeyBytes = recoveryKeys.private.encoded
        val credential = try {
            seal(
                AccountRecoveryCredential(
                    accountId = accountId.trim(),
                    version = VERSION,
                    kdfAlgorithm = KDF_ALGORITHM,
                    kdfSalt = "",
                    kdfIterations = 0,
                    envelopeAlgorithm = ENVELOPE_ALGORITHM,
                    envelopeIv = "",
                    envelopeCiphertext = "",
                    recoveryPublicKey = b64Encoder.encodeToString(recoveryKeys.public.encoded),
                    blobKeyVerifier = IdentityCrypto.blobKeyVerifier(blobAccessKey),
                ),
                password,
                masterKeyBytes,
                recoveryPrivateKeyBytes,
            )
        } finally {
            recoveryPrivateKeyBytes.fill(0)
            masterKeyBytes.fill(0)
        }
        return AccountRecoveryBundle(credential = credential, blobAccessKey = blobAccessKey)
    }

    /**
     * Wraps the same account key and recovery key under [newPassword]. The
     * account key does not change, so encrypted snapshots stay readable. The
     * proof, a recovery-key signature over the new envelope, shows the server
     * that whoever replaces it opened the old one.
     */
    fun rewrap(
        credential: AccountRecoveryCredential,
        deviceId: String,
        currentPassword: String,
        newPassword: String,
    ): RewrappedAccountRecovery {
        require(newPassword.isNotBlank()) { "blob encryption password is required" }
        val unlocked = unlock(credential, currentPassword)
        val masterKeyBytes = decodeBlobAccessKey(unlocked.blobAccessKey)
        val recoveryPrivateKeyBytes = b64Decoder.decode(unlocked.recoveryPrivateKey)
        // Always rewrapped as the current version: a version 2 envelope is
        // upgraded the first time it is opened with its password.
        val rewrapped = try {
            seal(
                credential.copy(accountId = credential.accountId.trim(), version = VERSION, kdfAlgorithm = KDF_ALGORITHM),
                newPassword,
                masterKeyBytes,
                recoveryPrivateKeyBytes,
            )
        } finally {
            masterKeyBytes.fill(0)
            recoveryPrivateKeyBytes.fill(0)
        }
        val signature = Signature.getInstance("SHA256withECDSA")
        signature.initSign(parseRecoveryPrivateKey(unlocked.recoveryPrivateKey))
        signature.update(rewrapCanonical(rewrapped, deviceId).toByteArray(Charsets.UTF_8))
        return RewrappedAccountRecovery(
            credential = rewrapped,
            proof = b64UrlEncoder.encodeToString(signature.sign()),
            blobAccessKey = unlocked.blobAccessKey,
        )
    }

    internal fun rewrapCanonical(credential: AccountRecoveryCredential, deviceId: String): String =
        if (credential.version >= VERSION) {
            listOf(
                REWRAP_SIGNATURE_CONTEXT,
                credential.accountId.trim(),
                deviceId.trim(),
                credential.version.toString(),
                credential.kdfAlgorithm.trim(),
                credential.kdfSalt.trim(),
                credential.kdfIterations.toString(),
                credential.kdfMemoryKiB.toString(),
                credential.authVerifier.trim(),
                credential.envelopeIv.trim(),
                credential.envelopeCiphertext.trim(),
            ).joinToString("\n")
        } else {
            listOf(
                LEGACY_REWRAP_SIGNATURE_CONTEXT,
                credential.accountId.trim(),
                deviceId.trim(),
                credential.kdfSalt.trim(),
                credential.kdfIterations.toString(),
                credential.envelopeIv.trim(),
                credential.envelopeCiphertext.trim(),
            ).joinToString("\n")
        }

    /**
     * Seals the keys into [template]'s account and version with a fresh salt
     * and IV, filling in the KDF fields. Version 2 is sealed only by tests.
     */
    internal fun seal(
        template: AccountRecoveryCredential,
        password: String,
        masterKey: ByteArray,
        recoveryPrivateKey: ByteArray,
    ): AccountRecoveryCredential {
        val salt = ByteArray(32).also { SecureRandom().nextBytes(it) }
        val iv = ByteArray(12).also { SecureRandom().nextBytes(it) }
        val credential = if (template.version >= VERSION) {
            template.copy(
                version = VERSION,
                kdfAlgorithm = KDF_ALGORITHM,
                kdfSalt = b64UrlEncoder.encodeToString(salt),
                kdfIterations = ARGON_PASSES,
                kdfMemoryKiB = ARGON_MEMORY_KIB,
            )
        } else {
            template.copy(
                version = LEGACY_VERSION,
                kdfAlgorithm = LEGACY_KDF_ALGORITHM,
                kdfSalt = b64UrlEncoder.encodeToString(salt),
                kdfIterations = LEGACY_KDF_ITERATIONS,
                kdfMemoryKiB = 0,
                authVerifier = "",
            )
        }
        val keys = deriveKeys(credential.parameters(), password)
        val plaintext = encodePayload(masterKey, recoveryPrivateKey)
        val ciphertext = try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(keys.wrappingKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, iv))
            cipher.updateAAD(envelopeAad(credential))
            cipher.doFinal(plaintext)
        } finally {
            keys.wrappingKey.fill(0)
            plaintext.fill(0)
        }
        return credential.copy(
            envelopeAlgorithm = ENVELOPE_ALGORITHM,
            envelopeIv = b64UrlEncoder.encodeToString(iv),
            envelopeCiphertext = b64UrlEncoder.encodeToString(ciphertext),
            authVerifier = if (credential.version >= VERSION) loginKeyVerifier(keys.loginKey) else "",
        )
    }

    private fun AccountRecoveryCredential.parameters() =
        AccountRecoveryParameters(version, kdfAlgorithm, kdfSalt, kdfIterations, kdfMemoryKiB)

    /**
     * Derives a credential's keys from [password]: for version 3, the login
     * key a recovering phone shows the server, and the key that opens the
     * envelope; for version 2, the latter alone.
     */
    fun deriveKeys(parameters: AccountRecoveryParameters, password: String): AccountRecoveryKeys {
        val salt = b64UrlDecoder.decode(parameters.kdfSalt)
        require(salt.size == 32) { "invalid account recovery salt" }
        return when {
            parameters.version == VERSION && parameters.kdfAlgorithm == KDF_ALGORITHM -> {
                require(parameters.kdfIterations in ARGON_PASSES..10 && parameters.kdfMemoryKiB in ARGON_MEMORY_KIB..256 * 1024) {
                    "invalid account recovery KDF work factor"
                }
                val derived = argon2id(password, salt, parameters.kdfIterations, parameters.kdfMemoryKiB)
                try {
                    AccountRecoveryKeys(derived.copyOfRange(0, 32), b64UrlEncoder.encodeToString(derived.copyOfRange(32, 64)))
                } finally {
                    derived.fill(0)
                }
            }
            parameters.version == LEGACY_VERSION && parameters.kdfAlgorithm == LEGACY_KDF_ALGORITHM -> {
                require(parameters.kdfIterations in LEGACY_KDF_ITERATIONS..2_000_000) { "invalid account recovery KDF work factor" }
                AccountRecoveryKeys(deriveLegacyWrappingKey(password, salt, parameters.kdfIterations), "")
            }
            else -> throw IllegalArgumentException("unsupported account recovery KDF")
        }
    }

    /** What the server keeps of a login key; the server computes the same. */
    internal fun loginKeyVerifier(loginKey: String): String {
        val digest = MessageDigest.getInstance("SHA-256")
        digest.update(LOGIN_CONTEXT.toByteArray(Charsets.UTF_8))
        digest.update(b64UrlDecoder.decode(loginKey))
        return b64UrlEncoder.encodeToString(digest.digest())
    }

    internal fun argon2id(password: String, salt: ByteArray, passes: Int, memoryKiB: Int): ByteArray {
        val parameters = org.bouncycastle.crypto.params.Argon2Parameters.Builder(
            org.bouncycastle.crypto.params.Argon2Parameters.ARGON2_id,
        )
            .withVersion(org.bouncycastle.crypto.params.Argon2Parameters.ARGON2_VERSION_13)
            .withIterations(passes)
            .withMemoryAsKB(memoryKiB)
            .withParallelism(1)
            .withSalt(salt)
            .build()
        val generator = org.bouncycastle.crypto.generators.Argon2BytesGenerator()
        generator.init(parameters)
        val passwordBytes = password.toByteArray(Charsets.UTF_8)
        return try {
            ByteArray(64).also { generator.generateBytes(passwordBytes, it) }
        } finally {
            passwordBytes.fill(0)
        }
    }

    /**
     * Opens [credential] with [password], or with [keys] already derived from
     * it (a recovering phone derives them once, for its login key too).
     */
    fun unlock(
        credential: AccountRecoveryCredential,
        password: String,
        keys: AccountRecoveryKeys? = null,
    ): UnlockedAccountRecovery {
        require(credential.envelopeAlgorithm == ENVELOPE_ALGORITHM) { "unsupported account recovery envelope" }
        val iv = b64UrlDecoder.decode(credential.envelopeIv)
        val ciphertext = b64UrlDecoder.decode(credential.envelopeCiphertext)
        require(iv.size == 12 && ciphertext.size in 64..4096) {
            "invalid account recovery envelope"
        }
        val wrappingKey = (keys ?: deriveKeys(credential.parameters(), password)).wrappingKey.copyOf()
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(wrappingKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, iv))
        cipher.updateAAD(envelopeAad(credential))
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

    private fun envelopeAad(credential: AccountRecoveryCredential): ByteArray = when (credential.version) {
        VERSION -> listOf(
            ENVELOPE_CONTEXT,
            credential.accountId.trim(),
            VERSION.toString(),
            KDF_ALGORITHM,
            credential.kdfSalt,
            credential.kdfIterations.toString(),
            credential.kdfMemoryKiB.toString(),
            ENVELOPE_ALGORITHM,
        )
        LEGACY_VERSION -> listOf(
            LEGACY_ENVELOPE_CONTEXT,
            credential.accountId.trim(),
            LEGACY_VERSION.toString(),
            LEGACY_KDF_ALGORITHM,
            credential.kdfSalt,
            credential.kdfIterations.toString(),
            ENVELOPE_ALGORITHM,
        )
        else -> throw IllegalArgumentException("unsupported account recovery version")
    }.joinToString("\n").toByteArray(Charsets.UTF_8)

    private fun deriveLegacyWrappingKey(password: String, salt: ByteArray, iterations: Int): ByteArray {
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
                data.writeInt(PAYLOAD_VERSION)
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
            require(input.readInt() == PAYLOAD_VERSION) { "invalid account recovery payload" }
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
