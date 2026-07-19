package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class IdentityPasswordPolicyTest {
    @Test
    fun rejectsBlankAndShortPasswords() {
        assertEquals(
            IdentityPasswordPolicy.Violation.EMPTY,
            IdentityPasswordPolicy.violation("   "),
        )
        assertEquals(
            IdentityPasswordPolicy.Violation.TOO_SHORT,
            IdentityPasswordPolicy.violation("short-pass"),
        )
    }

    @Test
    fun acceptsLongPassphrasesWithoutCompositionRules() {
        assertNull(IdentityPasswordPolicy.violation("correct horse battery staple"))
        assertNull(IdentityPasswordPolicy.violation("this is long enough"))
    }

    @Test
    fun countsUnicodeCodePointsAndCapsExcessiveInput() {
        assertNull(IdentityPasswordPolicy.violation("🔐".repeat(IdentityPasswordPolicy.MIN_LENGTH)))
        assertEquals(
            IdentityPasswordPolicy.Violation.TOO_LONG,
            IdentityPasswordPolicy.violation("a".repeat(IdentityPasswordPolicy.MAX_LENGTH + 1)),
        )
    }
}
