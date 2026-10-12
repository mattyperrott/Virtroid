package io.virtroid.client

import io.virtroid.client.api.VirtroidApiException
import kotlinx.coroutines.runBlocking
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.fail
import org.junit.Test

class RuntimeHostProvisioningTest {
    private fun preparing(state: String, detail: String, elapsed: Long) = VirtroidApiException(
        503,
        RUNTIME_HOST_PROVISIONING_CODE,
        "being prepared",
        JSONObject()
            .put("code", RUNTIME_HOST_PROVISIONING_CODE)
            .put("vm_state", state)
            .put("vm_stage", "Creating your private server")
            .put("vm_progress", detail)
            .put("vm_region", "snp-eu")
            .put("vm_elapsed_seconds", elapsed),
    )

    @Test
    fun retriesUntilTheServerIsReadyAndReportsProgress() = runBlocking {
        var clock = 0L
        var calls = 0
        var foregroundChecks = 0
        val reports = mutableListOf<RuntimeHostProgress>()
        val result = retryWhileRuntimeHostProvisioning(
            onWaiting = { _, progress -> reports += progress },
            nowMs = { clock },
            sleep = { clock += it },
            beforeAttempt = { foregroundChecks++ },
        ) {
            calls++
            when (calls) {
                1 -> throw preparing("creating", "The compute node is downloading the VM image", 40)
                2 -> throw preparing("creating", "Booting the VM and checking its root disk", 95)
                else -> "started"
            }
        }
        assertEquals("started", result)
        assertEquals(3, foregroundChecks)
        assertEquals(listOf(40L, 95L), reports.map { it.elapsedSeconds })
        assertEquals("Booting the VM and checking its root disk", reports.last().detail)
        assertEquals("snp-eu", reports.last().region)
        assertEquals(2 * RUNTIME_HOST_PROVISIONING_RETRY_MS, clock)
    }

    @Test
    fun otherErrorsAreNotRetried() = runBlocking {
        val noHost = VirtroidApiException(409, "no_ready_host", "no node")
        var calls = 0
        try {
            retryWhileRuntimeHostProvisioning(onWaiting = { attempt, _ -> fail("waited on $attempt") }, sleep = {}) {
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
        val error = preparing("creating", "", 0)
        try {
            retryWhileRuntimeHostProvisioning<Unit>(
                onWaiting = { _, _ -> },
                nowMs = { clock },
                sleep = { clock += it },
            ) { throw error }
            fail("kept waiting forever")
        } catch (thrown: VirtroidApiException) {
            assertSame(error, thrown)
        }
        assertEquals(true, clock >= RUNTIME_HOST_PROVISIONING_MAX_WAIT_MS)
    }

    @Test
    fun elapsedTimeFormatting() {
        assertEquals("45s", formatElapsed(45))
        assertEquals("2m 05s", formatElapsed(125))
    }
}
