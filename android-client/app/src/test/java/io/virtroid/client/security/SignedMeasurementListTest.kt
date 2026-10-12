package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
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
