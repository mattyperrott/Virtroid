// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import org.junit.Assert.assertEquals
import org.junit.Test
import java.math.BigInteger

class AmountsTest {
    @Test
    fun cleanKeepsOneDecimalPoint() {
        assertEquals("1250.5", Amounts.clean("1,250.5"))
        assertEquals("12.50", Amounts.clean("12,50"))
        assertEquals("1250000", Amounts.clean("1,250,000"))
        assertEquals("1250000.75", Amounts.clean("1.250.000,75"))
        assertEquals("1.", Amounts.clean("1."))
        assertEquals("100", Amounts.clean("100 USDC"))
        assertEquals("", Amounts.clean("abc"))
    }

    @Test
    fun aSecondTypedPointIsIgnored() {
        assertEquals(true, Amounts.secondPoint("1.5", "1.5."))
        assertEquals(true, Amounts.secondPoint("1.5", "1.,5"))
        assertEquals(false, Amounts.secondPoint("1.5", "1.55"))
        assertEquals(false, Amounts.secondPoint("15", "1.5"))
    }

    @Test
    fun readsAgainstTheAssetsDecimals() {
        assertEquals(AmountInput.Empty, Amounts.read("", 6))
        assertEquals(AmountInput.NotANumber, Amounts.read(".", 6))
        assertEquals(AmountInput.Zero, Amounts.read("0.000", 6))
        assertEquals(AmountInput.Units(BigInteger("1250500000")), Amounts.read("1250.5", 6))
        assertEquals(AmountInput.Units(BigInteger("1000000")), Amounts.read("1.", 6))
        assertEquals(AmountInput.Units(BigInteger("1000000")), Amounts.read("1.000000000", 6))
        assertEquals(AmountInput.TooPrecise(6), Amounts.read("0.0000001", 6))
        assertEquals(AmountInput.Units(BigInteger("1")), Amounts.read("0.000000000000000001", 18))
    }

    @Test
    fun showsGroupedAndRoundedDown() {
        assertEquals("1,250.00", Amounts.show(BigInteger("1250000000"), 6))
        assertEquals("25.30", Amounts.show(BigInteger("25300000"), 6))
        assertEquals("0.123456", Amounts.show(BigInteger("123456"), 6))
        assertEquals("0.042", Amounts.show(BigInteger("42000000000000000"), 18))
        assertEquals("0.123456", Amounts.show(BigInteger("123456789000000000"), 18))
        assertEquals("1 ETH", Amounts.show(BigInteger("1000000000000000000"), 18, "ETH"))
    }

    @Test
    fun plainRoundTrips() {
        val units = BigInteger("1250500001")
        assertEquals(AmountInput.Units(units), Amounts.read(Amounts.plain(units, 6), 6))
    }
}
