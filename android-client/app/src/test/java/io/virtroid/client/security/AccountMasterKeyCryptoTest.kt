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
}
