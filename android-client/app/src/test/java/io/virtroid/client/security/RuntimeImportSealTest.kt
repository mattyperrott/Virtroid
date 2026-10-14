package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Test

/** Must match virtnoded's TestSealedImportVector. */
class RuntimeImportSealTest {
    private fun ByteArray.hex() = joinToString("") { "%02x".format(it) }

    @Test
    fun sealMatchesTheGoNode() {
        val viewerKey = BlobKeyEnvelopeCrypto.runtimeViewerKey(ByteArray(32) { 0x42 }, "rt-viewer-vector")
        assertEquals(
            "df0e102409cd96117bfbb35e5c07ff8706656cfb97644b380d3c5b9e60cfc533",
            RuntimeImportSeal.importKey(viewerKey).hex(),
        )
        val sealed = RuntimeImportSeal.seal(
            viewerKey, "rt-viewer-vector", "file", "notes.txt", "hello".toByteArray(), nonce = ByteArray(12) { 7 },
        )
        assertEquals(
            "565254494d50310a070707070707070707070707df7a46737b2d5b626521866a3af4e4067fca1a588d7943126752d1fda832b98d",
            sealed.hex(),
        )
    }
}
