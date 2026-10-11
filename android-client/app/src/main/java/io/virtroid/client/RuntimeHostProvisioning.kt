package io.virtroid.client

import io.virtroid.client.api.VirtroidApiException
import kotlinx.coroutines.delay

internal const val RUNTIME_HOST_PROVISIONING_CODE = "runtime_host_provisioning"

/** How often to ask again while the server prepares the user's private runtime server. */
internal const val RUNTIME_HOST_PROVISIONING_RETRY_MS = 15_000L

/** Stop waiting before the server's own setup timeout gives up on the VM. */
internal const val RUNTIME_HOST_PROVISIONING_MAX_WAIT_MS = 25 * 60_000L

/**
 * Runs [block], and while the server answers that the account's private
 * runtime server is still being prepared, waits and runs it again.
 * [onWaiting] is told about each wait, starting at 1.
 */
internal suspend fun <T> retryWhileRuntimeHostProvisioning(
    onWaiting: (attempt: Int) -> Unit,
    nowMs: () -> Long = { System.currentTimeMillis() },
    sleep: suspend (Long) -> Unit = { delay(it) },
    block: suspend () -> T,
): T {
    val deadline = nowMs() + RUNTIME_HOST_PROVISIONING_MAX_WAIT_MS
    var attempt = 0
    while (true) {
        try {
            return block()
        } catch (error: VirtroidApiException) {
            if (error.code != RUNTIME_HOST_PROVISIONING_CODE || nowMs() >= deadline) {
                throw error
            }
            attempt++
            onWaiting(attempt)
            sleep(RUNTIME_HOST_PROVISIONING_RETRY_MS)
        }
    }
}
