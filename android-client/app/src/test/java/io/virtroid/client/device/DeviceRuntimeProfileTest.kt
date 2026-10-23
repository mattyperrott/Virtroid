package io.virtroid.client.device

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class DeviceRuntimeProfileTest {
    private fun size(areaWidth: Int, areaHeight: Int): Pair<Int, Int> =
        DeviceRuntimeProfile.forViewerArea(areaWidth, areaHeight).let { it.widthPx to it.heightPx }

    @Test
    fun picksTheWidestShapeThatFitsTheViewerArea() {
        // A 20:9 phone (1080x2400 at 420dpi) less the viewer's toolbar: 0.475.
        assertEquals(752 to 1600, size(1080, 2273))
        // A 19.5:9 phone (1080x2340 at 440dpi) less the toolbar: 0.489.
        assertEquals(768 to 1600, size(1080, 2207))
        // An exact 2:1 area.
        assertEquals(800 to 1600, size(900, 1800))
        // Narrower than every shape: the narrowest.
        assertEquals(720 to 1600, size(1080, 2520))
    }

    @Test
    fun landscapeAreasTurnTheShape() {
        assertEquals(1600 to 752, size(2273, 1080))
    }

    @Test
    fun theStreamFillsTheHeightOfAreasFrom20by9To2by1() {
        for (areaHeight in 2160..2400) {
            val areaWidth = 1080
            val (width, height) = size(areaWidth, areaHeight)
            // Fitted in the area as the viewer does, the stream is as tall as
            // the area and at most one step (1% of aspect, about 2% of the
            // width; 23px at 1080) narrower.
            val streamWidth = areaHeight * width.toFloat() / height
            assertTrue("$areaHeight: stream ${streamWidth}px wide", streamWidth <= areaWidth)
            assertTrue("$areaHeight: stream ${streamWidth}px wide", areaWidth - streamWidth < areaWidth * 0.023f)
        }
    }

    @Test
    fun everyShapeIsOneTheControlPlaneAccepts() {
        for (areaHeight in 1000..2600 step 7) {
            val profile = DeviceRuntimeProfile.forViewerArea(1080, areaHeight)
            assertEquals(0, profile.widthPx % 8)
            assertEquals(0, profile.heightPx % 8)
            assertEquals(0, profile.densityDpi % 20)
            assertTrue(profile.widthPx in 480..1600 && profile.heightPx in 480..1600)
        }
    }
}
