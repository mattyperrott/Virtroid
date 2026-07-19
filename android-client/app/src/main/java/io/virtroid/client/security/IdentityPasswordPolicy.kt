package io.virtroid.client.security

object IdentityPasswordPolicy {
    const val MIN_LENGTH = 14
    const val MAX_LENGTH = 256

    enum class Violation {
        EMPTY,
        TOO_SHORT,
        TOO_LONG,
    }

    fun violation(password: String): Violation? {
        if (password.isBlank()) {
            return Violation.EMPTY
        }
        val characterCount = password.codePointCount(0, password.length)
        return when {
            characterCount < MIN_LENGTH -> Violation.TOO_SHORT
            characterCount > MAX_LENGTH -> Violation.TOO_LONG
            else -> null
        }
    }
}
