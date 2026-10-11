package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Test

class ConfidentialRuntimeGuardTest {
    @Test
    fun measurementListIgnoresCommentsAndMalformedLines() {
        val good = "AB".repeat(48)
        val parsed = ConfidentialRuntimeGuard.parseMeasurements(
            listOf(
                "# approved images",
                "$good  # runtime 1, 2 vCPUs, Genoa",
                "",
                "abc",
                "zz".repeat(48),
            ),
        )
        assertEquals(setOf(good.lowercase()), parsed)
    }
}
