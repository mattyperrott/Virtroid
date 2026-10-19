package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.security.KeyFactory
import java.security.Signature
import java.security.spec.X509EncodedKeySpec
import java.util.Base64

class AccountMasterKeyCryptoTest {
    private val accountId = "11111111-1111-1111-1111-111111111111"
    private val password = "correct horse battery staple"

    @Test
    fun newAccountKeyRoundTripsAndIsRandom() {
        val first = AccountMasterKeyCrypto.create(accountId, password)
        val second = AccountMasterKeyCrypto.create(accountId, password)

        assertNotEquals(first.blobAccessKey, second.blobAccessKey)
        assertEquals(first.blobAccessKey, AccountMasterKeyCrypto.unlock(first.credential, password).blobAccessKey)
        assertEquals(
            IdentityCrypto.blobKeyVerifier(first.blobAccessKey),
            first.credential.blobKeyVerifier,
        )
    }

    @Test
    fun wrongPasswordCannotDecryptRecoveryEnvelope() {
        val bundle = AccountMasterKeyCrypto.create(accountId, password)

        try {
            AccountMasterKeyCrypto.unlock(bundle.credential, "definitely the wrong password")
            fail("wrong password unexpectedly decrypted the account recovery envelope")
        } catch (error: IllegalArgumentException) {
            assertTrue(error.message.orEmpty().contains("password", ignoreCase = true))
        }
    }

    @Test
    fun legacyMigrationPreservesExistingSnapshotKey() {
        val legacyKey = IdentityCrypto.deriveBlobAccessKey(
            accountId,
            "22222222-2222-2222-2222-222222222222",
            password,
        )
        val bundle = AccountMasterKeyCrypto.create(accountId, password, legacyKey)

        assertEquals(legacyKey, bundle.blobAccessKey)
        assertEquals(legacyKey, AccountMasterKeyCrypto.unlock(bundle.credential, password).blobAccessKey)
    }

    @Test
    fun encryptedEnvelopeIsBoundToAccountId() {
        val bundle = AccountMasterKeyCrypto.create(accountId, password)
        val wrongAccountCredential = bundle.credential.copy(
            accountId = "33333333-3333-3333-3333-333333333333",
        )

        try {
            AccountMasterKeyCrypto.unlock(wrongAccountCredential, password)
            fail("recovery envelope unexpectedly decrypted for another account")
        } catch (error: IllegalArgumentException) {
            assertFalse(error.message.isNullOrBlank())
        }
    }

    @Test
    fun recoveredPrivateKeySignsTheBoundEnrollmentChallenge() {
        val bundle = AccountMasterKeyCrypto.create(accountId, password)
        val unlocked = AccountMasterKeyCrypto.unlock(bundle.credential, password)
        val challenge = AccountRecoveryChallenge(
            id = "44444444-4444-4444-4444-444444444444",
            accountId = accountId,
            deviceId = "55555555-5555-5555-5555-555555555555",
            deviceName = "Replacement phone",
            devicePublicKey = "replacement-public-key",
            nonce = "recovery-nonce",
            expiresAt = "2026-08-03T12:00:00Z",
        )
        val signature = AccountMasterKeyCrypto.signRecoveryChallenge(challenge, unlocked.recoveryPrivateKey)
        val publicKey = KeyFactory.getInstance("EC").generatePublic(
            X509EncodedKeySpec(Base64.getDecoder().decode(bundle.credential.recoveryPublicKey)),
        )
        val verifier = Signature.getInstance("SHA256withECDSA").apply {
            initVerify(publicKey)
            update(recoveryCanonical(challenge).toByteArray(Charsets.UTF_8))
        }

        assertTrue(verifier.verify(Base64.getUrlDecoder().decode(signature)))
    }

    private fun recoveryCanonical(challenge: AccountRecoveryChallenge): String = listOf(
        "VIRTROID-ACCOUNT-RECOVERY-V1",
        challenge.id,
        challenge.nonce,
        challenge.accountId,
        challenge.deviceId,
        challenge.deviceName,
        challenge.devicePublicKey,
    ).joinToString("\n")
    @Test
    fun passwordChangeKeepsTheAccountKeyAndProvesTheOldPassword() {
        val deviceId = "22222222-2222-2222-2222-222222222222"
        val newPassword = "a completely different passphrase"
        val bundle = AccountMasterKeyCrypto.create(accountId, password)

        val rewrapped = AccountMasterKeyCrypto.rewrap(bundle.credential, deviceId, password, newPassword)

        // Same account key and recovery key, new envelope.
        assertEquals(bundle.blobAccessKey, rewrapped.blobAccessKey)
        assertEquals(bundle.credential.recoveryPublicKey, rewrapped.credential.recoveryPublicKey)
        assertEquals(bundle.credential.blobKeyVerifier, rewrapped.credential.blobKeyVerifier)
        assertNotEquals(bundle.credential.envelopeCiphertext, rewrapped.credential.envelopeCiphertext)
        assertEquals(bundle.blobAccessKey, AccountMasterKeyCrypto.unlock(rewrapped.credential, newPassword).blobAccessKey)
        try {
            AccountMasterKeyCrypto.unlock(rewrapped.credential, password)
            fail("the old password still opened the new envelope")
        } catch (_: IllegalArgumentException) {
        }

        // The proof is the recovery key's signature over this device and envelope.
        val verifier = Signature.getInstance("SHA256withECDSA")
        verifier.initVerify(
            KeyFactory.getInstance("EC").generatePublic(
                X509EncodedKeySpec(Base64.getDecoder().decode(bundle.credential.recoveryPublicKey)),
            ),
        )
        verifier.update(AccountMasterKeyCrypto.rewrapCanonical(rewrapped.credential, deviceId).toByteArray())
        assertTrue(verifier.verify(Base64.getUrlDecoder().decode(rewrapped.proof)))
    }

