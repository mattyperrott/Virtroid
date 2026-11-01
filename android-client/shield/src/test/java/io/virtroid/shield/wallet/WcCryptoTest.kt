// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.wallet

import org.bouncycastle.crypto.params.Ed25519PublicKeyParameters
import org.bouncycastle.crypto.signers.Ed25519Signer
import org.json.JSONObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.math.BigInteger
import java.util.Base64

/**
 * WcCrypto against WalletConnect's own vectors: the key pairs, shared and
 * symmetric keys, topic hash and envelopes in walletconnect-monorepo's
 * packages/utils/test (crypto.spec.ts, shared/values.ts), and the did-jwt
 * example in the specs' crypto-authentication.md.
 */
class WcCryptoTest {
    private val selfPrivate = WcCrypto.unhex("1fb63fca5c6ac731246f2f069d3bc2454345d5208254aa8ea7bffc6d110c8862")
    private val selfPublic = WcCrypto.unhex("ff7a7d5767c362b0a17ad92299ebdb7831dcbd9a56959c01368c7404543b3342")
    private val peerPrivate = WcCrypto.unhex("36bf507903537de91f5e573666eaa69b1fa313974f23b2b59645f20fea505854")
    private val peerPublic = WcCrypto.unhex("590c2c627be7af08597091ff80dd41f7fa28acd10ef7191d7e830e116d3a186a")
    private val symKey = "0653ca620c7b4990392e1c53c4a51c14a2840cd20f0f1524cf435b17b6fe988c"
    private val message = """{"id":1,"jsonrpc":"2.0","method":"test_method","params":{}}"""
    private val iv = WcCrypto.unhex("717765636661617364616473")
    private val type0 =
        "AHF3ZWNmYWFzZGFkc3paHoQ96/mLAdanVxi17icRXq+jyrqXA8ocVgGmryQZBFMg+uwgc8yLa43EOeY+IWEv84g8hn4L3Ncsgz6397sgNKnsNcL7A9k3Mg=="
    private val type1 =
        "Af96fVdnw2KwoXrZIpnr23gx3L2aVpWcATaMdARUOzNCcXdlY2ZhYXNkYWRzeloehD3r+YsB1qdXGLXuJxFer6PKupcDyhxWAaavJBkEUyD67CBzzItrjcQ55j4hYS/ziDyGfgvc1yyDPrf3uyA0qew1wvsD2Tcy"

    @Test
    fun publicKeysFollowFromPrivate() {
        assertArrayEquals(selfPublic, WcCrypto.X25519KeyPair(selfPrivate).publicKey)
        assertArrayEquals(peerPublic, WcCrypto.X25519KeyPair(peerPrivate).publicKey)
    }

    @Test
    fun deriveSymKeyMatchesWalletConnectBothWays() {
        assertEquals(symKey, WcCrypto.hex(WcCrypto.deriveSymKey(selfPrivate, peerPublic)))
        assertEquals(symKey, WcCrypto.hex(WcCrypto.deriveSymKey(peerPrivate, selfPublic)))
    }

    @Test
    fun topicIsTheKeysSha256() {
        val shared = WcCrypto.unhex("9c87e48e69b33a613907515bcd5b1b4cc10bbaf15167b19804b00f0a9217e607")
        assertEquals("a492906ccc809a411bb53a84572b57329375378c6ad7566f3e1c688200123e77", WcCrypto.topicOf(shared))
    }

    @Test
    fun sealsTheSameTypeZeroEnvelope() {
        assertEquals(type0, WcCrypto.seal(WcCrypto.unhex(symKey), message, iv))
    }

    @Test
    fun opensTypeZeroAndTypeOneEnvelopes() {
        assertEquals(message, WcCrypto.open(WcCrypto.unhex(symKey), type0))
        // Type 1 carries the sender's (self's) public key; the peer opens it with its private key.
        assertEquals(message, WcCrypto.open(null, type1, selfPrivateKey = peerPrivate))
    }

    @Test
    fun specJwtVerifiesAndItsIssuerRoundTrips() {
        val jwt = "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJkaWQ6a2V5Ono2TWtvZEhad25lVlJTaHRhTGY4SktZa3hwREdwMXZHWm5wR21kQnBYOE0yZXh4SCIsInN1YiI6ImM0NzlmZTVkYzQ2NGU3NzFlNzhiMTkzZDIzOWE2NWI1OGQyNzhjYWQxYzM0YmZiMGI1NzE2ZTViYjUxNDkyOGUiLCJhdWQiOiJ3c3M6Ly9yZWxheS53YWxsZXRjb25uZWN0LmNvbSIsImlhdCI6MTY1NjkxMDA5NywiZXhwIjoxNjU2OTk2NDk3fQ.bAKl1swvwqqV_FgwvD4Bx3Yp987B9gTpZctyBviA-EkAuWc8iI8SyokOjkv9GJESgid4U8Tf2foCgrQp2qrxBA"
        val (header, payload, signature) = jwt.split('.')
        val iss = JSONObject(String(Base64.getUrlDecoder().decode(payload))).getString("iss")
        val multicodec = base58Decode(iss.removePrefix("did:key:z"))
        assertEquals(0xed.toByte(), multicodec[0])
        assertEquals(0x01.toByte(), multicodec[1])
        val publicKey = multicodec.copyOfRange(2, multicodec.size)
        assertEquals(iss, WcCrypto.didKey(publicKey))
        val verifier = Ed25519Signer().apply {
            init(false, Ed25519PublicKeyParameters(publicKey, 0))
            "$header.$payload".toByteArray().let { update(it, 0, it.size) }
        }
        assertTrue(verifier.verifySignature(Base64.getUrlDecoder().decode(signature)))
    }

    @Test
    fun ownRelayJwtVerifies() {
        val identity = WcCrypto.Ed25519Identity(ByteArray(32) { it.toByte() })
        val jwt = WcCrypto.relayJwt(identity, WcRelay.RELAY_URL, nowSeconds = 1_800_000_000)
        val (header, payload, signature) = jwt.split('.')
        val claims = JSONObject(String(Base64.getUrlDecoder().decode(payload)))
        assertEquals(WcCrypto.didKey(identity.publicKey), claims.getString("iss"))
        assertEquals(WcRelay.RELAY_URL, claims.getString("aud"))
        assertEquals("client_auth", claims.getString("act"))
        assertEquals(1_800_086_400, claims.getLong("exp"))
        val verifier = Ed25519Signer().apply {
            init(false, Ed25519PublicKeyParameters(identity.publicKey, 0))
            "$header.$payload".toByteArray().let { update(it, 0, it.size) }
        }
        assertTrue(verifier.verifySignature(Base64.getUrlDecoder().decode(signature)))
    }

    private fun base58Decode(text: String): ByteArray {
        val alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
        var n = BigInteger.ZERO
        for (c in text) n = n.multiply(BigInteger.valueOf(58)).add(BigInteger.valueOf(alphabet.indexOf(c).toLong()))
        val bytes = n.toByteArray().let { if (it[0].toInt() == 0) it.copyOfRange(1, it.size) else it }
        val zeros = text.takeWhile { it == '1' }.length
        return ByteArray(zeros) + bytes
    }
}
