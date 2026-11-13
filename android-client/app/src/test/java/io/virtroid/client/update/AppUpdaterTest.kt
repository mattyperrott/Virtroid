package io.virtroid.client.update

import android.content.pm.PackageManager
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException

class AppUpdaterTest {
    @Test
    fun versionsCompareNumerically() {
        assertTrue(AppUpdater.compareVersions("0.4.10", "0.4.9") > 0)
        assertTrue(AppUpdater.compareVersions("0.5", "0.4.9") > 0)
        assertTrue(AppUpdater.compareVersions("0.4.4", "0.4.4") == 0)
        assertTrue(AppUpdater.compareVersions("0.4.3", "0.4.4") < 0)
        assertTrue(AppUpdater.compareVersions("1.0.0", "0.9.99") > 0)
    }

    @Test
    fun onlyTheInstalledSignersPass() {
        val ours = setOf("aa")
        assertTrue(AppUpdater.sameSigners(setOf("aa"), ours))
        assertFalse(AppUpdater.sameSigners(setOf("bb"), ours))
        assertFalse(AppUpdater.sameSigners(setOf("aa", "bb"), ours))
        // A signature Android couldn't read is refused, not waved through.
        assertFalse(AppUpdater.sameSigners(emptySet(), ours))
        assertFalse(AppUpdater.sameSigners(emptySet(), emptySet()))
    }

    @Test
    @Suppress("DEPRECATION")
    fun downloadsAreAskedForTheirSignersTheWayAndroid9And10Read() {
        // Without GET_SIGNATURES, Android 9 and 10 return no signers for a
        // downloaded APK, and every update would be refused there.
        assertTrue(AppUpdater.ARCHIVE_SIGNER_FLAGS and PackageManager.GET_SIGNATURES != 0)
        assertTrue(AppUpdater.ARCHIVE_SIGNER_FLAGS and PackageManager.GET_SIGNING_CERTIFICATES != 0)
    }

    @Test
    fun downloadsStopAtTheirLimit() {
        val data = ByteArray(200_000) { it.toByte() }
        val out = ByteArrayOutputStream()
        assertEquals(200_000L, AppUpdater.copyAtMost(ByteArrayInputStream(data), out, 200_000))
        assertArrayEquals(data, out.toByteArray())
        assertThrows(IOException::class.java) {
            AppUpdater.copyAtMost(ByteArrayInputStream(data), ByteArrayOutputStream(), 199_999)
        }
        assertArrayEquals("abc".toByteArray(), AppUpdater.readAtMost(ByteArrayInputStream("abc".toByteArray()), 3))
        assertThrows(IOException::class.java) {
            AppUpdater.readAtMost(ByteArrayInputStream("abcd".toByteArray()), 3)
        }
    }
}
