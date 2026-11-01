// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.wallet

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong

/**
 * The WalletConnect relay connection (specs: servers/relay/relay-server-rpc):
 * irn_subscribe, irn_publish and wc_proposeSession out, irn_subscription in.
 * It reconnects with backoff and resubscribes; the relay keeps messages for a
 * topic while the client is away, which irn_fetchMessages collects.
 */
class WcRelay(
    private val scope: CoroutineScope,
    private val projectId: String,
    private val identity: WcCrypto.Ed25519Identity,
    private val onMessage: (topic: String, message: String, tag: Int) -> Unit,
) {
    private val http = OkHttpClient.Builder().pingInterval(30, TimeUnit.SECONDS).build()
    private val ids = AtomicLong(System.currentTimeMillis() * 1000)
    private val waiting = ConcurrentHashMap<Long, CompletableDeferred<JSONObject>>()
    private val topics = ConcurrentHashMap.newKeySet<String>()
    @Volatile private var socket: WebSocket? = null
    @Volatile private var open = CompletableDeferred<Unit>()
    @Volatile private var closed = false
    @Volatile private var opened = false
    @Volatile private var connecting = false
    @Volatile private var retry: Job? = null
    private var backoffMs = 1_000L

    fun connect() {
        closed = false
        connecting = true
        val jwt = WcCrypto.relayJwt(identity, RELAY_URL, System.currentTimeMillis() / 1000)
        val ua = "wc-2/kotlin-2.0.0/android-${android.os.Build.VERSION.RELEASE}"
        val url = "$RELAY_URL/?auth=$jwt&projectId=$projectId&ua=$ua"
        socket = http.newWebSocket(Request.Builder().url(url).build(), listener)
    }

    fun close() {
        closed = true
        socket?.close(1000, null)
    }

    /**
     * Reconnects now instead of at the end of the backoff: Android cuts a
     * background app's network, so the retries back off to a minute while
     * the user is in their wallet approving, and the approval should show the
     * moment they come back (a screen starts, or the network returns).
     */
    fun wake() {
        if (closed || connecting || open.isCompleted) return
        retry?.cancel()
        backoffMs = 1_000L
        connect()
    }

    suspend fun subscribe(topic: String) {
        topics += topic
        call("irn_subscribe", JSONObject().put("topic", topic))
        fetchPending(topic)
    }

    /**
     * The relay pushes only what's published after a subscription; anything
     * the peer sent before (a wallet settles a session the moment it
     * approves) waits in the topic's mailbox for irn_fetchMessages.
     */
    private suspend fun fetchPending(topic: String) {
        repeat(MAX_FETCH_PAGES) {
            val result = call("irn_fetchMessages", JSONObject().put("topic", topic)).optJSONObject("result") ?: return
            val messages = result.optJSONArray("messages") ?: return
            for (i in 0 until messages.length()) {
                val m = messages.getJSONObject(i)
                onMessage(m.getString("topic"), m.getString("message"), m.optInt("tag"))
            }
            if (!result.optBoolean("hasMore")) return
        }
    }

    suspend fun unsubscribe(topic: String) {
        topics -= topic
        // The subscription id isn't kept; the relay drops it with the topic's expiry anyway.
    }

    /**
     * Sends a session proposal the way WalletConnect's own SDK does (checked
     * against sign-client 2.21.8): through the relay's wc_proposeSession, not
     * a plain publish. The wallet answers with wc_approveSession, which the
     * relay turns into the proposal response on the pairing topic and
     * wc_sessionSettle on the new session topic (fetched after subscribing).
     */
    suspend fun proposeSession(pairingTopic: String, sealedProposal: String, ttlSeconds: Int, correlationId: Long) {
        call(
            "wc_proposeSession",
            JSONObject()
                .put("pairingTopic", pairingTopic)
                .put("sessionProposal", sealedProposal)
                .put("ttl", ttlSeconds)
                .put("correlationId", correlationId),
        )
    }

    suspend fun publish(topic: String, message: String, tag: Int, ttlSeconds: Int) {
        call(
            "irn_publish",
            JSONObject().put("topic", topic).put("message", message).put("ttl", ttlSeconds).put("tag", tag).put("prompt", false),
        )
    }

    private suspend fun call(method: String, params: JSONObject): JSONObject {
        withTimeout(OPEN_TIMEOUT_MS) { open.await() }
        val id = ids.incrementAndGet()
        val answer = CompletableDeferred<JSONObject>()
        waiting[id] = answer
        trace("relay -> $method ${params.optString("topic").ifEmpty { params.optString("pairingTopic") }.take(12)} tag=${params.optInt("tag")}")
        val sent = socket?.send(JSONObject().put("id", id).put("jsonrpc", "2.0").put("method", method).put("params", params).toString()) ?: false
        if (!sent) {
            waiting.remove(id)
            throw IllegalStateException("The WalletConnect relay isn't connected")
        }
        val response = withTimeout(CALL_TIMEOUT_MS) { answer.await() }
        response.optJSONObject("error")?.let { throw IllegalStateException("Relay $method failed: ${it.optString("message")}") }
        return response
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            connecting = false
            backoffMs = 1_000L
            val reconnected = opened
            opened = true
            open.complete(Unit)
            // On the first connection the subscribe() calls waiting for it do
            // their own subscribing; only a reconnection has topics to restore.
            if (!reconnected) return
            scope.launch {
                for (topic in topics.toList()) {
                    runCatching {
                        call("irn_subscribe", JSONObject().put("topic", topic))
                        fetchPending(topic)
                    }
                }
            }
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            trace("relay <- ${text.take(300)}")
            val message = runCatching { JSONObject(text) }.getOrNull() ?: return
            if (message.optString("method") == "irn_subscription") {
                val data = message.getJSONObject("params").getJSONObject("data")
                webSocket.send(JSONObject().put("id", message.get("id")).put("jsonrpc", "2.0").put("result", true).toString())
                onMessage(data.getString("topic"), data.getString("message"), data.optInt("tag"))
            } else if (message.has("id")) {
                waiting.remove(message.optLong("id"))?.complete(message)
            }
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            trace("relay failure ${response?.code} ${t.message}")
            reconnect()
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = reconnect()
    }

    private fun reconnect() {
        connecting = false
        open = CompletableDeferred()
        if (closed) return
        val wait = backoffMs
        backoffMs = (backoffMs * 2).coerceAtMost(60_000L)
        retry = scope.launch {
            delay(wait)
            if (!closed && !connecting) connect()
        }
    }

    /** Debug builds only: the relay traffic's shape (no plaintext; messages are sealed). */
    private fun trace(line: String) {
        if (io.virtroid.shield.BuildConfig.DEBUG) android.util.Log.d("ShieldWC", line)
    }

    companion object {
        const val RELAY_URL = "wss://relay.walletconnect.org"
        private const val OPEN_TIMEOUT_MS = 20_000L
        private const val CALL_TIMEOUT_MS = 20_000L
        private const val MAX_FETCH_PAGES = 20
    }
}
