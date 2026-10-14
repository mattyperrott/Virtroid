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

    private fun sealReport(sealKey: ByteArray, nonce: ByteArray): ByteArray {
        val report = report()
        MessageDigest.getInstance("SHA-512").run {
            update("virtroid-blob-seal-key-v2".toByteArray())
            update(0)
            update(sealKey)
            update(nonce)
            digest()
        }.copyInto(report, 0x50)
        return report
    }

    @Test
    fun sealReportsMustBindTheKeyToThisChallenge() {
        val allowed = setOf(measurementHex)
        val sealKey = "seal key".toByteArray()
        val nonce = ByteArray(32) { 7 }
        SnpAttestationVerifier.checkSealReport(sealReport(sealKey, nonce), sealKey, nonce, allowed)
        val failures = mapOf(
            "a replayed challenge" to { SnpAttestationVerifier.checkSealReport(sealReport(sealKey, ByteArray(32) { 8 }), sealKey, nonce, allowed) },
            "another seal key" to { SnpAttestationVerifier.checkSealReport(sealReport("other".toByteArray(), nonce), sealKey, nonce, allowed) },
            "a TLS-key report" to { SnpAttestationVerifier.checkSealReport(report(), sealKey, nonce, allowed) },
            "a short challenge" to { SnpAttestationVerifier.checkSealReport(sealReport(sealKey, nonce), sealKey, ByteArray(16), allowed) },
            "an unapproved image" to { SnpAttestationVerifier.checkSealReport(sealReport(sealKey, nonce), sealKey, nonce, emptySet()) },
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
    fun milanChipsAreNotTrusted() {
        assertThrows(SnpAttestationException::class.java) { SnpAttestationVerifier.checkPlatform("Milan") }
        SnpAttestationVerifier.checkPlatform("Genoa")
        SnpAttestationVerifier.checkPlatform("Turin")
    }

    @Test
    fun runtimeKeysMatchTheGoNode() {
        // virtnoded's derivations from an account key of 32 bytes of 4, for runtime-1.
        val keys = BlobKeyEnvelopeCrypto.runtimeKeys(ByteArray(32) { 4 }, "runtime-1")
        val hex = keys.joinToString("") { "%02x".format(it) }
        if (hex != "f02425137d04c6e3f692f2f95b35b667926396c2cebbac1be808cb82e99a8c05bffd4ec90e1fe5ca8bee5e1b9db23560dc49fea26fe532db7480b512f8623bb9b85cdaa671aeccd3630112d93a2f81417a37cf29bbd19e8ce10efbee2a3d5535") {
            throw AssertionError("runtime keys differ from the node's: $hex")
        }
        val verifier = Base64.getUrlEncoder().withoutPadding().encodeToString(
            MessageDigest.getInstance("SHA-256").digest("virtroid-runtime-keys-verifier-v1:".toByteArray() + keys),
        )
        if (verifier != "u6fge0EsM4_fYlmSpxoTTmPB46wJrr8KDvNEuV_XE04") {
            throw AssertionError("runtime keys verifier differs from the node's: $verifier")
        }
        if (BlobKeyEnvelopeCrypto.runtimeKeys(ByteArray(32) { 4 }, "runtime-2").contentEquals(keys)) {
            throw AssertionError("two runtimes share keys")
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
