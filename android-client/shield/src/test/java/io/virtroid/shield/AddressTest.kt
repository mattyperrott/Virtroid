// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import org.junit.Assert.assertEquals
import org.junit.Test

class AddressTest {
    /** EIP-55's own examples, mixed-case checksums. */
    private val checksummed = listOf(
        "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
        "0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
        "0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB",
        "0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
    )

    @Test
    fun checksumsMatchEip55() {
        for (address in checksummed) {
            assertEquals(address, Address.checksum(address.substring(2)))
            assertEquals(AddressCheck.Valid(address), Address.check(address))
        }
    }

    @Test
    fun singleCaseAddressesCarryNoChecksum() {
        assertEquals(AddressCheck.Valid("0x52908400098527886E0F7030069857D2E4169EE7"), Address.check("0x52908400098527886E0F7030069857D2E4169EE7"))
        assertEquals(AddressCheck.Valid("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"), Address.check("0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed"))
        assertEquals(AddressCheck.Valid("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"), Address.check("  0X5AAEB6053F3E94C9B9A09F33669435E7EF1BEAED "))
    }

    @Test
    fun aWrongCapitalIsCaught() {
        assertEquals(AddressCheck.BadChecksum, Address.check("0x5AAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"))
    }

    @Test
    fun partialAndWrongInput() {
        assertEquals(AddressCheck.Empty, Address.check(""))
        assertEquals(AddressCheck.Incomplete, Address.check("0"))
        assertEquals(AddressCheck.Incomplete, Address.check("0x"))
        assertEquals(AddressCheck.Incomplete, Address.check("0x5aAeb6053F"))
        assertEquals(AddressCheck.NotHex, Address.check("5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"))
        assertEquals(AddressCheck.NotHex, Address.check("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeZ"))
        assertEquals(AddressCheck.NotHex, Address.check("vitalik.eth"))
        assertEquals(AddressCheck.TooLong, Address.check("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed00"))
        assertEquals(AddressCheck.Zero, Address.check("0x0000000000000000000000000000000000000000"))
    }

    @Test
    fun shortForm() {
        assertEquals("0x5aAe…eAed", Address.short("0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed"))
    }
}
