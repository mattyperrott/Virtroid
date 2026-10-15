package io.virtroid.client.update

import org.junit.Assert.assertTrue
import org.junit.Test

class AppUpdaterTest {
    @Test
    fun versionsCompareNumerically() {
        assertTrue(AppUpdater.compareVersions("0.4.10", "0.4.9") > 0)
        assertTrue(AppUpdater.compareVersions("0.5", "0.4.9") > 0)
        assertTrue(AppUpdater.compareVersions("0.4.4", "0.4.4") == 0)
        assertTrue(AppUpdater.compareVersions("0.4.3", "0.4.4") < 0)
        assertTrue(AppUpdater.compareVersions("1.0.0", "0.9.99") > 0)
    }
}
