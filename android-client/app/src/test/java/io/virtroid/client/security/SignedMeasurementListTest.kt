package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.security.KeyPairGenerator
import java.security.Signature
import java.util.Base64

class SignedMeasurementListTest {
    // Signed by `virtroid-measurements sign --serial 5` (Go).
    private val goPublicKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEjTeJdlkDRItcAy/hF/j39or032n/dUPvyd3svhf6nD47x9ScjBEn+orXaXh0NK7+SY6E0QrpgmJwOen4uJRj9A=="
    private val goDocument = "{\"payload\":\"eyJ2ZXJzaW9uIjoxLCJzZXJpYWwiOjUsImlzc3VlZF9hdCI6IjIwMjYtMTAtMTJUMDA6NDQ6MjJaIiwibWVhc3VyZW1lbnRzIjpbIjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZjBmMGYwZiIsImFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYiJdfQ==\",\"signature\":\"MEUCIQCpJ3J0QQW7g5BKfxbbtV9Qy3ojOrZBOqgOYbhdrNgTtQIgCKe7NLsobnVN188OvkACVvxH7UpUMRE/PHVsRVLAwF4=\"}"

    private val ab = "ab".repeat(48)
    private val of = "0f".repeat(48)

    @Test
    fun listSignedByTheGoToolVerifies() {
        val keys = SignedMeasurementList.parseKeys(listOf("# release key", goPublicKey, "not a key"))
        assertEquals(1, keys.size)
        val list = SignedMeasurementList.verify(goDocument, keys)
        assertEquals(5L, list.serial)
        assertEquals(setOf(ab, of), list.measurements)
    }

    // Signed by the Go tool with an expiry and minimum firmware (serial 6).
    private val goCurrentKey = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEmEq4h2HWPpnTOvxHtcVp3L+wMKEZmbMDnJ3sk/CGJMnDJMDZebApRptlkJmKtd2JqW6dKmdTlUW5Wp88PbpMQA=="
    private val goCurrentDocument = "{\"payload\":\"eyJ2ZXJzaW9uIjoxLCJzZXJpYWwiOjYsImlzc3VlZF9hdCI6IjIwMjYtMTAtMTlUMDA6MDA6MDBaIiwiZXhwaXJlc19hdCI6IjIwMjYtMTItMThUMDA6MDA6MDBaIiwibWluX3RjYiI6eyJHZW5vYSI6eyJib290bG9hZGVyIjoxMiwidGVlIjowLCJzbnAiOjI4LCJtaWNyb2NvZGUiOjg4fSwiVHVyaW4iOnsiYm9vdGxvYWRlciI6MSwidGVlIjoyLCJzbnAiOjMsIm1pY3JvY29kZSI6NCwiZm1jIjo1fX0sIm1lYXN1cmVtZW50cyI6WyJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWJhYmFiYWIiXX0=\",\"signature\":\"MEQCIAmd3Vqc37cSK9DZu8yPnrs6SUiQPMrieP/PGtKGoRSiAiAcJfEroYC+TeY7TE3yUwDdSKBFQqmk3i5sIIy/x82eiw==\"}"

    @Test
    fun listsCarryExpiryAndMinimumFirmware() {
        val list = SignedMeasurementList.verify(goCurrentDocument, SignedMeasurementList.parseKeys(listOf(goCurrentKey)))
        assertEquals(6L, list.serial)
        assertEquals(java.time.Instant.parse("2026-12-18T00:00:00Z").toEpochMilli(), list.expiresAtMillis)
        assertEquals(SnpTcb(bootloader = 12, tee = 0, snp = 28, microcode = 88), list.minimumFirmware["Genoa"])
        assertEquals(SnpTcb(bootloader = 1, tee = 2, snp = 3, microcode = 4, fmc = 5), list.minimumFirmware["Turin"])
        assertTrue(list.isCurrent(java.time.Instant.parse("2026-12-17T23:59:59Z").toEpochMilli()))
        assertFalse(list.isCurrent(java.time.Instant.parse("2026-12-18T00:00:00Z").toEpochMilli()))
        // A list from before expiries existed is never current.
        val old = SignedMeasurementList.verify(goDocument, SignedMeasurementList.parseKeys(listOf(goPublicKey)))
        assertFalse(old.isCurrent(0L))
    }

    @Test
    fun listFromAnotherKeyIsRejected() {
        val other = KeyPairGenerator.getInstance("EC").apply { initialize(256) }.generateKeyPair()
        assertThrows(SnpAttestationException::class.java) {
            SignedMeasurementList.verify(goDocument, listOf(other.public))
        }
        assertThrows(SnpAttestationException::class.java) {
            SignedMeasurementList.verify(goDocument, emptyList())
        }
    }

    @Test
    fun alteredListIsRejected() {
        val keys = SignedMeasurementList.parseKeys(listOf(goPublicKey))
        val payload = String(Base64.getDecoder().decode(org.json.JSONObject(goDocument).getString("payload")))
        val altered = org.json.JSONObject(goDocument)
            .put("payload", Base64.getEncoder().encodeToString(payload.replace("\"serial\":5", "\"serial\":6").toByteArray()))
            .toString()
        assertThrows(SnpAttestationException::class.java) { SignedMeasurementList.verify(altered, keys) }
        assertThrows(SnpAttestationException::class.java) { SignedMeasurementList.verify("not json", keys) }
    }

    @Test
    fun signatureMustCoverTheDomainTag() {
        // A signature over the bare payload (without the domain tag) must not verify.
        val pair = KeyPairGenerator.getInstance("EC").apply { initialize(256) }.generateKeyPair()
        val payload = """{"version":1,"serial":1,"issued_at":"x","measurements":[""]}""".toByteArray()
        val bare = Signature.getInstance("SHA256withECDSA").run {
            initSign(pair.private)
            update(payload)
            sign()
        }
        val document = org.json.JSONObject()
            .put("payload", Base64.getEncoder().encodeToString(payload))
            .put("signature", Base64.getEncoder().encodeToString(bare))
            .toString()
        assertThrows(SnpAttestationException::class.java) {
            SignedMeasurementList.verify(document, listOf(pair.public))
        }
    }
}
