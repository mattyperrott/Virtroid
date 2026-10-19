package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Test

class ConfidentialRuntimeGuardTest {
    @Test
    fun firmwareComparesEveryComponent() {
        val minimum = SnpTcb(bootloader = 12, tee = 0, snp = 28, microcode = 88)
        assertEquals(true, minimum.isAtLeast(minimum))
        assertEquals(true, minimum.copy(microcode = 90).isAtLeast(minimum))
        assertEquals(false, minimum.copy(snp = 27).isAtLeast(minimum))
        assertEquals(false, minimum.copy(bootloader = 11, microcode = 99).isAtLeast(minimum))
        assertEquals(false, minimum.isAtLeast(minimum.copy(fmc = 1)))
    }
}
