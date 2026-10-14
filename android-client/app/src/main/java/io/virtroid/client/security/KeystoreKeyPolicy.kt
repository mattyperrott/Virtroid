package io.virtroid.client.security

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.security.keystore.StrongBoxUnavailableException
import java.security.KeyPair
import java.security.KeyPairGenerator
import java.security.ProviderException
import java.security.spec.ECGenParameterSpec
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey

internal object KeystoreKeyPolicy {
    /**
     * A P-256 signing key. [unlockedDeviceRequired] keeps it unusable while
     * the phone is locked; only a key that must work in the background, such
     * as the notification relay's, turns that off.
     */
    fun generateSigningKey(alias: String, unlockedDeviceRequired: Boolean = true): KeyPair {
        return try {
            generateSigningKey(alias, strongBox = true, unlockedDeviceRequired)
        } catch (_: StrongBoxUnavailableException) {
            generateSigningKey(alias, strongBox = false, unlockedDeviceRequired)
        } catch (_: ProviderException) {
            generateSigningKey(alias, strongBox = false, unlockedDeviceRequired)
        }
    }

    fun generateAesKey(alias: String): SecretKey {
        return try {
            generateAesKey(alias, strongBox = true)
        } catch (_: StrongBoxUnavailableException) {
            generateAesKey(alias, strongBox = false)
        } catch (_: ProviderException) {
            generateAesKey(alias, strongBox = false)
        }
    }

    private fun generateSigningKey(alias: String, strongBox: Boolean, unlockedDeviceRequired: Boolean): KeyPair {
        val generator = KeyPairGenerator.getInstance(
            KeyProperties.KEY_ALGORITHM_EC,
            ANDROID_KEYSTORE,
        )
        val spec = KeyGenParameterSpec.Builder(
            alias,
            KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY,
        )
            .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA512)
            .setUnlockedDeviceRequired(unlockedDeviceRequired)
            .setIsStrongBoxBacked(strongBox)
            .build()
        generator.initialize(spec)
        return generator.generateKeyPair()
    }

    private fun generateAesKey(alias: String, strongBox: Boolean): SecretKey {
        val generator = KeyGenerator.getInstance(
            KeyProperties.KEY_ALGORITHM_AES,
            ANDROID_KEYSTORE,
        )
        val spec = KeyGenParameterSpec.Builder(
            alias,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setRandomizedEncryptionRequired(true)
            .setUnlockedDeviceRequired(true)
            .setIsStrongBoxBacked(strongBox)
            .build()
        generator.init(spec)
        return generator.generateKey()
    }

    private const val ANDROID_KEYSTORE = "AndroidKeyStore"
}
