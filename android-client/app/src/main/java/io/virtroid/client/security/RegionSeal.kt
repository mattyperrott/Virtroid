package io.virtroid.client.security

import org.json.JSONObject
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/** Where a runtime's Android believes it is: a place and its timezone. */
data class RuntimeRegion(
    val latitude: Double,
    val longitude: Double,
    val timezone: String,
    /** What the phone shows, e.g. "Berlin, Germany"; the VM ignores it. */
    val label: String,
)

/**
 * Seals a runtime's region for its attested VM with a key derived from the
 * runtime's viewer key, so the control plane that stores and relays it
 * learns neither the place nor its timezone. Must match virtnoded's region.go.
 */
object RegionSeal {
    private val MAGIC = "VRTREG1\n".toByteArray(Charsets.US_ASCII)
    private const val KEY_CONTEXT = "VIRTROID-RUNTIME-REGION-KEY-V1"
    private const val NONCE_SIZE = 12
    private const val TAG_BITS = 128
    private val TIMEZONE_NAME = Regex("^[A-Za-z][A-Za-z0-9_+-]{0,31}(/[A-Za-z0-9][A-Za-z0-9_+-]{0,31}){0,2}$")

    fun regionKey(viewerKey: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(viewerKey, "HmacSHA256"))
        return mac.doFinal(KEY_CONTEXT.toByteArray(Charsets.UTF_8))
    }

    /** The sealed region as unpadded base64url, as the control plane stores it. */
    fun seal(
        viewerKey: ByteArray,
        runtimeId: String,
        region: RuntimeRegion,
        nonce: ByteArray = ByteArray(NONCE_SIZE).also { SecureRandom().nextBytes(it) },
    ): String {
        require(region.latitude in -90.0..90.0 && region.longitude in -180.0..180.0) { "coordinates are out of range" }
        require(TIMEZONE_NAME.matches(region.timezone)) { "timezone must be an IANA name" }
        require(nonce.size == NONCE_SIZE) { "nonce must be 12 bytes" }
        val plaintext = JSONObject()
            .put("version", 1)
            .put("latitude", region.latitude)
            .put("longitude", region.longitude)
            .put("timezone", region.timezone)
            .put("label", region.label)
            .toString()
            .toByteArray(Charsets.UTF_8)
        val key = regionKey(viewerKey)
        try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(TAG_BITS, nonce))
            cipher.updateAAD(aad(runtimeId))
            return Base64.getUrlEncoder().withoutPadding().encodeToString(MAGIC + nonce + cipher.doFinal(plaintext))
        } finally {
            key.fill(0)
            plaintext.fill(0)
        }
    }

    /** Opens a region this account's phones sealed, or null if it is not one. */
    fun open(viewerKey: ByteArray, runtimeId: String, sealed: String): RuntimeRegion? {
        val bytes = runCatching { Base64.getUrlDecoder().decode(sealed.trim()) }.getOrNull() ?: return null
        val header = MAGIC.size + NONCE_SIZE
        if (bytes.size < header + TAG_BITS / 8 || !bytes.copyOfRange(0, MAGIC.size).contentEquals(MAGIC)) return null
        val key = regionKey(viewerKey)
        return try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(TAG_BITS, bytes, MAGIC.size, NONCE_SIZE))
            cipher.updateAAD(aad(runtimeId))
            val json = JSONObject(String(cipher.doFinal(bytes, header, bytes.size - header), Charsets.UTF_8))
            if (json.optInt("version") != 1) return null
            RuntimeRegion(
                latitude = json.getDouble("latitude"),
                longitude = json.getDouble("longitude"),
                timezone = json.getString("timezone"),
                label = json.optString("label"),
            )
        } catch (_: Exception) {
            null
        } finally {
            key.fill(0)
        }
    }

    /** The runtime's viewer key, from the account key the phone has unlocked. */
    fun viewerKey(blobAccessKey: String, runtimeId: String): ByteArray =
        android.util.Base64.decode(BlobKeyEnvelopeCrypto.runtimeViewerKey(blobAccessKey, runtimeId), android.util.Base64.NO_WRAP)

    private fun aad(runtimeId: String): ByteArray =
        "virtroid-region-v1\n${runtimeId.trim()}".toByteArray(Charsets.UTF_8)
}
