package io.virtroid.client.security

import org.json.JSONObject
import java.security.KeyFactory
import java.security.PublicKey
import java.security.Signature
import java.security.spec.X509EncodedKeySpec
import java.util.Base64

/**
 * A verified list of approved runtime VM launch measurements, with when it
 * expires (milliseconds, null for lists from before expiry existed) and, per
 * AMD product, the oldest host firmware it accepts.
 */
data class MeasurementList(
    val serial: Long,
    val measurements: Set<String>,
    val expiresAtMillis: Long? = null,
    val minimumFirmware: Map<String, SnpTcb> = emptyMap(),
) {
    /** Whether the list may be used now: lists without an expiry may not. */
    fun isCurrent(nowMillis: Long): Boolean = expiresAtMillis != null && nowMillis < expiresAtMillis
}

/**
 * The list of approved runtime VM images, signed offline with a release key
 * whose public half is built into the app (`res/raw/measurement_signing_keys`).
 * The control plane serves it but cannot change it, and the app never accepts
 * a lower serial than one it has seen, so an old list that approved a since
 * revoked image cannot be replayed.
 */
object SignedMeasurementList {
    private const val DOMAIN = "virtroid-snp-measurements-v1\n"

    fun parseKeys(lines: List<String>): List<PublicKey> = lines
        .map { it.substringBefore('#').trim() }
        .filter { it.isNotEmpty() }
        .mapNotNull { material ->
            try {
                KeyFactory.getInstance("EC").generatePublic(X509EncodedKeySpec(Base64.getDecoder().decode(material)))
            } catch (error: Exception) {
                null
            }
        }

    fun verify(document: String, trustedKeys: List<PublicKey>): MeasurementList {
        if (trustedKeys.isEmpty()) {
            throw SnpAttestationException("this app has no measurement signing key")
        }
        val (payload, signature) = try {
            val signed = JSONObject(document)
            Base64.getDecoder().decode(signed.getString("payload")) to
                Base64.getDecoder().decode(signed.getString("signature"))
        } catch (error: Exception) {
            throw SnpAttestationException("measurement list is malformed")
        }
        val signedBytes = DOMAIN.toByteArray(Charsets.US_ASCII) + payload
        val trusted = trustedKeys.any { key ->
            try {
                Signature.getInstance("SHA256withECDSA").run {
                    initVerify(key)
                    update(signedBytes)
                    verify(signature)
                }
            } catch (error: Exception) {
                false
            }
        }
        if (!trusted) {
            throw SnpAttestationException("measurement list is not signed by a trusted key")
        }
        return try {
            val list = JSONObject(String(payload, Charsets.UTF_8))
            val serial = list.getLong("serial")
            if (list.getInt("version") != 1 || serial <= 0) {
                throw SnpAttestationException("unsupported measurement list")
            }
            val entries = list.getJSONArray("measurements")
            val measurements = (0 until entries.length()).map { entries.getString(it).lowercase() }.toSet()
            if (measurements.any { it.length != 96 || !it.all { char -> char in '0'..'9' || char in 'a'..'f' } }) {
                throw SnpAttestationException("measurement list holds an invalid measurement")
            }
            val expiresAt = list.optString("expires_at").takeIf { it.isNotEmpty() }?.let {
                java.time.Instant.parse(it).toEpochMilli()
            }
            val minimumFirmware = mutableMapOf<String, SnpTcb>()
            list.optJSONObject("min_tcb")?.let { products ->
                for (product in products.keys()) {
                    val levels = products.getJSONObject(product)
                    fun level(name: String): Int = levels.optInt(name, 0).also {
                        if (it !in 0..255) throw SnpAttestationException("measurement list holds an invalid firmware level")
                    }
                    minimumFirmware[product] = SnpTcb(
                        bootloader = level("bootloader"),
                        tee = level("tee"),
                        snp = level("snp"),
                        microcode = level("microcode"),
                        fmc = level("fmc"),
                    )
                }
            }
            MeasurementList(serial, measurements, expiresAt, minimumFirmware)
        } catch (error: SnpAttestationException) {
            throw error
        } catch (error: Exception) {
            throw SnpAttestationException("measurement list payload is malformed")
        }
    }
}
