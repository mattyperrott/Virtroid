package io.virtroid.client.security

import java.nio.ByteBuffer
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * Seals what the phone imports into a runtime (a file's name and contents)
 * for the runtime's attested VM, with a key derived from the runtime's viewer
 * key. The control plane relaying it reads neither and cannot forge one.
 * Must match virtnoded's runtime_import_seal.go.
 */
object RuntimeImportSeal {
    private val MAGIC = "VRTIMP1\n".toByteArray(Charsets.US_ASCII)
    private const val KEY_CONTEXT = "VIRTROID-RUNTIME-IMPORT-KEY-V1"
    private const val NONCE_SIZE = 12
    private const val MAX_NAME_BYTES = 255

    fun importKey(viewerKey: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(viewerKey, "HmacSHA256"))
        return mac.doFinal(KEY_CONTEXT.toByteArray(Charsets.UTF_8))
    }

    /** [kind] is "file", "photo" or "video", as the node's import route. */
    fun seal(
        viewerKey: ByteArray,
        runtimeId: String,
        kind: String,
        fileName: String,
        content: ByteArray,
        nonce: ByteArray = ByteArray(NONCE_SIZE).also { SecureRandom().nextBytes(it) },
    ): ByteArray {
        val name = fileName.toByteArray(Charsets.UTF_8)
        require(name.isNotEmpty() && name.size <= MAX_NAME_BYTES) { "file name must be 1 to 255 bytes" }
        require(nonce.size == NONCE_SIZE) { "nonce must be 12 bytes" }
        val plaintext = ByteBuffer.allocate(2 + name.size + content.size)
            .putShort(name.size.toShort())
            .put(name)
            .put(content)
            .array()
        val key = importKey(viewerKey)
        try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, nonce))
            cipher.updateAAD("virtroid-import-v1\n${runtimeId.trim()}\n$kind".toByteArray(Charsets.UTF_8))
            return MAGIC + nonce + cipher.doFinal(plaintext)
        } finally {
            key.fill(0)
            plaintext.fill(0)
        }
    }
}
