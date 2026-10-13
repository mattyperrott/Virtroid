package io.virtroid.client

import org.junit.Assert.assertEquals
import org.junit.Test

class WalletFormatTest {
    @Test
    fun formatsUsdtFromMillionths() {
        assertEquals("0.00", WalletActivity.formatUsdt(0))
        assertEquals("25.00", WalletActivity.formatUsdt(25_000_000))
        assertEquals("69.190552", WalletActivity.formatUsdt(69_190_552))
        assertEquals("0.10", WalletActivity.formatUsdt(100_000))
        assertEquals("-1.25", WalletActivity.formatUsdt(-1_250_000))
    }
}
