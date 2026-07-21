package io.virtroid.client.security

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Test
import java.security.SecureRandom
import javax.crypto.spec.SecretKeySpec

class LocalVaultKeyEnvelopeTest {
    @Test
    fun hardwareSecretBoundEnvelopeRequiresSecretAndHardwareKey() {
        val dataKey = randomBytes(32)
        val secretKey = randomBytes(32)
        val wrongSecretKey = randomBytes(32)
        val hardwareKey = aesKey()
        val wrongHardwareKey = aesKey()

        val wrapped = LocalVaultKeyEnvelope.wrapHardwareSecretBoundDataKey(dataKey, secretKey, hardwareKey)

        assertArrayEquals(
            dataKey,
            LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(wrapped, secretKey, hardwareKey),
        )
        assertNull(LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(wrapped, wrongSecretKey, hardwareKey))
        assertNull(LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(wrapped, secretKey, wrongHardwareKey))
    }

    @Test
    fun secretOnlyEnvelopeCanBeMigratedIntoHardwareSecretBoundEnvelope() {
        val dataKey = randomBytes(32)
        val secretKey = randomBytes(32)
        val hardwareKey = aesKey()
        val copiedDeviceHardwareKey = aesKey()

        val secretOnly = LocalVaultKeyEnvelope.wrapSecretOnlyDataKey(dataKey, secretKey)
        val recoveredSecretOnly = LocalVaultKeyEnvelope.unwrapSecretOnlyDataKey(secretOnly, secretKey)
        val migrated = LocalVaultKeyEnvelope.wrapHardwareSecretBoundDataKey(
            recoveredSecretOnly!!,
            secretKey,
            hardwareKey,
        )

        assertArrayEquals(
            dataKey,
            LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(migrated, secretKey, hardwareKey),
        )
        assertNull(
            LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(
                migrated,
                secretKey,
                copiedDeviceHardwareKey,
            ),
        )
    }

    @Test
    fun repeatedWrappingUsesFreshProviderGeneratedIvs() {
        val dataKey = randomBytes(32)
        val secretKey = randomBytes(32)
        val hardwareKey = aesKey()

        val first = LocalVaultKeyEnvelope.wrapHardwareSecretBoundDataKey(dataKey, secretKey, hardwareKey)
        val second = LocalVaultKeyEnvelope.wrapHardwareSecretBoundDataKey(dataKey, secretKey, hardwareKey)

        assertFalse(first.iv.contentEquals(second.iv))
        assertArrayEquals(
            dataKey,
            LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(first, secretKey, hardwareKey),
        )
        assertArrayEquals(
            dataKey,
            LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(second, secretKey, hardwareKey),
        )
    }

    private fun aesKey() = SecretKeySpec(randomBytes(32), "AES")

    private fun randomBytes(size: Int): ByteArray {
        return ByteArray(size).also { secureRandom.nextBytes(it) }
    }

    private companion object {
        val secureRandom = SecureRandom()
    }
}
