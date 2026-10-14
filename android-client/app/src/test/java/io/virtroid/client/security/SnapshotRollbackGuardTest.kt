package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class SnapshotRollbackGuardTest {
    @Test
    fun rejectsOlderGeneration() {
        assertThrows(SnapshotRollbackException::class.java) {
            verifySnapshotAdvance(3, "snapshot-3", 2, "snapshot-2", "runtime-1")
        }
    }

    @Test
    fun rejectsForkAtSameGeneration() {
        assertThrows(SnapshotRollbackException::class.java) {
            verifySnapshotAdvance(3, "snapshot-3", 3, "snapshot-fork", "runtime-1")
        }
    }

    @Test
    fun acceptsNextGenerationAndIdempotentRead() {
        verifySnapshotAdvance(2, "snapshot-2", 3, "snapshot-3", "runtime-1")
        verifySnapshotAdvance(3, "snapshot-3", 3, "snapshot-3", "runtime-1")
    }

    @Test
    fun lostSnapshotIsAcceptedOnlyWhenTheFloorCoversIt() {
        // Generation 4 was lost with its VM, and the server says so.
        assertTrue(isLostSnapshotState(4, 0, "", 4))
        assertTrue(isLostSnapshotState(4, 0, "", 1000))
        // An empty answer without a floor covering what this phone saw.
        assertFalse(isLostSnapshotState(4, 0, "", 3))
        assertFalse(isLostSnapshotState(4, 0, "", 0))
        // Not empty, or nothing seen before.
        assertFalse(isLostSnapshotState(4, 2, "snapshot-2", 4))
        assertFalse(isLostSnapshotState(null, 0, "", 4))
        // After the loss, the record is the floor: only newer snapshots pass.
        verifySnapshotAdvance(4, "", 5, "snapshot-5", "runtime-1")
        assertThrows(SnapshotRollbackException::class.java) {
            verifySnapshotAdvance(4, "", 4, "snapshot-4", "runtime-1")
        }
        assertThrows(SnapshotRollbackException::class.java) {
            verifySnapshotAdvance(4, "", 3, "snapshot-3", "runtime-1")
        }
    }

    @Test
    fun comparesFloorPlusGeneration() {
        assertEquals(3L, effectiveSnapshotGeneration(3, "snapshot-3", 0, "runtime-1"))
        // Numbering restarts at 1 after generation 4 was lost.
        assertEquals(5L, effectiveSnapshotGeneration(1, "snapshot-a", 4, "runtime-1"))
        assertEquals(0L, effectiveSnapshotGeneration(0, "", 4, "runtime-1"))
        assertThrows(SnapshotRollbackException::class.java) {
            effectiveSnapshotGeneration(1, "snapshot-a", -1, "runtime-1")
        }
        assertThrows(SnapshotRollbackException::class.java) {
            effectiveSnapshotGeneration(2, "snapshot-a", Long.MAX_VALUE - 1, "runtime-1")
        }
    }

    @Test
    fun expectedResetCompletesOnlyForEmptySnapshotState() {
        assertTrue(isEmptySnapshotState(0, ""))
        assertFalse(isEmptySnapshotState(0, "snapshot"))
        assertFalse(isEmptySnapshotState(1, "snapshot-1"))
    }
}
