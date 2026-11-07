package io.virtroid.client.data

import java.security.MessageDigest

/**
 * Which runtime errors the app has already announced. A runtime's error stays
 * on the server until its next start, so each is announced once, not on every
 * launch. Only digests of the runtime and the error are kept, never the text.
 */
internal object RuntimeErrorNotices {
    const val MAX_REMEMBERED = 32

    /**
     * The remembered list with this error added, or null when it was already
     * there. [remembered] is what the previous call returned, or "".
     */
    fun record(remembered: String, runtimeId: String, error: String): String? {
        val digest = MessageDigest.getInstance("SHA-256")
            .digest("$runtimeId\u0000$error".toByteArray())
            .joinToString("") { "%02x".format(it) }
        val seen = remembered.split(',').filter { it.isNotBlank() }
        if (digest in seen) {
            return null
        }
        return (seen + digest).takeLast(MAX_REMEMBERED).joinToString(",")
    }
}
