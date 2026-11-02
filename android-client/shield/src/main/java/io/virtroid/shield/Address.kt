// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import org.bouncycastle.crypto.digests.KeccakDigest

/**
 * An EVM address as the user types or pastes it, checked the way wallets do:
 * 0x and 40 hex digits, and when the digits mix upper and lower case, the
 * EIP-55 checksum that case carries. An address says nothing of which chain
 * it's meant for; the screens name the chain beside it.
 */
sealed interface AddressCheck {
    data object Empty : AddressCheck
    /** A start of an address: 0x and fewer than 40 hex digits. */
    data object Incomplete : AddressCheck
    data object NotHex : AddressCheck
    data object TooLong : AddressCheck
    /** Mixed case that isn't the EIP-55 checksum: likely a typo. */
    data object BadChecksum : AddressCheck
    data object Zero : AddressCheck
    data class Valid(val checksummed: String) : AddressCheck
}

object Address {
    private val HEX = Regex("^[0-9a-fA-F]*$")

    fun check(text: String): AddressCheck {
        val value = text.trim()
        if (value.isEmpty()) return AddressCheck.Empty
        val digits = when {
            value.startsWith("0x") || value.startsWith("0X") -> value.substring(2)
            "0x".startsWith(value) -> return AddressCheck.Incomplete
            else -> return AddressCheck.NotHex
        }
        if (!HEX.matches(digits)) return AddressCheck.NotHex
        if (digits.length < 40) return AddressCheck.Incomplete
        if (digits.length > 40) return AddressCheck.TooLong
        val checksummed = checksum(digits)
        val mixed = digits != digits.lowercase() && digits != digits.uppercase()
        if (mixed && "0x$digits" != checksummed) return AddressCheck.BadChecksum
        if (digits.all { it == '0' }) return AddressCheck.Zero
        return AddressCheck.Valid(checksummed)
    }

    /** EIP-55: each letter upper case where the Keccak-256 of the lower-case address has a nibble of 8 or more. */
    fun checksum(digits: String): String {
        val lower = digits.lowercase()
        val digest = KeccakDigest(256)
        val bytes = lower.toByteArray(Charsets.US_ASCII)
        digest.update(bytes, 0, bytes.size)
        val hash = ByteArray(32).also { digest.doFinal(it, 0) }
        val out = StringBuilder("0x")
        lower.forEachIndexed { i, c ->
            val nibble = (hash[i / 2].toInt() shr (if (i % 2 == 0) 4 else 0)) and 0xf
            out.append(if (c.isLetter() && nibble >= 8) c.uppercaseChar() else c)
        }
        return out.toString()
    }

    fun same(a: String?, b: String?): Boolean = a != null && b != null && a.equals(b, ignoreCase = true)

    fun short(address: String): String = if (address.length > 12) "${address.take(6)}…${address.takeLast(4)}" else address
}
