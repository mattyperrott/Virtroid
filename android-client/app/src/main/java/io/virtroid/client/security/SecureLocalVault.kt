package io.virtroid.client.security

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import org.json.JSONObject
import java.io.File
import java.security.KeyStore
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Arrays
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

class SecureLocalVault private constructor(context: Context) {
    private val appContext = context.applicationContext
    private val keyPrefs = appContext.getSharedPreferences("virtroid-local-vault-key", Context.MODE_PRIVATE)
    private val vaultFile = File(appContext.filesDir, "secure-vault/local-state.v1")
    private val lock = Any()
    private var vaultKey: ByteArray? = null
    private var document = JSONObject()

    val isUnlocked: Boolean
        get() = synchronized(lock) { vaultKey != null }

    val exists: Boolean
        get() = vaultFile.exists()

    fun currentKeyCopy(): ByteArray? {
        return synchronized(lock) {
            vaultKey?.copyOf()
        }
    }

    fun unlockOrCreate(secret: String, saltB64: String): Boolean {
        val secretKey = deriveKey(secret, saltB64) ?: return false
        synchronized(lock) {
            val isVersioned = vaultFile.exists() && isVersionedVaultFile()
            var dataKey: ByteArray? = null
            return runCatching {
                document = when {
                    vaultFile.exists() && isVersioned -> {
                        dataKey = unwrapDataKeyLocked(secretKey)
                            ?: throw IllegalStateException("versioned vault is not bound to app-lock secret and device keystore")
                        JSONObject(decryptVersionedFile(dataKey!!))
                    }
                    vaultFile.exists() -> {
                        val plaintext = decryptLegacyFile(secretKey)
                        dataKey = newHardwareSecretBoundDataKeyLocked(secretKey)
                        JSONObject(plaintext)
                    }
                    else -> {
                        dataKey = newHardwareSecretBoundDataKeyLocked(secretKey)
                        JSONObject()
                    }
                }
                val unlockedKey = dataKey ?: throw IllegalStateException("secure vault key unavailable")
                replaceKeyLocked(unlockedKey)
                persistLocked()
                clearLegacyWrappedDataKeyPrefsLocked()
                true
            }.getOrElse {
                vaultKey = null
                document = JSONObject()
                dataKey?.let { key -> Arrays.fill(key, 0) }
                false
            }.also {
                Arrays.fill(secretKey, 0)
            }
        }
    }

