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
}
