package io.virtroid.client.security

import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.math.BigInteger
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.security.GeneralSecurityException
import java.security.KeyFactory
import java.security.MessageDigest
import java.security.NoSuchAlgorithmException
import java.security.PublicKey
import java.security.Signature
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import java.security.spec.MGF1ParameterSpec
import java.security.spec.PSSParameterSpec
import java.security.spec.X509EncodedKeySpec
import java.util.Base64
import java.util.Date

class SnpAttestationException(message: String) : GeneralSecurityException(message)

/** A runtime node's SEV-SNP attestation bundle, as the node built it. */
class NodeAttestation(
    val product: String,
    val report: ByteArray,
    val tlsSpki: ByteArray,
    val vcek: ByteArray,
    val ask: ByteArray,
    val ark: ByteArray,
) {
    companion object {
        fun parse(json: String): NodeAttestation {
            val bundle = try {
                JSONObject(json)
            } catch (error: Exception) {
                throw SnpAttestationException("node attestation is not valid JSON")
            }
            if (bundle.optInt("version") != 1) {
                throw SnpAttestationException("unsupported node attestation version")
            }
            fun field(name: String): ByteArray = try {
                Base64.getDecoder().decode(bundle.getString(name)).also {
                    if (it.isEmpty()) throw SnpAttestationException("node attestation $name is empty")
                }
            } catch (error: SnpAttestationException) {
                throw error
            } catch (error: Exception) {
                throw SnpAttestationException("node attestation $name is missing or not base64")
            }
            return NodeAttestation(
                product = bundle.optString("product"),
                report = field("report"),
                tlsSpki = field("tls_spki"),
                vcek = field("vcek"),
                ask = field("ask"),
                ark = field("ark"),
            )
        }
    }
}

/**
 * Checks that a runtime node runs inside a genuine AMD SEV-SNP VM started from
 * an approved image, and that the keys the phone is about to trust live in it:
 * the node's TLS key (bound into the report) and the viewer key (signed by the
 * TLS key). Everything is verified on the phone against AMD's root keys built
 * into the app, so the control plane that relays the bundle is not trusted.
 */
object SnpAttestationVerifier {
    const val REPORT_SIZE = 1184
    private const val SIGNED_LENGTH = 0x2a0
    private const val POLICY_OFFSET = 0x08
    private const val VMPL_OFFSET = 0x30
    private const val SIGNATURE_ALGO_OFFSET = 0x34
    private const val REPORT_DATA_OFFSET = 0x50
    private const val MEASUREMENT_OFFSET = 0x90
    private const val MEASUREMENT_SIZE = 48
    private const val SIGNATURE_OFFSET = 0x2a0
    private const val SIGNATURE_COMPONENT_SIZE = 72
    private const val SIGNATURE_ALGO_ECDSA_P384_SHA384 = 1
    private const val POLICY_MIGRATE_MA_BIT = 1L shl 18
    private const val POLICY_DEBUG_BIT = 1L shl 19
    private const val MAX_VMPL = 1

    private val NODE_TLS_KEY_DOMAIN = "virtroid-node-tls-key-v1\u0000".toByteArray(Charsets.US_ASCII)
    private const val VIEWER_KEY_DOMAIN = "virtroid-viewer-key-v1\n"

    /**
     * SHA-256 of AMD's root key certificate (ARK) for each EPYC generation, as
     * published by AMD's Key Distribution Service and built into the `sev` crate.
     */
    private val ARK_SHA256 = mapOf(
        "Milan" to "69d063b45344d26a2e94e1f4210de49ef555308287d4c174445c95639a540bcd",
        "Genoa" to "4c6598d19c18719c5dfd4a7d335f674e5bfe1d8f800cea2cf270c10d103db2f1",
        "Turin" to "1f084161a44bb6d93778a904877d4819cafa5d05ef4193b2ded9dd9c73dd3f6a",
    )

