package io.virtroid.client.security

import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class BackgroundKeyTest {
    private fun bytes() = byteArrayOf(1, 2, 3, 4)
    private val wiped = ByteArray(4)

    @Test
    fun aMatchKeepsTheKey() {
        val derived = bytes()
        val key = BackgroundKey { derived }
        assertTrue(BackgroundKey.checkAlongside(key) { true })
        assertArrayEquals(bytes(), key.take())
    }

    @Test
    fun aMismatchWipesIt() {
        val derived = bytes()
        assertFalse(BackgroundKey.checkAlongside(BackgroundKey { derived }) { false })
        assertArrayEquals(wiped, derived)
    }

    @Test
    fun aMismatchWaitsForAKeyStillBeingDerivedAndWipesIt() {
        val derived = bytes()
        val checked = CountDownLatch(1)
        val key = BackgroundKey {
            checked.await(5, TimeUnit.SECONDS)
            derived
        }
        assertFalse(BackgroundKey.checkAlongside(key) { checked.countDown(); false })
        assertArrayEquals(wiped, derived)
    }

    @Test
    fun aCheckThatThrowsWipesIt() {
        val derived = bytes()
        assertThrows(IllegalStateException::class.java) {
            BackgroundKey.checkAlongside(BackgroundKey { derived }) { error("no provider") }
        }
        assertArrayEquals(wiped, derived)
    }

    @Test
    fun aFailedDerivationIsNoKeyNotAnError() {
        val key = BackgroundKey { error("out of memory") }
        assertTrue(BackgroundKey.checkAlongside(key) { true })
        assertNull(key.take())
    }

    @Test
    fun noKeyToDeriveIsFine() {
        assertTrue(BackgroundKey.checkAlongside(null) { true })
        assertFalse(BackgroundKey.checkAlongside(null) { false })
    }

    @Test
    fun theCheckRunsWhileTheKeyIsDerived() {
        val started = CountDownLatch(1)
        val checked = CountDownLatch(1)
        val key = BackgroundKey {
            started.countDown()
            if (checked.await(5, TimeUnit.SECONDS)) bytes() else null
        }
        assertTrue(
            BackgroundKey.checkAlongside(key) {
                started.await(5, TimeUnit.SECONDS).also { checked.countDown() }
            },
        )
        assertArrayEquals(bytes(), key.take())
    }
}
