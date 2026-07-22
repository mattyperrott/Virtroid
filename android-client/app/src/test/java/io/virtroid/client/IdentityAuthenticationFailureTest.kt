package io.virtroid.client

import io.virtroid.client.api.VirtroidApiException
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

class IdentityAuthenticationFailureTest {
    @Test
    fun matchesBackendIdentityAuthenticationFailure() {
        assertTrue(
            VirtroidApiException(
                statusCode = 401,
                code = null,
                errorMessage = "identity authentication failed",
            ).isIdentityAuthenticationFailure(),
        )
        assertTrue(IOException("Identity Authentication Failed").isIdentityAuthenticationFailure())
    }

    @Test
    fun ignoresUnrelatedAuthenticationFailures() {
        assertFalse(IOException("runtime capability authentication failed").isIdentityAuthenticationFailure())
        assertFalse(IOException("network unavailable").isIdentityAuthenticationFailure())
    }
}
