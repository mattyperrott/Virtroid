// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.app.Activity
import android.app.Application
import android.net.ConnectivityManager
import android.net.Network
import android.os.Bundle
import io.virtroid.shield.engine.EngineHost
import io.virtroid.shield.engine.HostCalls
import io.virtroid.shield.engine.WalletRequests
import io.virtroid.shield.store.SecureStore
import io.virtroid.shield.wallet.WalletConnect
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import org.json.JSONArray
import org.json.JSONObject

class ShieldApp : Application() {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    lateinit var store: SecureStore
        private set
    lateinit var walletConnect: WalletConnect
        private set

    /** A debug build's local test mode, if it's on; always null in a release build. */
    val localTest: LocalTest? by lazy { BuildHooks.localTest(this) }

    /** Where the engine's signatures and transactions go: the user's wallet over WalletConnect. */
    val wallet: WalletRequests get() = localTest?.wallet ?: walletConnect

    /** Started on first use: the WebView and the engine's 4 MB load only when the app is opened. */
    val engine: EngineHost by lazy {
        val test = localTest
        val calls = HostCalls(this, store, wallet, test?.cleartextHosts.orEmpty())
        if (test != null) EngineHost(this, calls, test.page) else EngineHost(this, calls)
    }
    val shield: Shield by lazy { Shield(this) }
    val operations: Operations by lazy { Operations(this) }

    /** The connected wallet's address, the account's owner. */
    fun ownerAddress(): String? = localTest?.owner ?: walletConnect.session.value?.addresses?.firstOrNull()

    override fun onCreate() {
        super.onCreate()
        store = SecureStore(this)
        walletConnect = WalletConnect(
            store = store,
            scope = scope,
            projectId = BuildConfig.WALLETCONNECT_PROJECT_ID,
            metadata = JSONObject()
                .put("name", getString(R.string.app_name))
                .put("description", getString(R.string.wallet_connect_description))
                .put("url", WALLET_CONNECT_URL)
                .put("icons", JSONArray()),
        )
        scope.launch { walletConnect.start() }
        wakeWalletConnect()
    }

    /**
     * WalletConnect's relay connection is cut while the app is in the
     * background (the user is in their wallet, approving); it reconnects the
     * moment a screen comes forward or the network comes back, so what the
     * wallet sent meanwhile arrives at once.
     */
    private fun wakeWalletConnect() {
        registerActivityLifecycleCallbacks(object : ActivityLifecycleCallbacks {
            override fun onActivityStarted(activity: Activity) = walletConnect.wake()
            override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) = Unit
            override fun onActivityResumed(activity: Activity) = Unit
            override fun onActivityPaused(activity: Activity) = Unit
            override fun onActivityStopped(activity: Activity) = Unit
            override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit
            override fun onActivityDestroyed(activity: Activity) = Unit
        })
        getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(
            object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) = walletConnect.wake()
            },
        )
    }

    companion object {
        /** Wallets show this as the app asking; it names the app's source, not a service. */
        const val WALLET_CONNECT_URL = "https://github.com/mattyperrott/Virtroid"
    }
}
