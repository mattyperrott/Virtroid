package io.virtroid.client.viewer

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ClipboardGateTest {
    private fun open() = ClipboardGate().apply {
        runtimeReady = true
        focused = true
    }

    @Test
    fun anOlderRuntimeNeverOpensIt() {
        val gate = ClipboardGate().apply { focused = true }
        assertFalse(gate.shouldSend(1))
        assertFalse(gate.takesRuntimeClips)
    }

    @Test
    fun nothingMovesWithoutFocus() {
        val gate = open().apply { focused = false }
        assertFalse(gate.shouldSend(1))
        assertFalse(gate.takesRuntimeClips)
    }

    @Test
    fun aClipGoesOnce() {
        val gate = open()
        assertTrue(gate.shouldSend(1))
        gate.settle(1)
        assertFalse(gate.shouldSend(1))
    }

    @Test
    fun theSameTextCopiedAgainIsANewClip() {
        val gate = open()
        gate.settle(1)
        assertTrue(gate.shouldSend(2))
    }

    @Test
    fun aClipFromTheRuntimeDoesNotGoBack() {
        val gate = open()
        assertTrue(gate.takesRuntimeClips)
        gate.settleFromRuntime(5)
        assertFalse(gate.shouldSend(5))
    }

    @Test
    fun aRuntimeClipIsOverdueOnceItsTimeIsUp() {
        val gate = open()
        gate.settleFromRuntime(1_000)
        assertFalse(gate.isOverdue(1_000, 1_000 + RUNTIME_CLIP_CLEAR_AFTER_MS - 1))
        assertTrue(gate.isOverdue(1_000, 1_000 + RUNTIME_CLIP_CLEAR_AFTER_MS))
    }

    @Test
    fun aClipOfThePhonesIsNeverOverdue() {
        val gate = open()
        gate.settleFromRuntime(1_000)
        // Copied on the phone since, so it isn't this viewer's to clear.
        assertFalse(gate.isOverdue(2_000, 2_000 + RUNTIME_CLIP_CLEAR_AFTER_MS))
        gate.settle(3_000)
        assertFalse(gate.isOverdue(3_000, 3_000 + RUNTIME_CLIP_CLEAR_AFTER_MS))
    }
}
