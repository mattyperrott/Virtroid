package io.virtroid.client.security

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AwayTrackerTest {
    private val away = AwayTracker()

    /** A guarded screen opens and is in front. */
    private fun open() {
        away.onStarted()
        away.takeAway()
    }

    @Test
    fun leavingAndReturningIsTimeAwayOnce() {
        open()
        assertTrue(away.onStopped(rebuilding = false))
        assertTrue(away.onStarted())
        assertTrue(away.isAway)
        assertTrue(away.takeAway())
        assertFalse(away.takeAway())
    }

    @Test
    fun goingFromOneScreenToAnotherIsNot() {
        open()
        assertFalse(away.onStarted())
        assertFalse(away.onStopped(rebuilding = false))
        assertFalse(away.takeAway())
    }

    @Test
    fun rotatingIsNotLeaving() {
        open()
        assertFalse(away.onStopped(rebuilding = true))
        away.onStarted()
        assertFalse(away.takeAway())
    }

    @Test
    fun leavingAfterARotationIs() {
        open()
        away.onStopped(rebuilding = true)
        open()
        assertTrue(away.onStopped(rebuilding = false))
        away.onStarted()
        assertTrue(away.takeAway())
    }

    @Test
    fun aRotationWhileAwayLeavesItAway() {
        open()
        away.onStopped(rebuilding = false)
        away.onStarted()
        away.onStopped(rebuilding = true)
        away.onStarted()
        assertTrue(away.takeAway())
    }

    @Test
    fun timeAwayAtTheLockedUnlockScreenIsAnsweredThere() {
        away.onStarted()
        away.onUnlockScreen(locked = true)
        away.onStopped(rebuilding = false)
        away.onStarted()
        away.onUnlockScreen(locked = true)
        // The PIN opens a guarded screen.
        assertFalse(away.takeAway())
    }

    @Test
    fun timeAwayAfterAPinEnteredJustBeforeLeavingStillCounts() {
        away.onStarted()
        away.onUnlockScreen(locked = true)
        away.onStopped(rebuilding = false)
        away.onStarted()
        away.onUnlockScreen(locked = false)
        assertTrue(away.takeAway())
    }

    @Test
    fun anExtraStopNeverCountsBelowNone() {
        away.onStopped(rebuilding = true)
        away.onStopped(rebuilding = true)
        assertTrue(away.onStarted())
    }
}
