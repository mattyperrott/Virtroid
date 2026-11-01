// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.Context

/** Release builds have no test mode: the engine's own chains and WalletConnect, nothing else. */
object BuildHooks {
    fun localTest(context: Context): LocalTest? = null
}
