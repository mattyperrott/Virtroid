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
        assertTrue(isLostSnapshotState(4, 4))
        assertTrue(isLostSnapshotState(4, 1000))
        // An empty answer without a floor covering what this phone saw.
        assertFalse(isLostSnapshotState(4, 3))
        assertFalse(isLostSnapshotState(4, 0))
        assertFalse(isLostSnapshotState(null, 4))
        // The record stays at 4: the VM numbers the next snapshot 5, and
        // nothing older passes.
        verifySnapshotAdvance(4, "snapshot-4", 5, "snapshot-5", "runtime-1")
        assertThrows(SnapshotRollbackException::class.java) {
            verifySnapshotAdvance(4, "snapshot-4", 3, "snapshot-3", "runtime-1")
        }
    }

    @Test
    fun serverFloorNoLongerRaisesWhatIsCompared() {
        checkSnapshotMetadata(3, "snapshot-3", "runtime-1")
        checkSnapshotMetadata(0, "", "runtime-1")
        assertThrows(SnapshotRollbackException::class.java) {
            checkSnapshotMetadata(-1, "snapshot", "runtime-1")
        }
        assertThrows(SnapshotRollbackException::class.java) {
            checkSnapshotMetadata(2, "", "runtime-1")
        }
    }

    @Test
    fun recordsFromOlderAppsAreRecognised() {
        assertEquals(SnapshotRecord(1005, "snapshot-5"), parseSnapshotRecord("1005|snapshot-5"))
        assertEquals(SnapshotRecord(5, "snapshot-5"), parseSnapshotRecord("${RECORD_PREFIX}5|snapshot-5"))
        assertEquals(null, parseSnapshotRecord("garbage"))
    }

    @Test
    fun expectedResetCompletesOnlyForEmptySnapshotState() {
        assertTrue(isEmptySnapshotState(0, ""))
        assertFalse(isEmptySnapshotState(0, "snapshot"))
        assertFalse(isEmptySnapshotState(1, "snapshot-1"))
    }
}
