package io.virtroid.client.security

import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CatalogIconPolicyTest {
    @Test
    fun allowsOnlyCanonicalFdroidRepoHttpsUrls() {
        assertNotNull(CatalogIconPolicy.validatedUrl("https://f-droid.org/repo/icon.png"))

        assertNull(CatalogIconPolicy.validatedUrl("http://f-droid.org/repo/icon.png"))
        assertNull(CatalogIconPolicy.validatedUrl("https://cdn.f-droid.org/repo/icon.png"))
        assertNull(CatalogIconPolicy.validatedUrl("https://f-droid.org.evil.test/repo/icon.png"))
        assertNull(CatalogIconPolicy.validatedUrl("https://user@f-droid.org/repo/icon.png"))
        assertNull(CatalogIconPolicy.validatedUrl("https://f-droid.org/repo/../icon.png"))
        assertNull(CatalogIconPolicy.validatedUrl("https://f-droid.org/repo/icon.png?redirect=1"))
    }

    @Test
    fun enforcesEncodedTypeAndLengthBounds() {
        assertTrue(CatalogIconPolicy.allowsContent("image/png", 10_000))
        assertTrue(CatalogIconPolicy.allowsContent("image/webp; charset=binary", 10_000))
        assertFalse(CatalogIconPolicy.allowsContent("image/svg+xml", 10_000))
        assertFalse(CatalogIconPolicy.allowsContent("image/png", -1))
        assertFalse(
            CatalogIconPolicy.allowsContent(
                "image/png",
                CatalogIconPolicy.MAX_ENCODED_BYTES.toLong() + 1,
            ),
        )
    }

    @Test
    fun enforcesDecodedPixelAndAllocationBounds() {
        assertTrue(CatalogIconPolicy.allowsDecodedBitmap(512, 512, 512 * 512 * 4))
        assertFalse(CatalogIconPolicy.allowsDecodedBitmap(513, 1, 513 * 4))
        assertFalse(CatalogIconPolicy.allowsDecodedBitmap(512, 512, 512 * 512 * 4 + 1))
        assertFalse(CatalogIconPolicy.allowsDecodedBitmap(0, 128, 1))
    }
}
