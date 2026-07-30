package io.virtroid.client.security

import android.content.Context
import android.util.Base64
import androidx.core.content.edit
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.util.UUID

class RuntimeCapabilityStore {
    fun rotate(runtimeId: String): String {
        clear(runtimeId)
        return publicKeyMaterial(runtimeId)
    }

    fun clear(runtimeId: String) {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        val alias = keyAlias(runtimeId)
        if (keyStore.containsAlias(alias)) {
            keyStore.deleteEntry(alias)
        }
        RuntimeCapabilityAliasRegistry.unregister(alias)
    }

    fun publicKeyMaterial(runtimeId: String): String {
        val alias = keyAlias(runtimeId)
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        val existingCertificate = keyStore.getCertificate(alias)
        if (existingCertificate != null) {
            RuntimeCapabilityAliasRegistry.register(alias)
            return Base64.encodeToString(existingCertificate.publicKey.encoded, Base64.NO_WRAP)
        }

        RuntimeCapabilityAliasRegistry.register(alias)

        val keyPair = KeystoreKeyPolicy.generateSigningKey(alias)
        return Base64.encodeToString(keyPair.public.encoded, Base64.NO_WRAP)
    }

    fun capabilityId(runtimeId: String, publicKeyMaterial: String = publicKeyMaterial(runtimeId)): String {
        val material = listOf(
            CAPABILITY_ID_CONTEXT,
            runtimeId.trim(),
            publicKeyMaterial.trim(),
        ).joinToString("\n")
        val digest = MessageDigest.getInstance("SHA-256")
            .digest(material.toByteArray(Charsets.UTF_8))
            .copyOf(16)
        return Base64.encodeToString(digest, B64_URL_FLAGS)
    }

    fun signedHeaders(
        method: String,
        requestUri: String,
        runtimeId: String,
        body: ByteArray,
    ): Map<String, String> {
        val publicKey = publicKeyMaterial(runtimeId)
        val capabilityId = capabilityId(runtimeId, publicKey)
        val timestamp = (System.currentTimeMillis() / 1000L).toString()
        val nonce = UUID.randomUUID().toString()
        val bodyHash = MessageDigest.getInstance("SHA-256")
            .digest(body)
            .let { Base64.encodeToString(it, B64_URL_FLAGS) }
        val canonical = listOf(
            SIGNATURE_CONTEXT,
            method.uppercase(),
            requestUri,
            capabilityId,
            timestamp,
            nonce,
            bodyHash,
        ).joinToString("\n")

        val signature = Signature.getInstance("SHA256withECDSA")
        signature.initSign(privateKey(runtimeId))
        signature.update(canonical.toByteArray(Charsets.UTF_8))
        val signed = Base64.encodeToString(signature.sign(), B64_URL_FLAGS)

        return mapOf(
            "X-Virtroid-Capability-ID" to capabilityId,
            "X-Virtroid-Capability-Timestamp" to timestamp,
            "X-Virtroid-Capability-Nonce" to nonce,
            "X-Virtroid-Capability-Body-SHA256" to bodyHash,
            "X-Virtroid-Capability-Signature" to signed,
        )
    }

    private fun privateKey(runtimeId: String): PrivateKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        return keyStore.getKey(keyAlias(runtimeId), null) as PrivateKey
    }

    private fun keyAlias(runtimeId: String): String {
        return RuntimeCapabilityAliases.forRuntime(runtimeId)
    }

    companion object {
        fun initialize(context: Context) {
            RuntimeCapabilityAliasRegistry.initialize(context)
        }

        fun clearAllRegistered(context: Context) {
            RuntimeCapabilityAliasRegistry.initialize(context)
            val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
            val discoveredAliases = keyStore.aliases().toList()
                .filter(RuntimeCapabilityAliases::isManaged)
            val aliases = RuntimeCapabilityAliasRegistry.aliases() + discoveredAliases
            aliases
                .filter(RuntimeCapabilityAliases::isManaged)
                .forEach { alias ->
                    if (keyStore.containsAlias(alias)) {
                        keyStore.deleteEntry(alias)
                    }
                }
            RuntimeCapabilityAliasRegistry.clear()
        }

        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val CAPABILITY_ID_CONTEXT = "VIRTROID-RUNTIME-CAPABILITY-ID-V1"
        const val SIGNATURE_CONTEXT = "VIRTROID-CAPABILITY-SIGNATURE-V1"
        const val B64_URL_FLAGS = Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
    }
}

private object RuntimeCapabilityAliasRegistry {
    private const val PREFS_NAME = "virtroid-runtime-capability-aliases"
    private const val KEY_ALIASES = "aliases"
    private val lock = Any()

    @Volatile
    private var applicationContext: Context? = null

    fun initialize(context: Context) {
        applicationContext = context.applicationContext
    }

    fun register(alias: String) {
        require(RuntimeCapabilityAliases.isManaged(alias)) { "Refusing to register an unmanaged key alias" }
        synchronized(lock) {
            val prefs = preferences()
            val aliases = prefs.getStringSet(KEY_ALIASES, emptySet()).orEmpty().toMutableSet()
            if (aliases.add(alias)) {
                prefs.edit { putStringSet(KEY_ALIASES, aliases) }
            }
        }
    }

    fun unregister(alias: String) {
        synchronized(lock) {
            val prefs = preferences()
            val aliases = prefs.getStringSet(KEY_ALIASES, emptySet()).orEmpty().toMutableSet()
            if (aliases.remove(alias)) {
                prefs.edit { putStringSet(KEY_ALIASES, aliases) }
            }
        }
    }

    fun aliases(): Set<String> = synchronized(lock) {
        preferences().getStringSet(KEY_ALIASES, emptySet()).orEmpty().toSet()
    }

    fun clear() {
        synchronized(lock) {
            preferences().edit { remove(KEY_ALIASES) }
        }
    }

    private fun preferences() = checkNotNull(applicationContext) {
        "Runtime capability alias registry has not been initialized"
    }.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
}
