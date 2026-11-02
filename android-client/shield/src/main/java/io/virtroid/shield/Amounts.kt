// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import java.math.BigDecimal
import java.math.BigInteger
import java.math.RoundingMode
import java.text.DecimalFormat
import java.text.DecimalFormatSymbols
import java.util.Locale

/** What the user types in the amount field, read against the asset's decimals. */
sealed interface AmountInput {
    data object Empty : AmountInput
    data object NotANumber : AmountInput
    data object Zero : AmountInput
    data class TooPrecise(val decimals: Int) : AmountInput
    data class Units(val units: BigInteger) : AmountInput
}

object Amounts {
    /**
     * Keeps what can be part of an amount: digits and one decimal point. In a
     * pasted amount the last separator is the point when it's the only one of
     * its kind (1,250.5 and 12,50), and the rest group thousands (1,250,000).
     */
    fun clean(text: String): String {
        val kept = text.filter { it.isDigit() || it == '.' || it == ',' }
        val last = kept.indexOfLast { it == '.' || it == ',' }
        val point = last >= 0 && kept.count { it == kept[last] } == 1
        return buildString {
            kept.forEachIndexed { i, c ->
                when {
                    c.isDigit() -> append(c)
                    i == last && point -> append('.')
                }
            }
        }
    }

    /** Whether `next` is `previous` with a second decimal point typed in, which the field ignores. */
    fun secondPoint(previous: String, next: String): Boolean =
        next.length == previous.length + 1 && previous.contains('.') &&
            next.count { it == '.' || it == ',' } > previous.count { it == '.' || it == ',' }

    fun read(text: String, decimals: Int): AmountInput {
        val value = text.trim().replace(',', '.')
        if (value.isEmpty()) return AmountInput.Empty
        val number = value.toBigDecimalOrNull() ?: return AmountInput.NotANumber
        if (number.signum() < 0) return AmountInput.NotANumber
        if (number.stripTrailingZeros().scale() > decimals) return AmountInput.TooPrecise(decimals)
        if (number.signum() == 0) return AmountInput.Zero
        return AmountInput.Units(number.movePointRight(decimals).setScale(0, RoundingMode.UNNECESSARY).toBigIntegerExact())
    }

    /**
     * An amount for reading: grouped, rounded down, with two decimals for a
     * stablecoin (1,250.00) and up to six for anything finer.
     */
    fun show(units: BigInteger, decimals: Int): String {
        val format = DecimalFormat("#,##0", DecimalFormatSymbols(Locale.US)).apply {
            roundingMode = RoundingMode.DOWN
            minimumFractionDigits = if (decimals <= 6) 2 else 0
            maximumFractionDigits = minOf(decimals, 6)
        }
        return format.format(BigDecimal(units, decimals))
    }

    fun show(units: BigInteger, decimals: Int, symbol: String): String = "${show(units, decimals)} $symbol"

    /** An amount the way the field takes it back: no grouping, every digit. */
    fun plain(units: BigInteger, decimals: Int): String = Shield.format(units, decimals, decimals)
}
