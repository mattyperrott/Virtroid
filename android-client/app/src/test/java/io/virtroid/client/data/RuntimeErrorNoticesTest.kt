package io.virtroid.client.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

class RuntimeErrorNoticesTest {
    private val error = "Reconcile failed: snapshot source contains unsupported symlink \"data/org.fdroid.fdroid/files/fdroid/index.html\""

    @Test
    fun anErrorIsAnnouncedOnceUntilItChanges() {
        val first = RuntimeErrorNotices.record("", "runtime-a", error)
        assertNotNull("a new error is announced", first)
        assertNull("the same error again is not", RuntimeErrorNotices.record(first!!, "runtime-a", error))

        val changed = RuntimeErrorNotices.record(first, "runtime-a", "Runtime start failed")
        assertNotNull("a different error of the same runtime is announced", changed)
        assertNotNull("the same error of another runtime is announced", RuntimeErrorNotices.record(changed!!, "runtime-b", error))
    }

    @Test
    fun onlyDigestsAreKept() {
        val remembered = RuntimeErrorNotices.record("", "runtime-a", error)!!
        assertFalse(remembered.contains("runtime-a"))
        assertFalse(remembered.contains("fdroid"))
        assertEquals(64, remembered.length)
    }

    @Test
    fun theListKeepsOnlyTheMostRecentErrors() {
        var remembered = ""
        for (i in 0..RuntimeErrorNotices.MAX_REMEMBERED) {
            remembered = RuntimeErrorNotices.record(remembered, "runtime-$i", error)!!
        }
        assertEquals(RuntimeErrorNotices.MAX_REMEMBERED, remembered.split(',').size)
        assertNull("a recent error is still remembered", RuntimeErrorNotices.record(remembered, "runtime-${RuntimeErrorNotices.MAX_REMEMBERED}", error))
        assertNotNull("the oldest has been forgotten", RuntimeErrorNotices.record(remembered, "runtime-0", error))
    }
}
