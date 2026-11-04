// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.watch

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.IBinder
import android.widget.Toast
import androidx.core.app.NotificationCompat
import io.virtroid.shield.AccountState
import io.virtroid.shield.ChainInfo
import io.virtroid.shield.Incoming
import io.virtroid.shield.R
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.ui.MainActivity
import io.virtroid.shield.widget.ShieldWidget
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.math.BigInteger
import java.util.concurrent.TimeUnit

/** What to look at, kept from the last time the engine ran. */
class WatchList(val address: String, val watch: List<String>, val rpcUrls: List<String>, val tokens: List<String>) {
    companion object {
        private const val KEY = "watch.list"

        fun save(app: ShieldApp, state: AccountState.Ready, chain: ChainInfo) {
            val json = JSONObject()
                .put("address", state.address)
                .put("watch", JSONArray(state.watch))
                .put("rpcUrls", JSONArray(chain.rpcUrls))
                .put("tokens", JSONArray(chain.assets.map { it.token }))
            app.store.set(KEY, json.toString())
        }

        fun read(app: ShieldApp): WatchList? = runCatching {
            val json = JSONObject(app.store.get(KEY) ?: return null)
            fun list(name: String) = json.getJSONArray(name).let { a -> (0 until a.length()).map { a.getString(it) } }
            WatchList(json.getString("address"), list("watch"), list("rpcUrls"), list("tokens"))
        }.getOrNull()
    }
}

/**
 * Watches the deposit addresses while the phone is on, so what arrives goes
 * into the pool without the user opening anything. A foreground service,
 * because Android cuts a background app's network. Looking at an address is a
 * plain RPC read from here; the engine, with its WebView and 4 MB of code, only
 * starts when there's something to do, or to look up.
 */
