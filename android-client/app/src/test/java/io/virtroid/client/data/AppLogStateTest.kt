package io.virtroid.client.data

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class AppLogStateTest {
    private val info = entry("info", AppLogLevel.INFO)
    private val warning = entry("warning", AppLogLevel.WARN)
    private val error = entry("error", AppLogLevel.ERROR)
    private val critical = entry("critical", AppLogLevel.CRITICAL)

    @Test
    fun allFilterIsTheDefaultCompleteProjection() {
        val entries = listOf(info, warning, error, critical)

        assertEquals(entries, AppLogState.visibleEntries(entries, AppLogFilter.ALL))
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
            AppLogState.unresolvedCriticalCount(listOf(info, warning, error, critical, resolvedError)),
        )
    }

    @Test
    fun appendRetainsOnlyTheConfiguredTail() {
        val entries = AppLogState.append(listOf(info, warning, error), critical, maxEntries = 3)

        assertEquals(listOf(warning, error, critical), entries)
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
