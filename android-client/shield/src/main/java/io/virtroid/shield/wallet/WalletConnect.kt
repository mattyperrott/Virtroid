// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.wallet

import io.virtroid.shield.engine.EngineException
import io.virtroid.shield.engine.WalletRequests
import io.virtroid.shield.store.SecureStore
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.ConcurrentHashMap
import kotlin.random.Random

/** A wallet the user connected: which accounts it shares and how to bring it forward. */
data class WcSession(
    val topic: String,
    val symKey: ByteArray,
    val accounts: List<String>,
    val walletName: String,
    /** The wallet app's own link (its metadata's redirect.native), to switch to it for each prompt. */
    val walletLink: String?,
    val expiry: Long,
) {
    /** The EVM addresses shared, without their eip155:<chain>: prefix, in the wallet's order. */
    val addresses: List<String> get() = accounts.map { it.substringAfterLast(':') }.distinct()

    fun toJson(): JSONObject = JSONObject()
        .put("topic", topic)
        .put("symKey", WcCrypto.hex(symKey))
        .put("accounts", JSONArray(accounts))
        .put("walletName", walletName)
        .put("walletLink", walletLink ?: JSONObject.NULL)
        .put("expiry", expiry)

    companion object {
        fun fromJson(o: JSONObject) = WcSession(
            topic = o.getString("topic"),
            symKey = WcCrypto.unhex(o.getString("symKey")),
            accounts = o.getJSONArray("accounts").let { a -> (0 until a.length()).map(a::getString) },
            walletName = o.optString("walletName"),
            walletLink = o.optString("walletLink").takeIf { it.isNotEmpty() && it != "null" },
            expiry = o.getLong("expiry"),
        )
    }
}

/**
 * The app's side of WalletConnect v2's Sign protocol (specs: clients/sign,
 * clients/core/pairing): it proposes a session over a fresh pairing, the
 * user approves it in their wallet, and from then on the engine's signature
 * and transaction requests go to that wallet as wc_sessionRequest. Written
 * from the public specs because Reown's own SDK is under a licence a GPL app
 * can't take in.
 */
