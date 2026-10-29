package io.virtroid.client

import io.virtroid.client.api.VirtroidApiException
import kotlinx.coroutines.delay

internal const val RUNTIME_HOST_PROVISIONING_CODE = "runtime_host_provisioning"

/**
 * How often to ask again while the server prepares the user's private runtime
 * server, when it does not say. It says retry_after_seconds, shorter once the
 * VM is booting; that is clamped to the range below.
 */
internal const val RUNTIME_HOST_PROVISIONING_RETRY_MS = 5_000L
internal const val RUNTIME_HOST_PROVISIONING_MIN_RETRY_MS = 1_000L
internal const val RUNTIME_HOST_PROVISIONING_MAX_RETRY_MS = 10_000L

/** Stop waiting before the server's own setup timeout gives up on the VM. */
internal const val RUNTIME_HOST_PROVISIONING_MAX_WAIT_MS = 25 * 60_000L

/** Where the account's private runtime server is on its way up, as the server reports it. */
internal data class RuntimeHostProgress(
    val state: String,
    val stage: String,
    val detail: String,
    val region: String,
    val elapsedSeconds: Long,
    val retryAfterMs: Long = RUNTIME_HOST_PROVISIONING_RETRY_MS,
)

internal fun VirtroidApiException.runtimeHostProgress(): RuntimeHostProgress? {
    if (code != RUNTIME_HOST_PROVISIONING_CODE) return null
    val body = details ?: return RuntimeHostProgress("", "", "", "", 0)
    val retryAfterSeconds = body.optLong("retry_after_seconds", 0L)
    return RuntimeHostProgress(
        state = body.optString("vm_state"),
        stage = body.optString("vm_stage"),
        detail = body.optString("vm_progress"),
        region = body.optString("vm_region"),
        elapsedSeconds = body.optLong("vm_elapsed_seconds"),
        retryAfterMs = if (retryAfterSeconds > 0) {
            (retryAfterSeconds * 1000).coerceIn(RUNTIME_HOST_PROVISIONING_MIN_RETRY_MS, RUNTIME_HOST_PROVISIONING_MAX_RETRY_MS)
        } else {
            RUNTIME_HOST_PROVISIONING_RETRY_MS
        },
    )
}

internal fun formatElapsed(seconds: Long): String =
    if (seconds < 60) "${seconds}s" else "${seconds / 60}m ${"%02d".format(seconds % 60)}s"

/**
 * Runs [block], and while the server answers that the account's private
 * runtime server is still being prepared, waits and runs it again.
 * [onWaiting] gets each progress report. [beforeAttempt] runs before every
 * attempt; screens pass one that suspends while they are not in the
 * foreground, because the device keys only sign while the phone is unlocked.
 */
internal suspend fun <T> retryWhileRuntimeHostProvisioning(
    onWaiting: (attempt: Int, progress: RuntimeHostProgress) -> Unit,
    nowMs: () -> Long = { System.currentTimeMillis() },
    sleep: suspend (Long) -> Unit = { delay(it) },
    beforeAttempt: suspend () -> Unit = {},
    block: suspend () -> T,
): T {
    val deadline = nowMs() + RUNTIME_HOST_PROVISIONING_MAX_WAIT_MS
    var attempt = 0
    while (true) {
        beforeAttempt()
        try {
            return block()
        } catch (error: VirtroidApiException) {
            val progress = error.runtimeHostProgress()
            if (progress == null || nowMs() >= deadline) {
                throw error
            }
            attempt++
            onWaiting(attempt, progress)
            sleep(progress.retryAfterMs)
        }
    }
}