class WatchService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private var loop: Job? = null
    private val http = OkHttpClient.Builder().connectTimeout(20, TimeUnit.SECONDS).readTimeout(20, TimeUnit.SECONDS).followRedirects(false).build()
    private val app get() = application as ShieldApp

    /** Until when to look often: the user just copied the address, or opened the app. */
    private var eagerUntil = 0L
    /** What each address held when the engine last looked, so it isn't asked about the same funds again. */
    private val seen = HashMap<String, List<BigInteger>>()
    private var nextEngineLook = 0L
    private var olderCursor = 0
    private var rpcCursor = 0

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(
            NOTIFICATION_ID,
            notification(getString(R.string.watch_watching)),
            ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE,
        )
        eagerUntil = System.currentTimeMillis() + EAGER_MS
        if (intent?.action == ACTION_COPY) copyAddress()
        if (loop?.isActive != true) loop = scope.launch { run() }
        return START_STICKY
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    private fun copyAddress() {
        scope.launch {
            val address = ShieldWidget.address(this@WatchService) ?: return@launch
            getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText(getString(R.string.app_name), address))
            Toast.makeText(this@WatchService, R.string.copied, Toast.LENGTH_SHORT).show()
        }
    }

    private suspend fun run() {
        // The list the app last saved means the engine needn't start just to look.
        if (WatchList.read(app) == null) runCatching { app.shield.load() }
        while (true) {
            val list = WatchList.read(app)
            if (list == null) {
                stopSelf()
                return
            }
            look(list)
            delay(if (System.currentTimeMillis() < eagerUntil) EAGER_DELAY_MS else RELAXED_DELAY_MS)
        }
    }

    private suspend fun look(list: WatchList) {
        val older = list.watch.filter { it != list.address }
        val addresses = buildList {
            add(list.address)
            if (older.isNotEmpty()) add(older[olderCursor++ % older.size])
        }
        var changed = false
        for (address in addresses) {
            val holdings = runCatching { holdings(list.rpcUrls, address, list.tokens) }.getOrNull() ?: continue
            if (seen[address] != holdings) {
                if (seen.containsKey(address) || holdings.any { it.signum() > 0 }) changed = true
                seen[address] = holdings
            }
        }
        val now = System.currentTimeMillis()
        val waiting = ShieldWidget.hasWork(this)
        if (!changed && !(waiting && now >= nextEngineLook)) return
        // What's waiting (for screening, a lower fee, the gas service) is looked at again in a while.
        nextEngineLook = now + ENGINE_DELAY_MS
        runCatching {
            val shield = app.shield
            if (shield.state.value !is AccountState.Ready) shield.load()
            val incoming = shield.check()
            if (waiting) shield.refresh()
            showStatus(incoming)
        }
    }

    private fun showStatus(incoming: List<Incoming>) {
        val moving = incoming.firstOrNull { it.state == Incoming.State.SHIELDING }
        val text = if (moving != null) getString(R.string.watch_shielding) else getString(R.string.watch_watching)
        getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification(text))
    }

    /** ETH and each token at `address`, in one request, without the engine. */
    private suspend fun holdings(rpcs: List<String>, address: String, tokens: List<String>): List<BigInteger> = withContext(Dispatchers.IO) {
        val calls = JSONArray()
        tokens.forEachIndexed { i, token ->
            val call = if (token.equals(NATIVE, ignoreCase = true)) {
                JSONObject().put("method", "eth_getBalance").put("params", JSONArray().put(address).put("latest"))
            } else {
                val data = "0x70a08231" + address.removePrefix("0x").lowercase().padStart(64, '0')
                JSONObject().put("method", "eth_call").put("params", JSONArray().put(JSONObject().put("to", token).put("data", data)).put("latest"))
            }
            calls.put(call.put("jsonrpc", "2.0").put("id", i))
        }
        val url = rpcs[rpcCursor++ % rpcs.size]
        val request = Request.Builder().url(url).post(calls.toString().toRequestBody("application/json".toMediaType())).build()
        http.newCall(request).execute().use { response ->
            val answers = JSONArray(response.body!!.string())
            val byId = HashMap<Int, BigInteger>()
            for (i in 0 until answers.length()) {
                val answer = answers.getJSONObject(i)
                byId[answer.getInt("id")] = BigInteger(answer.getString("result").removePrefix("0x").ifEmpty { "0" }, 16)
            }
            tokens.indices.map { byId.getValue(it) }
        }
    }

    private fun notification(text: String) = NotificationCompat.Builder(this, channel())
        .setSmallIcon(R.drawable.widget_ic_mark)
        .setContentTitle(getString(R.string.app_name))
        .setContentText(text)
        .setOngoing(true)
        .setSilent(true)
        .setContentIntent(PendingIntent.getActivity(this, 0, MainActivity.intent(this), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
        .build()

    private fun channel(): String {
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            NotificationChannel(CHANNEL, getString(R.string.watch_channel), NotificationManager.IMPORTANCE_LOW),
        )
        return CHANNEL
    }

    /** Starts watching again when the phone starts, if there's a wallet to watch for. */
    class Boot : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action == Intent.ACTION_BOOT_COMPLETED && ShieldWidget.address(context) != null) start(context)
        }
    }

    companion object {
        const val ACTION_COPY = "io.virtroid.shield.COPY_ADDRESS"
        private const val CHANNEL = "watch"
        private const val NOTIFICATION_ID = 1
        private const val NATIVE = "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE"
        private const val EAGER_MS = 2 * 3_600_000L
        private const val EAGER_DELAY_MS = 20_000L
        private const val RELAXED_DELAY_MS = 120_000L
        private const val ENGINE_DELAY_MS = 120_000L

        fun start(context: Context) {
            // The system can refuse a start from the background; the next foreground one starts it.
            runCatching { context.startForegroundService(Intent(context, WatchService::class.java)) }
        }

        fun copyIntent(context: Context): Intent = Intent(context, WatchService::class.java).setAction(ACTION_COPY)
    }
}
