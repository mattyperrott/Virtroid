package io.virtroid.client

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PermissionAskTest {
    @Test
    fun aGrantIsNeverAskedOut() {
        assertFalse(androidWontAsk(granted = true, showRationale = false, requestedBefore = true))
    }

    @Test
    fun aFirstRefusalLeavesTheDialog() {
        // Refused once: Android shows a rationale and still asks.
        assertFalse(androidWontAsk(granted = false, showRationale = true, requestedBefore = false))
        // Dismissed the first time: no rationale, but Android asks again.
        assertFalse(androidWontAsk(granted = false, showRationale = false, requestedBefore = false))
    }

    @Test
    fun aSecondRefusalOrABlockedAskIsAskedOut() {
        // Refused again, or Android answered without a dialog.
        assertTrue(androidWontAsk(granted = false, showRationale = false, requestedBefore = true))
        // Dismissed after one refusal: the rationale is still there.
        assertFalse(androidWontAsk(granted = false, showRationale = true, requestedBefore = true))
    }
}
