package io.virtroid.client.security

import org.junit.Assert.assertThrows
import org.junit.Test
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.Signature
import java.util.Base64
import java.util.Calendar
import java.util.Date
import java.util.TimeZone

class SnpAttestationVerifierTest {
    private fun resource(name: String): ByteArray =
        javaClass.getResourceAsStream("/snp/$name")!!.use { it.readBytes() }

    private val milanReport = String(resource("report_milan.hex")).trim().chunked(2).map { it.toInt(16).toByte() }.toByteArray()

    // Inside the validity of every certificate in the test chain.
    private val testTime: Date = Calendar.getInstance(TimeZone.getTimeZone("UTC")).apply { set(2026, 9, 1) }.time

    private fun milanAttestation(report: ByteArray = milanReport, ark: ByteArray = resource("milan-ark.der"), product: String = "Milan") =
        NodeAttestation(
            product = product,
            report = report,
            tlsSpki = byteArrayOf(1),
            vcek = resource("vcek_milan.der"),
            ask = resource("milan-ask.der"),
            ark = ark,
        )

    @Test
    fun realMilanReportChainsToAmdsRoot() {
        SnpAttestationVerifier.verifyAmdSignature(milanAttestation(), testTime)
    }

    @Test
    fun alteredReportIsRejected() {
        val tampered = milanReport.copyOf().also { it[0x90] = (it[0x90].toInt() xor 1).toByte() }
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyAmdSignature(milanAttestation(report = tampered), testTime)
        }
    }

    @Test
    fun onlyTheBuiltInAmdRootIsTrusted() {
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyAmdSignature(milanAttestation(ark = resource("genoa-ark.der")), testTime)
        }
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyAmdSignature(milanAttestation(product = "Genoa"), testTime)
        }
    }

    @Test
    fun expiredChainIsRejected() {
        val later = Calendar.getInstance(TimeZone.getTimeZone("UTC")).apply { set(2060, 0, 1) }.time
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyAmdSignature(milanAttestation(), later)
        }
    }

    private val spki = "node tls key".toByteArray()
    private val measurement = ByteArray(48) { it.toByte() }
    private val measurementHex = measurement.joinToString("") { "%02x".format(it) }

    private fun report(policy: Long = 0x30000, vmpl: Int = 0, boundKey: ByteArray = spki): ByteArray {
        val report = ByteArray(SnpAttestationVerifier.REPORT_SIZE)
        val buffer = ByteBuffer.wrap(report).order(ByteOrder.LITTLE_ENDIAN)
        buffer.putInt(0, 3)
        buffer.putLong(0x08, policy)
        buffer.putInt(0x30, vmpl)
        val reportData = MessageDigest.getInstance("SHA-512").run {
            update("virtroid-node-tls-key-v1\u0000".toByteArray())
            update(boundKey)
            digest()
        }
        reportData.copyInto(report, 0x50)
        measurement.copyInto(report, 0x90)
        return report
    }

    @Test
    fun reportClaimsAreChecked() {
        val allowed = setOf(measurementHex)
        SnpAttestationVerifier.checkReport(report(), spki, allowed)

        val failures = mapOf(
            "debug policy" to { SnpAttestationVerifier.checkReport(report(policy = 0x30000L or (1L shl 19)), spki, allowed) },
            "migration agent" to { SnpAttestationVerifier.checkReport(report(policy = 0x30000L or (1L shl 18)), spki, allowed) },
            "high VMPL" to { SnpAttestationVerifier.checkReport(report(vmpl = 2), spki, allowed) },
            "other key" to { SnpAttestationVerifier.checkReport(report(boundKey = "other".toByteArray()), spki, allowed) },
            "unapproved image" to { SnpAttestationVerifier.checkReport(report(), spki, emptySet()) },
        )
        for ((name, check) in failures) {
            try {
                check()
                throw AssertionError("$name was accepted")
            } catch (expected: SnpAttestationException) {
            }
        }
    }

    @Test
    fun viewerKeySignedByTheGoNodeVerifies() {
        // Produced by the node's signViewerKey in Go.
        val goSpki = Base64.getDecoder().decode(
            "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE8xLcf07oMD2Q53jaXE1yDr5WRLWe/oB3TpV+9IoO9t+qA+FYqlwzgMrHtWRfgs6RLtt3YKkFmb0//Ujjr+enxA==",
        )
        val goSignature = "MEQCIG/rFiMrq6CjD6AbINSlsFmFrm/uPsGtsCDIg1OXq5XdAiBtgCKqmyta+V1JTcProT3SduwDdIPi0mtIVzfDefvfIQ=="
        SnpAttestationVerifier.verifyViewerKey(goSpki, "runtime-1", "viewer-key", goSignature)
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyViewerKey(goSpki, "runtime-2", "viewer-key", goSignature)
        }
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyViewerKey(goSpki, "runtime-1", "other-viewer-key", goSignature)
        }
    }

    @Test
    fun sealKeySignedByTheGoNodeVerifies() {
        // Signed by virtnoded's seal-key construction with a fresh TLS key.
        val goSpki = Base64.getDecoder().decode("MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE5xNJGdy0TTG77H175bft8Mf8P8DVsf2SnRGXHaS3NiFq+L0L3oikVGayg+6Use6cjL1kE10r26D1ybNw3J3Hpw==")
        val goSignature = "MEUCIGBSqTKS+F1S/9ZIbAFsp1DTlByZ2KCCCWDrXJnvfHc8AiEAnWDP6w3WHuxJ5RbWooW2BiC5yn67iDgUBEXhXw/UNXw="
        SnpAttestationVerifier.verifySealKey(goSpki, "node-1", "seal-key", goSignature)
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifySealKey(goSpki, "node-2", "seal-key", goSignature)
        }
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifySealKey(goSpki, "node-1", "other-seal-key", goSignature)
        }
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifySealKey(goSpki, "node-1", "seal-key", "")
        }
    }

    @Test
    fun viewerKeyFromAnotherNodeIsRejected() {
        val keys = KeyPairGenerator.getInstance("EC").apply { initialize(256) }
        val nodeKey = keys.generateKeyPair()
        val otherKey = keys.generateKeyPair()
        val signature = Signature.getInstance("SHA256withECDSA").run {
            initSign(otherKey.private)
            update("virtroid-viewer-key-v1\nruntime-1\nviewer-key".toByteArray())
            Base64.getEncoder().encodeToString(sign())
        }
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyViewerKey(nodeKey.public.encoded, "runtime-1", "viewer-key", signature)
        }
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verifyViewerKey(nodeKey.public.encoded, "runtime-1", "viewer-key", "")
        }
    }

    @Test
    fun attestationForAnotherNodeKeyIsRejectedFirst() {
        val attestation = milanAttestation()
        assertThrows(SnpAttestationException::class.java) {
            SnpAttestationVerifier.verify(
                attestation,
                expectedTlsPin = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
                runtimeId = "runtime-1",
                viewerPublicKey = "viewer-key",
                viewerKeySignature = "",
                allowedMeasurements = emptySet(),
                now = testTime,
            )
        }
    }

    @Test
    fun bundleParsing() {
        val parsed = NodeAttestation.parse(
            """{"version":1,"product":"Genoa","report":"cmVwb3J0","tls_spki":"c3BraQ==","vcek":"dg==","ask":"YQ==","ark":"Yg=="}""",
        )
        assert(parsed.product == "Genoa" && String(parsed.tlsSpki) == "spki")
        for (bad in listOf(
            "not json",
            """{"version":2,"product":"Genoa","report":"YQ==","tls_spki":"YQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}""",
            """{"version":1,"product":"Genoa","report":"YQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}""",
            """{"version":1,"product":"Genoa","report":"!!","tls_spki":"YQ==","vcek":"YQ==","ask":"YQ==","ark":"YQ=="}""",
        )) {
            assertThrows(SnpAttestationException::class.java) { NodeAttestation.parse(bad) }
        }
    }
}
