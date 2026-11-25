package io.virtroid.client.security

import java.util.Arrays
import java.util.concurrent.FutureTask
import kotlin.concurrent.thread

/** A key being derived on a thread of its own, taken once or wiped. */
internal class BackgroundKey(derive: () -> ByteArray?) {
    private val task = FutureTask<ByteArray?>(derive)

    init {
        thread(name = "vault-key") { task.run() }
    }

    /** Waits for the key, which the caller now wipes; null if it could not be derived. */
    fun take(): ByteArray? = runCatching { task.get() }.getOrNull()

    /** Waits for the key and wipes it: it was not for a secret that matched. */
    fun wipe() {
        take()?.let { Arrays.fill(it, 0) }
    }

    companion object {
        /** Runs [check] while [key] is derived; unless it matches, the key is wiped, also if it throws. */
        fun checkAlongside(key: BackgroundKey?, check: () -> Boolean): Boolean {
            var matches = false
            try {
                matches = check()
            } finally {
                if (!matches) key?.wipe()
            }
            return matches
        }
    }
}