    /**
     * Verifies [attestation] completely. [expectedTlsPin] is the base64 SHA-256
     * of the node's TLS key the session pins; [allowedMeasurements] are the
     * lowercase hex launch measurements of approved runtime images.
     */
    fun verify(
        attestation: NodeAttestation,
        expectedTlsPin: String,
        runtimeId: String,
        viewerPublicKey: String,
        viewerKeySignature: String,
        allowedMeasurements: Set<String>,
        now: Date = Date(),
    ) {
        if (expectedTlsPin.isNotBlank() && sha256Base64(attestation.tlsSpki) != expectedTlsPin.trim()) {
            throw SnpAttestationException("attestation is for a different node key than the session pins")
        }
        verifyAmdSignature(attestation, now)
        checkReport(attestation.report, attestation.tlsSpki, allowedMeasurements)
        verifyViewerKey(attestation.tlsSpki, runtimeId, viewerPublicKey, viewerKeySignature)
    }

    /** The report is signed by the chip's VCEK, which chains to AMD's root. */
    fun verifyAmdSignature(attestation: NodeAttestation, now: Date = Date()) {
        val expectedArk = ARK_SHA256[attestation.product]
            ?: throw SnpAttestationException("unknown AMD product ${attestation.product}")
        if (sha256Hex(attestation.ark) != expectedArk) {
            throw SnpAttestationException("AMD root certificate is not the built-in ${attestation.product} root")
        }
        if (attestation.report.size < REPORT_SIZE) {
            throw SnpAttestationException("attestation report is truncated")
        }
        val ark = certificate(attestation.ark, "ARK")
        val ask = certificate(attestation.ask, "ASK")
        val vcek = certificate(attestation.vcek, "VCEK")
        for ((name, certificate) in listOf("ARK" to ark, "ASK" to ask, "VCEK" to vcek)) {
            try {
                certificate.checkValidity(now)
            } catch (error: Exception) {
                throw SnpAttestationException("AMD $name certificate is not valid now")
            }
        }
        verifyRsaPss(ark, ark.publicKey, "ARK")
        verifyRsaPss(ask, ark.publicKey, "ASK")
        verifyRsaPss(vcek, ask.publicKey, "VCEK")

        val report = attestation.report
        if (u32(report, SIGNATURE_ALGO_OFFSET) != SIGNATURE_ALGO_ECDSA_P384_SHA384) {
            throw SnpAttestationException("unsupported report signature algorithm")
        }
        val r = littleEndianInteger(report, SIGNATURE_OFFSET)
        val s = littleEndianInteger(report, SIGNATURE_OFFSET + SIGNATURE_COMPONENT_SIZE)
        val verifier = Signature.getInstance("SHA384withECDSA")
        verifier.initVerify(vcek.publicKey)
        verifier.update(report, 0, SIGNED_LENGTH)
        if (!verifier.verify(derSignature(r, s))) {
            throw SnpAttestationException("attestation report signature does not verify")
        }
    }

    /** The report's own claims: the bound key, the image, and a safe policy. */
    fun checkReport(report: ByteArray, tlsSpki: ByteArray, allowedMeasurements: Set<String>) {
        if (report.size < REPORT_SIZE) {
            throw SnpAttestationException("attestation report is truncated")
        }
        if (u32(report, 0) < 2) {
            throw SnpAttestationException("attestation report version is too old")
        }
        val policy = ByteBuffer.wrap(report, POLICY_OFFSET, 8).order(ByteOrder.LITTLE_ENDIAN).long
        if (policy and POLICY_DEBUG_BIT != 0L) {
            throw SnpAttestationException("the VM allows host debugging")
        }
        if (policy and POLICY_MIGRATE_MA_BIT != 0L) {
            throw SnpAttestationException("the VM allows a migration agent")
        }
        if (u32(report, VMPL_OFFSET) > MAX_VMPL) {
            throw SnpAttestationException("attestation report comes from an untrusted privilege level")
        }
        val expectedReportData = MessageDigest.getInstance("SHA-512").run {
            update(NODE_TLS_KEY_DOMAIN)
            update(tlsSpki)
            digest()
        }
        if (!MessageDigest.isEqual(expectedReportData, report.copyOfRange(REPORT_DATA_OFFSET, REPORT_DATA_OFFSET + 64))) {
            throw SnpAttestationException("attestation report does not bind the node's TLS key")
        }
        val measurement = report.copyOfRange(MEASUREMENT_OFFSET, MEASUREMENT_OFFSET + MEASUREMENT_SIZE).toHex()
        if (measurement !in allowedMeasurements) {
            throw SnpAttestationException("the VM runs an image this app does not approve ($measurement)")
        }
    }

