package io.virtroid.client.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class PullRefreshLayoutTest {
    @Test
    fun theListGivesLessTheFurtherItIsPulledAndNeverPastItsLimit() {
        assertEquals(0f, PullRefreshLayout.offsetFor(0f))
        assertEquals(0f, PullRefreshLayout.offsetFor(-80f))
        val offsets = (0..2_000 step 20).map { PullRefreshLayout.offsetFor(it.toFloat()) }
        val steps = offsets.zipWithNext { a, b -> b - a }
        assertTrue(steps.all { it >= 0f })
        assertTrue(steps.zipWithNext().all { (a, b) -> b <= a })
        assertTrue(offsets.last() <= 140f)
    }
}