class WalletConnect(
    private val store: SecureStore,
    private val scope: CoroutineScope,
    private val projectId: String,
    private val metadata: JSONObject,
) : WalletRequests {
    private val keys = ConcurrentHashMap<String, ByteArray>()
    private val waiting = ConcurrentHashMap<Long, CompletableDeferred<JSONObject>>()
    private val settled = ConcurrentHashMap<String, CompletableDeferred<WcSession>>()
    private val _session = MutableStateFlow<WcSession?>(null)
    private val _prompts = MutableSharedFlow<WcSession>(extraBufferCapacity = 8)
    private var relay: WcRelay? = null

    val session: StateFlow<WcSession?> = _session

    /** Each request the wallet has to approve: the screens switch to the wallet app. */
    val prompts: SharedFlow<WcSession> = _prompts

    val configured: Boolean get() = projectId.isNotBlank()

    suspend fun start() = withContext(Dispatchers.IO) {
        if (relay != null || !configured) return@withContext
        val seed = store.get(IDENTITY)?.let(WcCrypto::unhex) ?: WcCrypto.randomBytes(32).also { store.set(IDENTITY, WcCrypto.hex(it)) }
        relay = WcRelay(scope, projectId, WcCrypto.Ed25519Identity(seed), ::onRelayMessage).also { it.connect() }
        store.get(SESSION)?.let { WcSession.fromJson(JSONObject(it)) }?.takeIf { it.expiry > nowSeconds() }?.let { saved ->
            keys[saved.topic] = saved.symKey
            relay!!.subscribe(saved.topic)
            _session.value = saved
        }
    }

    /** A pairing to show as a QR code or hand to a wallet on this device as a wc: link. */
    class Pairing(val uri: String, val approved: CompletableDeferred<WcSession>)

    suspend fun pair(chainIds: List<Int>): Pairing {
        val relay = requireNotNull(relay) { "WalletConnect isn't set up" }
        val pairingKey = WcCrypto.randomBytes(32)
        val pairingTopic = WcCrypto.topicOf(pairingKey)
        keys[pairingTopic] = pairingKey
        relay.subscribe(pairingTopic)

        val self = WcCrypto.x25519KeyPair()
        val expiry = nowSeconds() + PAIRING_TTL
        val chains = JSONArray(chainIds.map { "eip155:$it" })
        val proposal = JSONObject()
            .put("relays", JSONArray().put(JSONObject().put("protocol", "irn")))
            .put("proposer", JSONObject().put("publicKey", WcCrypto.hex(self.publicKey)).put("metadata", metadata))
            .put("requiredNamespaces", JSONObject())
            .put(
                "optionalNamespaces",
                JSONObject().put(
                    "eip155",
                    JSONObject().put("chains", chains).put("methods", JSONArray(METHODS)).put("events", JSONArray(EVENTS)),
                ),
            )
            .put("expiryTimestamp", expiry)

        val approved = CompletableDeferred<WcSession>()
        val id = newId()
        val response = CompletableDeferred<JSONObject>().also { waiting[id] = it }
        publish(pairingTopic, request(id, "wc_sessionPropose", proposal), TAG_PROPOSE, 300)

        scope.launch {
            try {
                val answer = response.await()
                answer.optJSONObject("error")?.let { throw EngineException("wallet_rejected", it.optString("message", "The wallet declined")) }
                val result = answer.getJSONObject("result")
                val symKey = WcCrypto.deriveSymKey(self.privateKey, WcCrypto.unhex(result.getString("responderPublicKey")))
                val topic = WcCrypto.topicOf(symKey)
                keys[topic] = symKey
                val settle = CompletableDeferred<WcSession>().also { settled[topic] = it }
                relay.subscribe(topic)
                val session = withTimeout(SETTLE_TIMEOUT_MS) { settle.await() }
                store.set(SESSION, session.toJson().toString())
                _session.value?.let { old -> if (old.topic != session.topic) runCatching { deleteSession(old) } }
                _session.value = session
                approved.complete(session)
            } catch (error: Throwable) {
                approved.completeExceptionally(error)
            } finally {
                keys.remove(pairingTopic)
                runCatching { relay.unsubscribe(pairingTopic) }
            }
        }

        val uri = "wc:$pairingTopic@2?symKey=${WcCrypto.hex(pairingKey)}&relay-protocol=irn&expiryTimestamp=$expiry"
        return Pairing(uri, approved)
    }

    /** The engine's wallet request, sent to the connected wallet; returns its result. */
    override suspend fun request(chainId: Int, method: String, params: JSONArray): Any? {
        val session = _session.value ?: throw EngineException("no_wallet", "Connect a wallet first")
        val id = newId()
        val answer = CompletableDeferred<JSONObject>().also { waiting[id] = it }
        val expiry = nowSeconds() + REQUEST_TTL
        val payload = JSONObject()
            .put("request", JSONObject().put("method", method).put("params", params).put("expiry", expiry))
            .put("chainId", "eip155:$chainId")
        publish(session.topic, request(id, "wc_sessionRequest", payload), TAG_REQUEST, REQUEST_TTL.toInt())
        _prompts.tryEmit(session)
        val response = withTimeout(REQUEST_TTL * 1000) { answer.await() }
        response.optJSONObject("error")?.let { error ->
            val code = error.optInt("code")
            val rejected = code == 4001 || code in 5000..5999
            throw EngineException(if (rejected) "wallet_rejected" else "wallet_error", error.optString("message", "The wallet refused"))
        }
        return response.opt("result")
    }

    suspend fun disconnect() {
        val session = _session.value ?: return
        _session.value = null
        withContext(Dispatchers.IO) { store.delete(SESSION) }
        runCatching { deleteSession(session) }
    }

    private suspend fun deleteSession(session: WcSession) {
        val params = JSONObject().put("code", 6000).put("message", "User disconnected")
        publish(session.topic, request(newId(), "wc_sessionDelete", params), TAG_DELETE, 86_400)
        keys.remove(session.topic)
    }

    private fun onRelayMessage(topic: String, message: String, tag: Int) {
        val key = keys[topic] ?: return
        val payload = runCatching { JSONObject(WcCrypto.open(key, message)) }.getOrNull() ?: return
        if (payload.has("method")) {
            scope.launch { onWalletRequest(topic, payload) }
        } else {
            waiting.remove(payload.optLong("id"))?.complete(payload)
        }
    }

    private suspend fun onWalletRequest(topic: String, payload: JSONObject) {
        val id = payload.getLong("id")
        val params = payload.optJSONObject("params") ?: JSONObject()
        when (payload.getString("method")) {
            "wc_sessionSettle" -> {
                val namespaces = params.getJSONObject("namespaces")
                val accounts = mutableListOf<String>()
                for (name in namespaces.keys()) {
                    val list = namespaces.getJSONObject(name).optJSONArray("accounts") ?: continue
                    for (i in 0 until list.length()) accounts += list.getString(i)
                }
                val wallet = params.optJSONObject("controller")?.optJSONObject("metadata")
                val session = WcSession(
                    topic = topic,
                    symKey = keys.getValue(topic),
                    accounts = accounts,
                    walletName = wallet?.optString("name").orEmpty(),
                    walletLink = wallet?.optJSONObject("redirect")?.optString("native")?.takeIf { it.isNotEmpty() },
                    expiry = params.optLong("expiry", nowSeconds() + 7 * 86_400),
                )
                respond(topic, id, true, TAG_SETTLE + 1)
                settled.remove(topic)?.complete(session)
            }
            "wc_sessionPing" -> respond(topic, id, true, TAG_PING + 1)
            "wc_sessionExtend" -> {
                respond(topic, id, true, TAG_EXTEND + 1)
                updateSession(topic) { it.copy(expiry = params.optLong("expiry", it.expiry)) }
            }
            "wc_sessionUpdate" -> {
                respond(topic, id, true, TAG_UPDATE + 1)
                val accounts = params.optJSONObject("namespaces")?.optJSONObject("eip155")?.optJSONArray("accounts")
                if (accounts != null) updateSession(topic) { it.copy(accounts = (0 until accounts.length()).map(accounts::getString)) }
            }
            "wc_sessionEvent" -> {
                respond(topic, id, true, TAG_EVENT + 1)
                val event = params.optJSONObject("event")
                if (event?.optString("name") == "accountsChanged") {
                    val data = event.optJSONArray("data")
                    if (data != null) updateSession(topic) { s -> s.copy(accounts = (0 until data.length()).map { "eip155:0:${data.getString(it)}" }) }
                }
            }
            "wc_sessionDelete" -> {
                respond(topic, id, true, TAG_DELETE + 1)
                if (_session.value?.topic == topic) {
                    _session.value = null
                    withContext(Dispatchers.IO) { store.delete(SESSION) }
                }
                keys.remove(topic)
            }
            else -> respondError(topic, id, 10001, "Unsupported method")
        }
    }

    private suspend fun updateSession(topic: String, change: (WcSession) -> WcSession) {
        val current = _session.value?.takeIf { it.topic == topic } ?: return
        val updated = change(current)
        _session.value = updated
        withContext(Dispatchers.IO) { store.set(SESSION, updated.toJson().toString()) }
    }

    private suspend fun respond(topic: String, id: Long, result: Any, tag: Int) {
        publish(topic, JSONObject().put("id", id).put("jsonrpc", "2.0").put("result", result), tag, if (tag in LONG_TTL_TAGS) 86_400 else 300)
    }

    private suspend fun respondError(topic: String, id: Long, code: Int, message: String) {
        val error = JSONObject().put("code", code).put("message", message)
        publish(topic, JSONObject().put("id", id).put("jsonrpc", "2.0").put("error", error), TAG_REQUEST + 1, 300)
    }

    private suspend fun publish(topic: String, payload: JSONObject, tag: Int, ttl: Int) {
        val key = keys[topic] ?: throw IllegalStateException("No key for topic")
        requireNotNull(relay).publish(topic, WcCrypto.seal(key, payload.toString()), tag, ttl)
    }

    private fun request(id: Long, method: String, params: JSONObject) =
        JSONObject().put("id", id).put("jsonrpc", "2.0").put("method", method).put("params", params)

    // 13-digit milliseconds then 3 random digits, as WalletConnect's own ids.
    private fun newId(): Long = System.currentTimeMillis() * 1000 + Random.nextInt(1000)

    private fun nowSeconds() = System.currentTimeMillis() / 1000

    private companion object {
        const val IDENTITY = "wc.identity"
        const val SESSION = "wc.session"
        const val PAIRING_TTL = 300L
        const val REQUEST_TTL = 3_600L
        const val SETTLE_TIMEOUT_MS = 300_000L
        // Tags from the specs' method tables; each response is its request's tag + 1.
        const val TAG_PROPOSE = 1100
        const val TAG_SETTLE = 1102
        const val TAG_UPDATE = 1104
        const val TAG_EXTEND = 1106
        const val TAG_REQUEST = 1108
        const val TAG_EVENT = 1110
        const val TAG_DELETE = 1112
        const val TAG_PING = 1114
        val LONG_TTL_TAGS = setOf(TAG_UPDATE + 1, TAG_EXTEND + 1, TAG_DELETE, TAG_DELETE + 1)
        val METHODS = listOf("eth_sendTransaction", "eth_signTypedData_v4", "personal_sign", "wallet_switchEthereumChain")
        val EVENTS = listOf("chainChanged", "accountsChanged")
    }
}
