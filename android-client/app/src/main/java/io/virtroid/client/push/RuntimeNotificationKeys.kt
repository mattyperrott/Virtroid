package io.virtroid.client.push

import android.content.Context
import android.util.Base64
import io.virtroid.client.security.KeystoreKeyPolicy
import java.security.KeyStore
import java.util.UUID
import javax.crypto.Cipher
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * Each runtime's notification key, which its VM seals notifications with
 * before they leave it. The phone derives one from the account key whenever it
 * hands a runtime its keys, and keeps it here wrapped by a Keystore key that
 * works while the phone is locked, when notifications arrive.
 */
class RuntimeNotificationKeys(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    fun save(runtimeId: String, key: ByteArray) {
        val id = canonicalRuntimeId(runtimeId) ?: return
        require(key.size == KEY_BYTES)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, wrappingKey())
        cipher.updateAAD(id.toByteArray(Charsets.UTF_8))
        val sealed = cipher.iv + cipher.doFinal(key)
        prefs.edit().putString(id, Base64.encodeToString(sealed, Base64.NO_WRAP)).apply()
    }

    /** The runtime's notification key, or null when this phone has none. */
    fun load(runtimeId: String): ByteArray? = runCatching {
        val id = canonicalRuntimeId(runtimeId) ?: return null
        val sealed = Base64.decode(prefs.getString(id, null) ?: return null, Base64.NO_WRAP)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, wrappingKey(), GCMParameterSpec(128, sealed, 0, IV_BYTES))
        cipher.updateAAD(id.toByteArray(Charsets.UTF_8))
        cipher.doFinal(sealed, IV_BYTES, sealed.size - IV_BYTES).takeIf { it.size == KEY_BYTES }
    }.getOrNull()

    /**
     * Opens a sealed event from [runtimeId]: the agent's JSON of its app, time
     * and title. Null without this runtime's key or for a tampered event.
     */
    fun open(runtimeId: String, eventId: String, sealed: ByteArray): String? {
        val key = load(runtimeId) ?: return null
        return try {
            if (sealed.size <= IV_BYTES + 16) return null
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, sealed, 0, IV_BYTES))
            cipher.updateAAD("$SEALED_CONTEXT\n$runtimeId\n$eventId".toByteArray(Charsets.UTF_8))
            String(cipher.doFinal(sealed, IV_BYTES, sealed.size - IV_BYTES), Charsets.UTF_8)
        } catch (error: Exception) {
            null
        } finally {
            key.fill(0)
        }
    }

    fun clear() {
        prefs.edit().clear().apply()
        runCatching { KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }.deleteEntry(KEY_ALIAS) }
    }

    private fun wrappingKey(): SecretKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        return KeystoreKeyPolicy.generateAesKey(KEY_ALIAS, unlockedDeviceRequired = false)
    }

    private fun canonicalRuntimeId(runtimeId: String): String? =
        runCatching { UUID.fromString(runtimeId.trim()).toString() }.getOrNull()

    companion object {
        private const val PREFS = "virtroid-runtime-notification-keys"
        private const val KEY_ALIAS = "virtroid-runtime-notification-keys-v1"
        private const val ANDROID_KEYSTORE = "AndroidKeyStore"
        private const val TRANSFORMATION = "AES/GCM/NoPadding"
        private const val IV_BYTES = 12
        private const val KEY_BYTES = 32
        private const val SEALED_CONTEXT = "virtroid-notification-v1"
    }
}
