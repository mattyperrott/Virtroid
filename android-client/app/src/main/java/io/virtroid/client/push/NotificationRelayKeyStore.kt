package io.virtroid.client.push

import android.util.Base64
import io.virtroid.client.security.KeystoreKeyPolicy
import io.virtroid.client.security.signedRequestHeaders
import java.security.KeyStore
import java.security.PrivateKey

/**
 * The notification relay's own signing key. Unlike the device key it works
 * while the phone is locked, so the relay can reconnect in the background;
 * the control plane accepts it only for the notification stream and its
 * acknowledgements.
 */
class NotificationRelayKeyStore {
    fun publicKeyMaterial(): String {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        val publicKey = keyStore.getCertificate(KEY_ALIAS)?.publicKey
            ?: KeystoreKeyPolicy.generateSigningKey(KEY_ALIAS, unlockedDeviceRequired = false).public
        return Base64.encodeToString(publicKey.encoded, Base64.NO_WRAP)
    }

    fun signedHeaders(
        method: String,
        requestUri: String,
        accountId: String,
        deviceId: String,
        body: ByteArray,
    ): Map<String, String> {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        val privateKey = keyStore.getKey(KEY_ALIAS, null) as? PrivateKey
            ?: throw IllegalStateException("notification relay key is not registered")
        return signedRequestHeaders(privateKey, SIGNATURE_CONTEXT, method, requestUri, accountId, deviceId, body) +
            ("X-Virtroid-Signing-Key" to SIGNING_KEY_NAME)
    }

    fun deleteKey() {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        if (keyStore.containsAlias(KEY_ALIAS)) {
            keyStore.deleteEntry(KEY_ALIAS)
        }
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "virtroid_notification_relay_key_v1"
        const val SIGNATURE_CONTEXT = "VIRTROID-RELAY-SIGNATURE-V1"
        const val SIGNING_KEY_NAME = "notification-relay"
    }
}
