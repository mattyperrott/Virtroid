package io.virtroid.client.security

import android.content.Context
import io.virtroid.client.BuildConfig
import io.virtroid.client.R
import io.virtroid.client.api.SessionAttestation
import io.virtroid.client.data.AppLogStore
import java.io.IOException
import java.security.PublicKey

class ConfidentialRuntimeException(message: String, cause: Throwable? = null) : IOException(message, cause)

/**
 * Decides whether a session must be attested and verifies it. A runtime must
 * be attested when the server says it runs in a provisioned confidential VM,
 * when its node offers an attestation, or when it was attested before on this
 * phone, so a server that stops sending the attestation cannot downgrade it.
 *
 * Approved images, and the oldest host firmware each AMD product may run, come
 * from the newest signed list this phone has verified that has not expired, or
 * from the signed list bundled with the app. An expired list is fetched again,
 * and nothing is verified against one: a server that keeps serving an old list
 * cannot keep a since-revoked image approved for long.
 */
class ConfidentialRuntimeGuard(
    private val context: Context,
    private val signingKeys: List<PublicKey> = loadSigningKeys(context),
    private val bundledDocument: String? = loadBundledList(context),
) {
    private val attested = context.getSharedPreferences(ATTESTED_PREFS, Context.MODE_PRIVATE)
    private val lists = context.getSharedPreferences(LIST_PREFS, Context.MODE_PRIVATE)

    suspend fun check(
        runtimeId: String,
        relayTlsPin: String,
        viewerPublicKey: String,
        attestation: SessionAttestation,
        fetchMeasurementList: suspend () -> String?,
    ) {
        val required = BuildConfig.REQUIRE_ATTESTATION ||
            attestation.confidentialRuntime ||
            attestation.nodeAttestationJson.isNotBlank() ||
            attested.getBoolean(runtimeId, false)
        if (!required) {
            return
        }
        var verified: Pair<String, String>? = null
        val verify = {
            if (attestation.nodeAttestationJson.isBlank()) {
                throw SnpAttestationException("the server sent no attestation for a confidential runtime")
            }
            val node = NodeAttestation.parse(attestation.nodeAttestationJson)
            SnpAttestationVerifier.checkPlatform(node.product)
            val list = currentListOrThrow()
            val measurement = SnpAttestationVerifier.verify(
                attestation = node,
                expectedTlsPin = relayTlsPin,
                runtimeId = runtimeId,
                viewerPublicKey = viewerPublicKey,
                viewerKeySignature = attestation.viewerKeySignature,
                allowedMeasurements = list.measurements,
            )
            SnpAttestationVerifier.checkFirmware(node, list.minimumFirmware[node.product])
            verified = node.product to measurement
        }
        verifyRefreshingList(verify, fetchMeasurementList)
        attested.edit().putBoolean(runtimeId, true).apply()
        verified?.let { (product, measurement) ->
            AppLogStore.get(context).security(
                "Confidential server verified: AMD SEV-SNP report from a $product chip, signed by AMD; " +
                    "launch measurement ${measurement.take(16)}… is approved; the stream is bound to the attested VM's key.",
                "attestation",
            )
        }
    }

    /**
     * Decides where [lease] seals the runtime's keys. A runtime that must be
     * attested gets them only sealed to the VM's in-memory seal key, after a
     * fresh report verifies: signed by AMD, from an approved image on a chip
     * whose attestation can't be forged, and binding that key to the
     * challenge this phone sent. Returns the seal key, or null only for a
     * runtime that needs no attestation (a development build's local node),
     * whose node key gets the account key as before.
     */
    suspend fun checkBlobSealKey(lease: BlobKeyLease, fetchMeasurementList: suspend () -> String?): String? {
        val required = BuildConfig.REQUIRE_ATTESTATION ||
            lease.confidentialRuntime ||
            lease.sealAttestationJson.isNotBlank() ||
            attested.getBoolean(lease.runtimeId, false)
        if (!required) {
            return null
        }
        var sealKey: ByteArray? = null
        val verify = {
            if (lease.sealAttestationJson.isBlank()) {
                throw SnpAttestationException("the VM offered no fresh report for a key only it holds")
            }
            val sealed = SealAttestation.parse(lease.sealAttestationJson)
            val nonce = try {
                java.util.Base64.getDecoder().decode(lease.sealNonce)
            } catch (error: IllegalArgumentException) {
                throw SnpAttestationException("seal challenge is not base64")
            }
            SnpAttestationVerifier.checkPlatform(sealed.chip.product)
            val list = currentListOrThrow()
            SnpAttestationVerifier.verifyAmdSignature(sealed.chip)
            SnpAttestationVerifier.checkFirmware(sealed.chip, list.minimumFirmware[sealed.chip.product])
            SnpAttestationVerifier.checkSealReport(sealed.chip.report, sealed.sealPublicKey, nonce, list.measurements)
            sealKey = sealed.sealPublicKey
        }
        verifyRefreshingList(verify, fetchMeasurementList)
        attested.edit().putBoolean(lease.runtimeId, true).apply()
        return java.util.Base64.getEncoder().encodeToString(sealKey ?: throw ConfidentialRuntimeException("seal key missing"))
    }

    private suspend fun verifyRefreshingList(verify: () -> Unit, fetchMeasurementList: suspend () -> String?) {
        refreshListIfStale(fetchMeasurementList)
        try {
            try {
                verify()
            } catch (unapproved: UnapprovedMeasurementException) {
                // The VM may run an image newer than this phone's list.
                val document = try {
                    fetchMeasurementList()
                } catch (error: IOException) {
                    null
                }
                if (document == null || !accept(document)) {
                    throw unapproved
                }
                verify()
            }
        } catch (error: SnpAttestationException) {
            throw ConfidentialRuntimeException(
                context.getString(R.string.confidential_runtime_unverified, error.message.orEmpty()),
                error,
            )
        }
    }

    /**
     * Fetches the signed list now and then even when every image checks out:
     * otherwise a revoked image would stay approved on this phone until it
     * met one it didn't know. With no current list it fetches every time. A
     * failed fetch keeps the list it has, until that list expires.
     */
    private suspend fun refreshListIfStale(fetchMeasurementList: suspend () -> String?) {
        val now = System.currentTimeMillis()
        if (currentList(now) != null && now - lists.getLong(KEY_LIST_CHECKED_AT, 0) < LIST_REFRESH_MS) {
            return
        }
        val document = try {
            fetchMeasurementList()
        } catch (error: IOException) {
            null
        } ?: return
        accept(document)
        lists.edit().putLong(KEY_LIST_CHECKED_AT, now).apply()
    }

    fun approvedMeasurements(): Set<String> = currentList()?.measurements.orEmpty()

    private fun currentListOrThrow(): MeasurementList = currentList()
        ?: throw SnpAttestationException("the list of approved server images has expired and no newer one could be fetched")

    /** The newest verified list that has not expired, or null. */
    private fun currentList(now: Long = System.currentTimeMillis()): MeasurementList? =
        listOfNotNull(storedList(), bundledList())
            .filter { it.isCurrent(now) && it.serial >= minimumSerial() }
            .maxByOrNull { it.serial }

    /** Stores a verified list newer than any seen before; returns whether it did. */
    fun accept(document: String): Boolean {
        val list = try {
            SignedMeasurementList.verify(document, signingKeys)
        } catch (error: SnpAttestationException) {
            return false
        }
        // Lists without an expiry predate it and are never used again; and a
        // list is kept only when newer than the bundled one and any seen.
        if (list.expiresAtMillis == null || list.serial <= minimumSerial()) {
            return false
        }
        lists.edit()
            .putString(KEY_DOCUMENT, document)
            .putLong(KEY_HIGHEST_SERIAL, list.serial)
            .apply()
        return true
    }

    /** No list older than the bundled one, or than one seen before, is used. */
    private fun minimumSerial(): Long =
        maxOf(lists.getLong(KEY_HIGHEST_SERIAL, 0), bundledList()?.serial ?: 0)

    private fun storedList(): MeasurementList? = verifiedOrNull(lists.getString(KEY_DOCUMENT, null))

    private fun bundledList(): MeasurementList? = verifiedOrNull(bundledDocument)

    private fun verifiedOrNull(document: String?): MeasurementList? {
        document ?: return null
        return try {
            SignedMeasurementList.verify(document, signingKeys)
        } catch (error: SnpAttestationException) {
            null
        }
    }

    companion object {
        private const val ATTESTED_PREFS = "virtroid_attested_runtimes"
        private const val LIST_PREFS = "virtroid_measurement_list"
        private const val KEY_DOCUMENT = "document"
        private const val KEY_HIGHEST_SERIAL = "highest_serial"
        private const val KEY_LIST_CHECKED_AT = "list_checked_at"
        private const val LIST_REFRESH_MS = 6 * 60 * 60 * 1_000L

        /** The signed list built into this release of the app. */
        fun loadBundledList(context: Context): String? =
            context.resources.openRawResource(R.raw.snp_measurement_list).bufferedReader().use { it.readText() }
                .trim().takeIf { it.isNotEmpty() }

        fun loadSigningKeys(context: Context): List<PublicKey> =
            context.resources.openRawResource(R.raw.measurement_signing_keys).bufferedReader().useLines { lines ->
                SignedMeasurementList.parseKeys(lines.toList())
            }

    }
}
