package io.virtroid.client.security

import org.junit.Assert.assertThrows
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
}
