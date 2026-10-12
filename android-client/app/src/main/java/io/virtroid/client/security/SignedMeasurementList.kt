package io.virtroid.client.security

import org.json.JSONObject
import java.security.KeyFactory
import java.security.PublicKey
import java.security.Signature
import java.security.spec.X509EncodedKeySpec
import java.util.Base64

/** A verified list of approved runtime VM launch measurements. */
data class MeasurementList(val serial: Long, val measurements: Set<String>)

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
            MeasurementList(serial, measurements)
        } catch (error: SnpAttestationException) {
            throw error
        } catch (error: Exception) {
            throw SnpAttestationException("measurement list payload is malformed")
        }
    }
}
