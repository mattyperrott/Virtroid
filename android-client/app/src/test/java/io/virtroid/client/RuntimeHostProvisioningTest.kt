package io.virtroid.client

import io.virtroid.client.api.VirtroidApiException
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.fail
import org.junit.Test

class RuntimeHostProvisioningTest {
    private val preparing = VirtroidApiException(503, RUNTIME_HOST_PROVISIONING_CODE, "being prepared")

    @Test
    fun retriesUntilTheServerIsReady() = runBlocking {
        var clock = 0L
        var calls = 0
        val waits = mutableListOf<Int>()
        val result = retryWhileRuntimeHostProvisioning(
            onWaiting = { waits += it },
            nowMs = { clock },
            sleep = { clock += it },
        ) {
            calls++
            if (calls < 3) throw preparing
            "started"
        }
        assertEquals("started", result)
        assertEquals(listOf(1, 2), waits)
        assertEquals(2 * RUNTIME_HOST_PROVISIONING_RETRY_MS, clock)
    }

    @Test
    fun otherErrorsAreNotRetried() = runBlocking {
        val noHost = VirtroidApiException(409, "no_ready_host", "no node")
        var calls = 0
        try {
            retryWhileRuntimeHostProvisioning(onWaiting = { fail("waited on $it") }, sleep = {}) {
                calls++
                throw noHost
            }
        } catch (error: VirtroidApiException) {
            assertSame(noHost, error)
        }
        assertEquals(1, calls)
    }

    @Test
    fun givesUpAfterTheMaximumWait() = runBlocking {
        var clock = 0L
        try {
            retryWhileRuntimeHostProvisioning<Unit>(
                onWaiting = {},
                nowMs = { clock },
                sleep = { clock += it },
            ) { throw preparing }
            fail("kept waiting forever")
        } catch (error: VirtroidApiException) {
            assertSame(preparing, error)
        }
        assertEquals(true, clock >= RUNTIME_HOST_PROVISIONING_MAX_WAIT_MS)
    }
}
