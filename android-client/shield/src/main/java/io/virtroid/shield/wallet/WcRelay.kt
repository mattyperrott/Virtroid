// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.wallet

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
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
 * irn_subscribe and irn_publish out, irn_subscription in. It reconnects with
 * backoff and resubscribes; the relay keeps messages for a topic while the
 * client is away and delivers them on resubscribe.
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
    private var backoffMs = 1_000L

    fun connect() {
        closed = false
        val jwt = WcCrypto.relayJwt(identity, RELAY_URL, System.currentTimeMillis() / 1000)
        val ua = "wc-2/kotlin-2.0.0/android-${android.os.Build.VERSION.RELEASE}"
        val url = "$RELAY_URL/?auth=$jwt&projectId=$projectId&ua=$ua"
        socket = http.newWebSocket(Request.Builder().url(url).build(), listener)
    }

    fun close() {
        closed = true
        socket?.close(1000, null)
    }

    suspend fun subscribe(topic: String) {
        topics += topic
        call("irn_subscribe", JSONObject().put("topic", topic))
    }

    suspend fun unsubscribe(topic: String) {
        topics -= topic
        // The subscription id isn't kept; the relay drops it with the topic's expiry anyway.
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
            backoffMs = 1_000L
            open.complete(Unit)
            scope.launch {
                for (topic in topics.toList()) runCatching { call("irn_subscribe", JSONObject().put("topic", topic)) }
            }
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val message = runCatching { JSONObject(text) }.getOrNull() ?: return
            if (message.optString("method") == "irn_subscription") {
                val data = message.getJSONObject("params").getJSONObject("data")
                webSocket.send(JSONObject().put("id", message.get("id")).put("jsonrpc", "2.0").put("result", true).toString())
                onMessage(data.getString("topic"), data.getString("message"), data.optInt("tag"))
            } else if (message.has("id")) {
                waiting.remove(message.optLong("id"))?.complete(message)
            }
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = reconnect()

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = reconnect()
    }

    private fun reconnect() {
        open = CompletableDeferred()
        if (closed) return
        val wait = backoffMs
        backoffMs = (backoffMs * 2).coerceAtMost(60_000L)
        scope.launch {
            delay(wait)
            if (!closed) connect()
        }
    }

    companion object {
        const val RELAY_URL = "wss://relay.walletconnect.org"
        private const val OPEN_TIMEOUT_MS = 20_000L
        private const val CALL_TIMEOUT_MS = 20_000L
    }
}