    fun unlockWithKey(key: ByteArray): Boolean {
        val keyCopy = key.copyOf()
        synchronized(lock) {
            return runCatching {
                document = if (vaultFile.exists()) {
                    if (!isVersionedVaultFile()) {
                        throw IllegalStateException("legacy vault requires PIN migration before biometric unlock")
                    }
                    JSONObject(decryptVersionedFile(keyCopy))
                } else {
                    JSONObject()
                }
                replaceKeyLocked(keyCopy)
                true
            }.getOrElse {
                Arrays.fill(keyCopy, 0)
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
            keyPrefs.edit().clear().apply()
            runCatching {
                keyStore().deleteEntry(KEYSTORE_KEY_ALIAS)
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
        val envelope = JSONObject()
            .put("version", VAULT_VERSION)
            .put("iv", Base64.encodeToString(iv, B64_FLAGS))
            .put("ciphertext", Base64.encodeToString(ciphertext, B64_FLAGS))

        vaultFile.parentFile?.mkdirs()
        val tmp = File(vaultFile.parentFile, "${vaultFile.name}.tmp")
        tmp.writeText(envelope.toString(), Charsets.UTF_8)
        if (!tmp.renameTo(vaultFile)) {
            tmp.delete()
            throw IllegalStateException("could not commit secure vault")
        }
    }

    private fun decryptVersionedFile(key: ByteArray): String {
        val envelope = JSONObject(vaultFile.readText(Charsets.UTF_8).trim())
        require(envelope.optInt("version") == VAULT_VERSION)
        val iv = Base64.decode(envelope.getString("iv"), B64_FLAGS)
        val ciphertext = Base64.decode(envelope.getString("ciphertext"), B64_FLAGS)
        val cipher = Cipher.getInstance(AES_MODE)
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, KEY_ALGORITHM), GCMParameterSpec(GCM_TAG_BITS, iv))
        return String(cipher.doFinal(ciphertext), Charsets.UTF_8)
    }

    private fun decryptLegacyFile(key: ByteArray): String {
        val payload = Base64.decode(vaultFile.readText(Charsets.UTF_8).trim(), B64_FLAGS)
        require(payload.size > GCM_IV_BYTES)
        val iv = payload.copyOfRange(0, GCM_IV_BYTES)
        val ciphertext = payload.copyOfRange(GCM_IV_BYTES, payload.size)
        val cipher = Cipher.getInstance(AES_MODE)
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, KEY_ALGORITHM), GCMParameterSpec(GCM_TAG_BITS, iv))
        return String(cipher.doFinal(ciphertext), Charsets.UTF_8)
    }

    private fun isVersionedVaultFile(): Boolean {
        return vaultFile.readText(Charsets.UTF_8).trimStart().startsWith("{")
    }

    private fun newHardwareSecretBoundDataKeyLocked(secretKey: ByteArray): ByteArray {
        return ByteArray(KEY_BYTES).also {
            SecureRandom().nextBytes(it)
            wrapHardwareSecretBoundDataKeyLocked(it, secretKey)
        }
    }

    private fun unwrapDataKeyLocked(secretKey: ByteArray): ByteArray? {
        unwrapHardwareSecretBoundDataKeyLocked(secretKey)?.let { return it }

        unwrapSecretOnlyDataKeyForMigrationLocked(secretKey)?.let { dataKey ->
            wrapHardwareSecretBoundDataKeyLocked(dataKey, secretKey)
            return dataKey
        }

        return null
    }

    private fun unwrapHardwareSecretBoundDataKeyLocked(secretKey: ByteArray): ByteArray? {
        val iv = decodeKeyPref(KEY_HARDWARE_SECRET_WRAPPED_DEK_IV) ?: return null
        val ciphertext = decodeKeyPref(KEY_HARDWARE_SECRET_WRAPPED_DEK_CIPHERTEXT) ?: return null
        return LocalVaultKeyEnvelope.unwrapHardwareSecretBoundDataKey(
            LocalVaultKeyEnvelope.Wrapped(iv, ciphertext),
            secretKey,
            getOrCreateKeystoreKey(),
        )
    }

    private fun wrapHardwareSecretBoundDataKeyLocked(dataKey: ByteArray, secretKey: ByteArray) {
        val wrapped = LocalVaultKeyEnvelope.wrapHardwareSecretBoundDataKey(
            dataKey,
            secretKey,
            getOrCreateKeystoreKey(),
        )
        keyPrefs.edit()
            .putString(KEY_HARDWARE_SECRET_WRAPPED_DEK_IV, Base64.encodeToString(wrapped.iv, B64_FLAGS))
            .putString(KEY_HARDWARE_SECRET_WRAPPED_DEK_CIPHERTEXT, Base64.encodeToString(wrapped.ciphertext, B64_FLAGS))
            .apply()
        Arrays.fill(wrapped.iv, 0)
        Arrays.fill(wrapped.ciphertext, 0)
    }

    private fun unwrapSecretOnlyDataKeyForMigrationLocked(secretKey: ByteArray): ByteArray? {
        val iv = decodeKeyPref(KEY_SECRET_WRAPPED_DEK_IV) ?: return null
        val ciphertext = decodeKeyPref(KEY_SECRET_WRAPPED_DEK_CIPHERTEXT) ?: return null
        return LocalVaultKeyEnvelope.unwrapSecretOnlyDataKey(
            LocalVaultKeyEnvelope.Wrapped(iv, ciphertext),
            secretKey,
        )
    }

    private fun clearLegacyWrappedDataKeyPrefsLocked() {
        keyPrefs.edit()
            .remove(KEY_WRAPPED_DEK_IV)
            .remove(KEY_WRAPPED_DEK_CIPHERTEXT)
            .remove(KEY_SECRET_WRAPPED_DEK_IV)
            .remove(KEY_SECRET_WRAPPED_DEK_CIPHERTEXT)
            .apply()
    }

    private fun decodeKeyPref(key: String): ByteArray? {
        val value = keyPrefs.getString(key, null)?.takeIf { it.isNotBlank() } ?: return null
        return runCatching { Base64.decode(value, B64_FLAGS) }.getOrNull()
    }

    private fun keyStore(): KeyStore {
        return KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
    }

    private fun getOrCreateKeystoreKey(): SecretKey {
        keyStore().getKey(KEYSTORE_KEY_ALIAS, null)?.let { return it as SecretKey }

        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        val spec = KeyGenParameterSpec.Builder(
            KEYSTORE_KEY_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setRandomizedEncryptionRequired(true)
            .build()
        generator.init(spec)
        return generator.generateKey()
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
        private const val VAULT_VERSION = 2
        private const val ANDROID_KEYSTORE = "AndroidKeyStore"
        private const val KEYSTORE_KEY_ALIAS = "virtroid_local_vault_master_v2"
        private const val KEY_WRAPPED_DEK_IV = "wrapped_dek_iv"
        private const val KEY_WRAPPED_DEK_CIPHERTEXT = "wrapped_dek_ciphertext"
        private const val KEY_SECRET_WRAPPED_DEK_IV = "secret_wrapped_dek_iv"
        private const val KEY_SECRET_WRAPPED_DEK_CIPHERTEXT = "secret_wrapped_dek_ciphertext"
        private const val KEY_HARDWARE_SECRET_WRAPPED_DEK_IV = "hardware_secret_wrapped_dek_iv"
        private const val KEY_HARDWARE_SECRET_WRAPPED_DEK_CIPHERTEXT = "hardware_secret_wrapped_dek_ciphertext"

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
