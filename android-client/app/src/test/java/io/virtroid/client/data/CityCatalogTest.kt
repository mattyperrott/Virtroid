package io.virtroid.client.data

import org.junit.Assert.assertEquals
import org.junit.Test

class CityCatalogTest {
    @Test
    fun foldIgnoresCaseAndAccents() {
        assertEquals("sao paulo", CityCatalog.fold("  São Paulo "))
        assertEquals("zurich", CityCatalog.fold("Zürich"))
        assertEquals("münchen".let(CityCatalog::fold), CityCatalog.fold("MUNCHEN"))
    }
}
