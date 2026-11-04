// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.app.Application
import io.virtroid.shield.engine.EngineHost
import io.virtroid.shield.engine.HostCalls
import io.virtroid.shield.store.SecureStore
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob

class ShieldApp : Application() {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    lateinit var store: SecureStore
        private set

    /** A debug build's local test mode, if it's on; always null in a release build. */
    val localTest: LocalTest? by lazy { BuildHooks.localTest(this) }

    /** Started on first use: the WebView and the engine's 4 MB load only when something needs them. */
    val engine: EngineHost by lazy {
        val test = localTest
        val calls = HostCalls(this, store, test?.cleartextHosts.orEmpty())
        if (test != null) EngineHost(this, calls, test.page) else EngineHost(this, calls)
    }
    val shield: Shield by lazy { Shield(this) }

    override fun onCreate() {
        super.onCreate()
        store = SecureStore(this)
    }
}
