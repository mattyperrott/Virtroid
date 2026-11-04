package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Test

class PendingClipClearTest {
    private val copiedAt = 1_000_000L
    private val pending = PendingClipClear.after(copiedAt, stamp = STAMP, clearAfterMs = 120_000L)
    private val atTimer = copiedAt + 120_000L
    private val atBackstop = copiedAt + CLIP_BACKSTOP_AFTER_MS

    @Test
    fun theBackstopIsTenMinutesAfterTheCopy() {
        assertEquals(copiedAt + 10 * 60_000L, pending.backstopAt)
    }

    @Test
    fun theBackstopNeverComesBeforeTheTimer() {
        val slow = PendingClipClear.after(copiedAt, STAMP, clearAfterMs = 60 * 60_000L)
        assertEquals(slow.clearAt, slow.backstopAt)
    }

    @Test
    fun aVeryShortTimerIsRaised() {
        assertEquals(copiedAt + 5_000L, PendingClipClear.after(copiedAt, STAMP, clearAfterMs = 1L).clearAt)
    }

    @Test
    fun ourClipStaysUntilItsTime() {
        assertEquals(ClipClearStep.WAIT, pending.next(readable = true, STAMP, atTimer - 1))
    }

    @Test
    fun ourClipGoesOnceItsTimeIsUp() {
        assertEquals(ClipClearStep.CLEAR, pending.next(readable = true, STAMP, atTimer))
    }

    @Test
    fun anotherClipIsNeverCleared() {
        assertEquals(ClipClearStep.FORGET, pending.next(readable = true, STAMP + 1, atTimer - 1))
        assertEquals(ClipClearStep.FORGET, pending.next(readable = true, STAMP + 1, atBackstop + 1))
    }

    @Test
    fun anEmptyClipboardLeavesNothingToDo() {
        assertEquals(ClipClearStep.FORGET, pending.next(readable = true, null, atTimer))
    }

    @Test
    fun aClipThatCantBeReadWaitsPastItsTime() {
        assertEquals(ClipClearStep.WAIT, pending.next(readable = false, null, atTimer))
        assertEquals(ClipClearStep.WAIT, pending.next(readable = false, null, atBackstop - 1))
    }

    @Test
    fun aClipThatCantBeReadGoesAtTheBackstop() {
        assertEquals(ClipClearStep.CLEAR, pending.next(readable = false, null, atBackstop))
    }

    @Test
    fun aClipThatWasNeverIdentifiedIsLeftOnceItCanBeRead() {
        val unknown = PendingClipClear.after(copiedAt, stamp = null, clearAfterMs = 120_000L)
        assertEquals(ClipClearStep.FORGET, unknown.next(readable = true, STAMP, atTimer))
        assertEquals(ClipClearStep.CLEAR, unknown.next(readable = false, null, atBackstop))
    }

    private companion object {
        const val STAMP = 1_762_300_000_000L
    }
}
