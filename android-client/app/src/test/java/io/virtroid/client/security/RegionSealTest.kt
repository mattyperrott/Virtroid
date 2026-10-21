package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class RegionSealTest {
    private fun ByteArray.hex() = joinToString("") { "%02x".format(it) }
    private val viewerKey = BlobKeyEnvelopeCrypto.runtimeViewerKey(ByteArray(32) { 0x42 }, "rt-region-vector")
    private val berlin = RuntimeRegion(52.52, 13.405, "Europe/Berlin", "Berlin, Germany")

    /** Sealed by virtnoded's TestSealedRegionVector. */
    @Test
    fun opensTheGoNodesRegion() {
        assertEquals(
            "8374bee8c6b12bc495481ac46b6648f3673752ba8b1ed12db473b6689be28c2a",
            RegionSeal.regionKey(viewerKey).hex(),
        )
        val sealed = "VlJUUkVHMQoHBwcHBwcHBwcHBwdY6Nm5LscVNdk3Ui_JMIFT5uiUGdeyapmBo-D350VTyYKiF_OLL-rEJNqCRDOO8mZV_rMht6TTy6kSeXkZKBKAn4szNPcuz2fSlK1V1ZPBTb4n40ImBK38RmpqScENfbwCkAlO4QeGxI7b1N__8YLmInBu4xO9"
        assertEquals(berlin, RegionSeal.open(viewerKey, "rt-region-vector", sealed))
    }

    @Test
    fun opensOnlyForItsRuntime() {
        val sealed = RegionSeal.seal(viewerKey, "rt-region-vector", berlin)
        assertEquals(berlin, RegionSeal.open(viewerKey, "rt-region-vector", sealed))
        assertNull(RegionSeal.open(viewerKey, "another-runtime", sealed))
        val otherKey = BlobKeyEnvelopeCrypto.runtimeViewerKey(ByteArray(32) { 0x43 }, "rt-region-vector")
        assertNull(RegionSeal.open(otherKey, "rt-region-vector", sealed))
        assertNull(RegionSeal.open(viewerKey, "rt-region-vector", "not sealed"))
    }

    @Test(expected = IllegalArgumentException::class)
    fun refusesATimezoneThatIsNotAName() {
        RegionSeal.seal(viewerKey, "rt", berlin.copy(timezone = "UTC; reboot"))
    }
}