    @Test
    fun passwordChangeNeedsTheCurrentPassword() {
        val bundle = AccountMasterKeyCrypto.create(accountId, password)
        try {
            AccountMasterKeyCrypto.rewrap(bundle.credential, "device", "not the current password", "a new long password")
            fail("rewrapped without the current password")
        } catch (error: IllegalArgumentException) {
            assertTrue(error.message.orEmpty().contains("password", ignoreCase = true))
        }
    }

    @Test
    fun argon2idMatchesGo() {
        val salt = ByteArray(32) { (it + 1).toByte() }
        val derived = AccountMasterKeyCrypto.argon2id(password, salt, 3, 64 * 1024)
        // golang.org/x/crypto/argon2.IDKey with the same inputs.
        assertEquals(
            "649e676bce34fe3bb2ad2824c1c2079186717dd4adc0dda6bd0513f7f00cab33" +
                "6cbc0d200e2cec93d8660508df81e021101fd4bc476607a090600b71970ec02b",
            derived.joinToString("") { "%02x".format(it) },
        )
    }

    @Test
    fun newCredentialsUseArgon2idAndALoginKey() {
        val bundle = AccountMasterKeyCrypto.create(accountId, password)
        val credential = bundle.credential
        assertEquals(3, credential.version)
        assertEquals("ARGON2ID_V3", credential.kdfAlgorithm)
        assertEquals(3, credential.kdfIterations)
        assertEquals(64 * 1024, credential.kdfMemoryKiB)
        val keys = AccountMasterKeyCrypto.deriveKeys(
            AccountRecoveryParameters(credential.version, credential.kdfAlgorithm, credential.kdfSalt, credential.kdfIterations, credential.kdfMemoryKiB),
            password,
        )
        assertEquals(credential.authVerifier, AccountMasterKeyCrypto.loginKeyVerifier(keys.loginKey))
        assertEquals(bundle.blobAccessKey, AccountMasterKeyCrypto.unlock(credential, password, keys).blobAccessKey)
    }

    @Test
    fun loginKeyVerifierMatchesTheServer() {
        val loginKey = Base64.getUrlEncoder().withoutPadding().encodeToString(ByteArray(32) { 7 })
        // store.RecoveryLoginKeyVerifier in Go.
        assertEquals("aw370rarqujQKCqrA1vALRoR0_9-xxplx_vvJYP8pzg", AccountMasterKeyCrypto.loginKeyVerifier(loginKey))
    }

    @Test
    fun olderEnvelopesStillOpenAndAreUpgraded() {
        val created = AccountMasterKeyCrypto.create(accountId, password)
        val unlocked = AccountMasterKeyCrypto.unlock(created.credential, password)
        val legacy = AccountMasterKeyCrypto.seal(
            created.credential.copy(version = 2),
            password,
            Base64.getUrlDecoder().decode(unlocked.blobAccessKey),
            Base64.getDecoder().decode(unlocked.recoveryPrivateKey),
        )
        assertEquals(2, legacy.version)
        assertEquals("PBKDF2_HMAC_SHA256_V2", legacy.kdfAlgorithm)
        assertEquals("", legacy.authVerifier)
        assertEquals(created.blobAccessKey, AccountMasterKeyCrypto.unlock(legacy, password).blobAccessKey)
        val upgraded = AccountMasterKeyCrypto.rewrap(legacy, "22222222-2222-2222-2222-222222222222", password, password)
        assertEquals(3, upgraded.credential.version)
        assertTrue(upgraded.credential.authVerifier.isNotEmpty())
        assertEquals(created.blobAccessKey, AccountMasterKeyCrypto.unlock(upgraded.credential, password).blobAccessKey)
    }

    @Test
    fun rewrapProofCoversTheVersion3Fields() {
        val credential = AccountRecoveryCredential(
            accountId = accountId, version = 3, kdfAlgorithm = "ARGON2ID_V3", kdfSalt = "salt",
            kdfIterations = 3, envelopeAlgorithm = "AES_GCM_256_V1", envelopeIv = "iv", envelopeCiphertext = "ct",
            recoveryPublicKey = "pk", blobKeyVerifier = "bv", kdfMemoryKiB = 65536, authVerifier = "av",
        )
        // httpapi.rewrapProofCanonical in Go produces the same.
        assertEquals(
            "VIRTROID-ACCOUNT-RECOVERY-REWRAP-V2\n$accountId\ndevice\n3\nARGON2ID_V3\nsalt\n3\n65536\nav\niv\nct",
            AccountMasterKeyCrypto.rewrapCanonical(credential, "device"),
        )
    }
}
