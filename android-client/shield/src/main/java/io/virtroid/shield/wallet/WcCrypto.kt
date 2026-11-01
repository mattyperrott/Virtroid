// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.wallet

import org.bouncycastle.crypto.digests.SHA256Digest
import org.bouncycastle.crypto.generators.HKDFBytesGenerator
import org.bouncycastle.crypto.modes.ChaCha20Poly1305
import org.bouncycastle.crypto.params.AEADParameters
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.params.HKDFParameters
import org.bouncycastle.crypto.params.KeyParameter
import org.bouncycastle.crypto.signers.Ed25519Signer
import org.bouncycastle.math.ec.rfc7748.X25519
import java.math.BigInteger
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64

/**
 * WalletConnect v2's cryptography, from its specs (github.com/WalletConnect/
 * walletconnect-specs, docs/specs/clients/core/crypto): X25519 key agreement
 * hashed with HKDF-SHA256 into a ChaCha20-Poly1305 key, typed envelopes,
 * topics as the SHA-256 of a key, and the Ed25519 did:key JWT that
 * authenticates the client to the relay.
 */
object WcCrypto {
    private val random = SecureRandom()

    fun randomBytes(n: Int) = ByteArray(n).also(random::nextBytes)

    class X25519KeyPair(val privateKey: ByteArray) {
        val publicKey: ByteArray = ByteArray(X25519.POINT_SIZE).also { X25519.scalarMultBase(privateKey, 0, it, 0) }
    }

    fun x25519KeyPair() = X25519KeyPair(randomBytes(X25519.SCALAR_SIZE))

    /** The session's symmetric key: HKDF-SHA256 over the X25519 secret, no salt, no info. */
    fun deriveSymKey(privateKey: ByteArray, peerPublicKey: ByteArray): ByteArray {
        val shared = ByteArray(X25519.POINT_SIZE)
        check(X25519.calculateAgreement(privateKey, 0, peerPublicKey, 0, shared, 0)) { "The wallet's key is invalid" }
        return hkdf(shared)
    }

    fun hkdf(ikm: ByteArray): ByteArray {
        val generator = HKDFBytesGenerator(SHA256Digest())
        generator.init(HKDFParameters(ikm, null, null))
        return ByteArray(32).also { generator.generateBytes(it, 0, it.size) }
    }

    fun topicOf(symKey: ByteArray): String = hex(MessageDigest.getInstance("SHA-256").digest(symKey))

    /** A type 0 envelope (both sides hold the key): 0x00 || iv || ciphertext+tag, base64. */
    fun seal(symKey: ByteArray, plaintext: String, iv: ByteArray = randomBytes(12)): String {
        val cipher = ChaCha20Poly1305().apply { init(true, AEADParameters(KeyParameter(symKey), 128, iv)) }
        val input = plaintext.toByteArray()
        val out = ByteArray(cipher.getOutputSize(input.size))
        val n = cipher.processBytes(input, 0, input.size, out, 0)
        cipher.doFinal(out, n)
        return Base64.getEncoder().encodeToString(byteArrayOf(0) + iv + out)
    }

    /** Opens a type 0 envelope, or a type 1 (sender's public key inside) with [selfPrivateKey]. */
    fun open(symKey: ByteArray?, message: String, selfPrivateKey: ByteArray? = null): String {
        val bytes = Base64.getDecoder().decode(message)
        val (key, offset) = when (bytes[0].toInt()) {
            0 -> requireNotNull(symKey) { "No key for this topic" } to 1
            1 -> {
                val peer = bytes.copyOfRange(1, 33)
                deriveSymKey(requireNotNull(selfPrivateKey) { "No private key for a type 1 envelope" }, peer) to 33
            }
            else -> throw IllegalArgumentException("Unknown envelope type ${bytes[0]}")
        }
        val iv = bytes.copyOfRange(offset, offset + 12)
        val sealed = bytes.copyOfRange(offset + 12, bytes.size)
        val cipher = ChaCha20Poly1305().apply { init(false, AEADParameters(KeyParameter(key), 128, iv)) }
        val out = ByteArray(cipher.getOutputSize(sealed.size))
        val n = cipher.processBytes(sealed, 0, sealed.size, out, 0)
        val total = n + cipher.doFinal(out, n)
        return String(out, 0, total)
    }

    // --- relay authentication -------------------------------------------------

    class Ed25519Identity(seed: ByteArray) {
        private val key = Ed25519PrivateKeyParameters(seed, 0)
        val seed: ByteArray = key.encoded
        val publicKey: ByteArray = key.generatePublicKey().encoded

        fun sign(data: ByteArray): ByteArray = Ed25519Signer().run {
            init(true, key)
            update(data, 0, data.size)
            generateSignature()
        }
    }

    /** did:key for an Ed25519 public key: multicodec 0xed01, base58btc, "z" prefix. */
    fun didKey(publicKey: ByteArray): String = "did:key:z" + base58(byteArrayOf(0xed.toByte(), 0x01) + publicKey)

    /** The relay's client_auth did-jwt, signed by the client's persisted identity. */
    fun relayJwt(identity: Ed25519Identity, audience: String, nowSeconds: Long, ttlSeconds: Long = 86_400): String {
        val url = Base64.getUrlEncoder().withoutPadding()
        val header = url.encodeToString("""{"alg":"EdDSA","typ":"JWT"}""".toByteArray())
        val payload = org.json.JSONObject()
            .put("iss", didKey(identity.publicKey))
            .put("sub", hex(randomBytes(32)))
            .put("aud", audience)
            .put("iat", nowSeconds)
            .put("exp", nowSeconds + ttlSeconds)
            .put("act", "client_auth")
            .toString()
        val body = url.encodeToString(payload.toByteArray())
        val signature = url.encodeToString(identity.sign("$header.$body".toByteArray()))
        return "$header.$body.$signature"
    }

    private const val ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

    fun base58(bytes: ByteArray): String {
        var n = BigInteger(1, bytes)
        val out = StringBuilder()
        val base = BigInteger.valueOf(58)
        while (n > BigInteger.ZERO) {
            val (q, r) = n.divideAndRemainder(base)
            out.append(ALPHABET[r.toInt()])
            n = q
        }
        for (b in bytes) if (b.toInt() == 0) out.append('1') else break
        return out.reverse().toString()
    }

    fun hex(bytes: ByteArray) = bytes.joinToString("") { "%02x".format(it) }

    fun unhex(text: String): ByteArray {
        val s = text.removePrefix("0x")
        require(s.length % 2 == 0) { "Odd-length hex" }
        return ByteArray(s.length / 2) { s.substring(it * 2, it * 2 + 2).toInt(16).toByte() }
    }
}
