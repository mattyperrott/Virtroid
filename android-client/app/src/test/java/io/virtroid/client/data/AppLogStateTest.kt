package io.virtroid.client.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class AppLogStateTest {
    private val info = entry("info", AppLogLevel.INFO)
    private val security = entry("security", AppLogLevel.SECURITY)
    private val warning = entry("warning", AppLogLevel.WARN)
    private val error = entry("error", AppLogLevel.ERROR)
    private val critical = entry("critical", AppLogLevel.CRITICAL)

    @Test
    fun allFilterIsTheDefaultCompleteProjection() {
        val entries = listOf(info, security, warning, error, critical)

        assertEquals(entries, AppLogState.visibleEntries(entries, AppLogFilter.ALL))
        assertEquals(listOf(security), AppLogState.visibleEntries(entries, AppLogFilter.SECURITY))
        assertEquals(listOf(error, critical), AppLogState.visibleEntries(entries, AppLogFilter.ERRORS))
        assertEquals(listOf(warning), AppLogState.visibleEntries(entries, AppLogFilter.WARN))
    }

    @Test
    fun clearRemovesEveryEntryAndResetsNotificationCount() {
        val cleared = AppLogState.clear()

        assertTrue(cleared.isEmpty())
        assertEquals(0, AppLogState.unresolvedCriticalCount(cleared))
    }

    @Test
    fun notificationCountIncludesOnlyUnresolvedErrorsAndCriticalEntries() {
        val resolvedError = error.copy(id = "resolved", criticalResolved = true)

        assertEquals(
            2,
            AppLogState.unresolvedCriticalCount(listOf(info, security, warning, error, critical, resolvedError)),
        )
    }

    @Test
    fun appendRetainsOnlyTheConfiguredTail() {
        val entries = AppLogState.append(listOf(info, warning, error), critical, maxEntries = 3)

        assertEquals(listOf(warning, error, critical), entries)
    }

    @Test
    fun appendCoalescedSuppressesEquivalentRecentSecurityNotices() {
        val first = critical.copy(timestampMs = 1_000L, source = "security", message = "shell alert")
        val duplicate = first.copy(id = "duplicate", timestampMs = 30_000L)
        val entries = listOf(first)

        val result = AppLogState.appendCoalesced(entries, duplicate, maxEntries = 200, windowMs = 60_000L)

        assertTrue(result === entries)
    }

    @Test
    fun appendCoalescedKeepsEquivalentNoticesOutsideWindow() {
        val first = critical.copy(timestampMs = 1_000L, source = "security", message = "shell alert")
        val later = first.copy(id = "later", timestampMs = 61_000L)

        val result = AppLogState.appendCoalesced(listOf(first), later, maxEntries = 200, windowMs = 60_000L)

        assertEquals(listOf(first, later), result)
    }

    private fun entry(id: String, level: AppLogLevel): AppLogEntry {
        return AppLogEntry(
            id = id,
            timestampMs = 1L,
            level = level,
            source = "test",
            message = id,
            criticalResolved = false,
        )
    }
}
