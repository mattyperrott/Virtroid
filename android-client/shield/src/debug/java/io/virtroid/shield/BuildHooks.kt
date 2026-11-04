// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.Context
import java.io.File

/**
 * Debug builds' local test mode, on when the test writes the harness's URL
 * (engine/test/harness/server.mjs, reached at 10.0.2.2 from an emulator) into
 * the app's files:
 *   adb shell run-as io.virtroid.shield sh -c 'echo http://10.0.2.2:8099 > files/harness-url'
 */
object BuildHooks {
    fun localTest(context: Context): LocalTest? {
        if (!File(context.filesDir, "harness-url").isFile) return null
        return LocalTest(page = "engine/index-local.html", cleartextHosts = setOf("10.0.2.2"))
    }
}
