package io.virtroid.client.security

import android.content.Context
import io.virtroid.client.R
import io.virtroid.client.api.SessionAttestation
import java.io.IOException

class ConfidentialRuntimeException(message: String, cause: Throwable? = null) : IOException(message, cause)

/**
 * Decides whether a session must be attested and verifies it. A runtime must
 * be attested when the server says it runs in a provisioned confidential VM,
 * when its node offers an attestation, or when it was attested before on this
 * phone, so a server that stops sending the attestation cannot downgrade it.
 */
class ConfidentialRuntimeGuard(
    private val context: Context,
    private val allowedMeasurements: Set<String> = loadMeasurements(context),
) {
    private val attested = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    fun check(runtimeId: String, relayTlsPin: String, viewerPublicKey: String, attestation: SessionAttestation) {
        val required = attestation.confidentialRuntime ||
            attestation.nodeAttestationJson.isNotBlank() ||
            attested.getBoolean(runtimeId, false)
        if (!required) {
            return
        }
        try {
            if (attestation.nodeAttestationJson.isBlank()) {
                throw SnpAttestationException("the server sent no attestation for a confidential runtime")
            }
            SnpAttestationVerifier.verify(
                attestation = NodeAttestation.parse(attestation.nodeAttestationJson),
                expectedTlsPin = relayTlsPin,
                runtimeId = runtimeId,
                viewerPublicKey = viewerPublicKey,
                viewerKeySignature = attestation.viewerKeySignature,
                allowedMeasurements = allowedMeasurements,
            )
        } catch (error: SnpAttestationException) {
            throw ConfidentialRuntimeException(
                context.getString(R.string.confidential_runtime_unverified, error.message.orEmpty()),
                error,
            )
        }
        attested.edit().putBoolean(runtimeId, true).apply()
    }

    companion object {
        private const val PREFS = "virtroid_attested_runtimes"

        fun loadMeasurements(context: Context): Set<String> =
            context.resources.openRawResource(R.raw.snp_launch_measurements).bufferedReader().useLines { lines ->
                parseMeasurements(lines.toList())
            }

        fun parseMeasurements(lines: List<String>): Set<String> = lines
            .map { it.substringBefore('#').trim().lowercase() }
            .filter { it.length == 96 && it.all { char -> char in '0'..'9' || char in 'a'..'f' } }
            .toSet()
    }
}