    /** The viewer key was signed by the attested TLS key for this runtime. */
    fun verifyViewerKey(tlsSpki: ByteArray, runtimeId: String, viewerPublicKey: String, signature: String) {
        val signatureBytes = try {
            Base64.getDecoder().decode(signature.trim())
        } catch (error: IllegalArgumentException) {
            throw SnpAttestationException("viewer key signature is not base64")
        }
        if (signatureBytes.isEmpty()) {
            throw SnpAttestationException("viewer key is not signed by the attested node")
        }
        val tlsKey: PublicKey = try {
            KeyFactory.getInstance("EC").generatePublic(X509EncodedKeySpec(tlsSpki))
        } catch (error: Exception) {
            throw SnpAttestationException("attested node key is not an EC public key")
        }
        val verifier = Signature.getInstance("SHA256withECDSA")
        verifier.initVerify(tlsKey)
        verifier.update("$VIEWER_KEY_DOMAIN$runtimeId\n$viewerPublicKey".toByteArray(Charsets.UTF_8))
        val valid = try {
            verifier.verify(signatureBytes)
        } catch (error: Exception) {
            false
        }
        if (!valid) {
            throw SnpAttestationException("viewer key is not signed by the attested node")
        }
    }

    private fun certificate(der: ByteArray, name: String): X509Certificate = try {
        CertificateFactory.getInstance("X.509").generateCertificate(ByteArrayInputStream(der)) as X509Certificate
    } catch (error: Exception) {
        throw SnpAttestationException("AMD $name certificate is malformed")
    }

    /** AMD signs its chain with RSASSA-PSS, SHA-384, MGF1-SHA-384, 48-byte salt. */
    private fun verifyRsaPss(certificate: X509Certificate, issuerKey: PublicKey, name: String) {
        val verifier = try {
            Signature.getInstance("SHA384withRSA/PSS")
        } catch (error: NoSuchAlgorithmException) {
            Signature.getInstance("RSASSA-PSS").apply {
                setParameter(PSSParameterSpec("SHA-384", "MGF1", MGF1ParameterSpec.SHA384, 48, 1))
            }
        }
        verifier.initVerify(issuerKey)
        verifier.update(certificate.tbsCertificate)
        if (!verifier.verify(certificate.signature)) {
            throw SnpAttestationException("AMD $name certificate signature does not verify")
        }
    }

    private fun littleEndianInteger(bytes: ByteArray, offset: Int): BigInteger =
        BigInteger(1, bytes.copyOfRange(offset, offset + SIGNATURE_COMPONENT_SIZE).reversedArray())

    /** ASN.1 DER SEQUENCE { INTEGER r, INTEGER s }. */
    private fun derSignature(r: BigInteger, s: BigInteger): ByteArray {
        val rBytes = r.toByteArray()
        val sBytes = s.toByteArray()
        val body = byteArrayOf(0x02, rBytes.size.toByte()) + rBytes + byteArrayOf(0x02, sBytes.size.toByte()) + sBytes
        return byteArrayOf(0x30, body.size.toByte()) + body
    }

    private fun u32(bytes: ByteArray, offset: Int): Int =
        ByteBuffer.wrap(bytes, offset, 4).order(ByteOrder.LITTLE_ENDIAN).int

    private fun sha256Hex(bytes: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(bytes).toHex()

    private fun sha256Base64(bytes: ByteArray): String =
        Base64.getEncoder().encodeToString(MessageDigest.getInstance("SHA-256").digest(bytes))

    private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it.toInt() and 0xff) }
}
