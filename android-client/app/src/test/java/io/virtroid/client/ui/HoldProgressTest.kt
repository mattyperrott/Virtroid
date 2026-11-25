package io.virtroid.client.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class HoldProgressTest {
    private val progress = HoldProgress(holdMs = 1_200L, drainMs = 350L)

    /** Frames of 16 ms for [ms], counting how often it came full. */
    private fun frames(ms: Long): Int = (0 until ms / 16).count { progress.advance(16L) }

    @Test
    fun fillsOnceWhenHeldLongEnough() {
        progress.hold()
        assertEquals(0, frames(1_100L))
        assertEquals(1, frames(200L))
        // Still held, or held again, it does not confirm a second time.
        progress.hold()
        assertEquals(0, frames(3_000L))
        assertFalse(progress.isMoving)
    }

    @Test
    fun aTapNeverFills() {
        repeat(50) {
            progress.hold()
            assertEquals(0, frames(100L))
            progress.release()
            assertEquals(0, frames(400L))
        }
        assertEquals(0f, progress.level)
    }

    @Test
    fun lettingGoJustBeforeTheEndDrains() {
        progress.hold()
        assertEquals(0, frames(1_190L))
        progress.release()
        assertEquals(0, frames(5_000L))
        assertEquals(0f, progress.level)
        assertFalse(progress.isMoving)
    }

    @Test
    fun aStalledScreenDoesNotCountAsHeld() {
        progress.hold()
        assertFalse(progress.advance(10_000L))
        assertTrue(progress.level < 0.1f)
    }

    @Test
    fun notHeldItStaysEmpty() {
        assertEquals(0, frames(5_000L))
        assertEquals(0f, progress.level)
    }
}
