package io.virtroid.client.security

import android.content.Context
import android.util.Base64
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Arrays
import javax.crypto.Cipher
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

class SecureLocalVault private constructor(context: Context) {
    private val appContext = context.applicationContext
    private val vaultFile = File(appContext.filesDir, "secure-vault/local-state.v1")
    private val lock = Any()
    private var vaultKey: ByteArray? = null
    private var document = JSONObject()

    val isUnlocked: Boolean
        get() = synchronized(lock) { vaultKey != null }

    val exists: Boolean
        get() = vaultFile.exists()

    fun unlockOrCreate(secret: String, saltB64: String): Boolean {
        val derivedKey = deriveKey(secret, saltB64) ?: return false
        synchronized(lock) {
            return runCatching {
                document = if (vaultFile.exists()) {
                    JSONObject(decryptFile(derivedKey))
                } else {
                    JSONObject()
                }
                replaceKeyLocked(derivedKey)
                persistLocked()
                true
            }.getOrElse {
                Arrays.fill(derivedKey, 0)
                false
            }
        }
    }

    fun lock() {
        synchronized(lock) {
            vaultKey?.let { Arrays.fill(it, 0) }
            vaultKey = null
            document = JSONObject()
        }
    }

    fun destroy() {
        synchronized(lock) {
            lock()
            if (vaultFile.exists()) {
                vaultFile.delete()
            }
        }
    }

    fun contains(namespace: String, key: String): Boolean {
        return synchronized(lock) {
            if (vaultKey == null) return@synchronized false
            document.optJSONObject(namespace)?.has(key) == true
        }
    }

    fun getString(namespace: String, key: String, defaultValue: String? = null): String? {
        return synchronized(lock) {
            if (vaultKey == null) return@synchronized defaultValue
            val value = document.optJSONObject(namespace)?.opt(key) ?: return@synchronized defaultValue
            if (value == JSONObject.NULL) defaultValue else value.toString()
        }
    }

    fun getBoolean(namespace: String, key: String, defaultValue: Boolean): Boolean {
        return synchronized(lock) {
            if (vaultKey == null) return@synchronized defaultValue
            when (val value = document.optJSONObject(namespace)?.opt(key)) {
                is Boolean -> value
                is String -> value.toBooleanStrictOrNull() ?: defaultValue
                else -> defaultValue
            }
        }
    }

    fun getInt(namespace: String, key: String, defaultValue: Int): Int {
        return synchronized(lock) {
            if (vaultKey == null) return@synchronized defaultValue
            when (val value = document.optJSONObject(namespace)?.opt(key)) {
                is Number -> value.toInt()
                is String -> value.toIntOrNull() ?: defaultValue
                else -> defaultValue
            }
        }
    }

    fun getLong(namespace: String, key: String, defaultValue: Long): Long {
        return synchronized(lock) {
            if (vaultKey == null) return@synchronized defaultValue
            when (val value = document.optJSONObject(namespace)?.opt(key)) {
                is Number -> value.toLong()
                is String -> value.toLongOrNull() ?: defaultValue
                else -> defaultValue
            }
        }
    }

    fun putString(namespace: String, key: String, value: String?) {
        putValue(namespace, key, value)
    }

    fun putBoolean(namespace: String, key: String, value: Boolean) {
        putValue(namespace, key, value)
    }

    fun putInt(namespace: String, key: String, value: Int) {
        putValue(namespace, key, value)
    }

    fun putLong(namespace: String, key: String, value: Long) {
        putValue(namespace, key, value)
    }

    fun remove(namespace: String, key: String) {
        synchronized(lock) {
            if (vaultKey == null) return
            document.optJSONObject(namespace)?.remove(key)
            persistLocked()
        }
    }

    fun clearNamespace(namespace: String) {
        synchronized(lock) {
            if (vaultKey == null) return
            document.remove(namespace)
            persistLocked()
        }
    }

    private fun putValue(namespace: String, key: String, value: Any?) {
        synchronized(lock) {
            if (vaultKey == null) return
            val target = document.optJSONObject(namespace) ?: JSONObject().also {
                document.put(namespace, it)
            }
            if (value == null) {
                target.remove(key)
            } else {
                target.put(key, value)
            }
            persistLocked()
        }
    }

    private fun replaceKeyLocked(key: ByteArray) {
        vaultKey?.let { Arrays.fill(it, 0) }
        vaultKey = key
    }

    private fun persistLocked() {
        val key = vaultKey ?: return
        val iv = ByteArray(GCM_IV_BYTES).also { SecureRandom().nextBytes(it) }
        val cipher = Cipher.getInstance(AES_MODE)
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, KEY_ALGORITHM), GCMParameterSpec(GCM_TAG_BITS, iv))
        val ciphertext = cipher.doFinal(document.toString().toByteArray(Charsets.UTF_8))
        val payload = ByteArray(iv.size + ciphertext.size)
        System.arraycopy(iv, 0, payload, 0, iv.size)
        System.arraycopy(ciphertext, 0, payload, iv.size, ciphertext.size)

        vaultFile.parentFile?.mkdirs()
        val tmp = File(vaultFile.parentFile, "${vaultFile.name}.tmp")
        tmp.writeText(Base64.encodeToString(payload, B64_FLAGS), Charsets.UTF_8)
        if (!tmp.renameTo(vaultFile)) {
            tmp.delete()
            throw IllegalStateException("could not commit secure vault")
        }
    }

    private fun decryptFile(key: ByteArray): String {
        val payload = Base64.decode(vaultFile.readText(Charsets.UTF_8).trim(), B64_FLAGS)
        require(payload.size > GCM_IV_BYTES)
        val iv = payload.copyOfRange(0, GCM_IV_BYTES)
        val ciphertext = payload.copyOfRange(GCM_IV_BYTES, payload.size)
        val cipher = Cipher.getInstance(AES_MODE)
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, KEY_ALGORITHM), GCMParameterSpec(GCM_TAG_BITS, iv))
        return String(cipher.doFinal(ciphertext), Charsets.UTF_8)
    }

    private fun deriveKey(secret: String, saltB64: String): ByteArray? {
        val salt = runCatching { Base64.decode(saltB64, B64_FLAGS) }.getOrNull() ?: return null
        val spec = PBEKeySpec(secret.toCharArray(), salt, PBKDF2_ITERATIONS, KEY_BYTES * 8)
        return SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256")
            .generateSecret(spec)
            .encoded
    }

    companion object {
        private const val AES_MODE = "AES/GCM/NoPadding"
        private const val KEY_ALGORITHM = "AES"
        private const val GCM_IV_BYTES = 12
        private const val GCM_TAG_BITS = 128
        private const val PBKDF2_ITERATIONS = 210_000
        private const val KEY_BYTES = 32
        private const val B64_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE

        @Volatile
        private var instance: SecureLocalVault? = null

        fun get(context: Context): SecureLocalVault {
            return instance ?: synchronized(this) {
                instance ?: SecureLocalVault(context.applicationContext).also { instance = it }
            }
        }

        fun constantTimeEquals(left: String, right: String): Boolean {
            return MessageDigest.isEqual(left.toByteArray(Charsets.UTF_8), right.toByteArray(Charsets.UTF_8))
        }
    }
}
