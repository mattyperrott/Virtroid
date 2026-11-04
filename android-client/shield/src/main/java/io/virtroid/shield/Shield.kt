// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import io.virtroid.shield.engine.EngineEvent
import io.virtroid.shield.engine.EngineException
import io.virtroid.shield.watch.WatchList
import io.virtroid.shield.widget.ShieldWidget
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import org.json.JSONArray
import org.json.JSONObject
import java.math.BigDecimal
import java.math.BigInteger

data class AssetInfo(val symbol: String, val token: String, val decimals: Int)

data class ChainInfo(val chainId: Int, val name: String, val explorerUrl: String, val rpcUrls: List<String>, val assets: List<AssetInfo>)

data class Deposit(val commitment: String, val value: BigInteger, val label: String?)

data class Balance(
    val symbol: String,
    val decimals: Int,
    val spendable: BigInteger,
    val pending: BigInteger,
    val waiting: List<Deposit>,
    val declined: List<Deposit>,
)

/** Funds at a deposit address, on their way in or waiting to be (engine.ts `Incoming`). */
data class Incoming(
    val symbol: String,
    val decimals: Int,
    val amount: BigInteger,
    val state: State,
    val minimum: BigInteger?,
    val fee: BigInteger?,
    val txHash: String?,
) {
    enum class State { BELOW_MINIMUM, FEE_HIGH, GAS_UNAVAILABLE, SHIELDING }
}

/** A send the relayer priced; [id] is what sends it. It goes as [parts] withdrawals, one per deposit. */
data class Quote(val id: Long, val symbol: String, val receive: BigInteger, val fee: BigInteger, val parts: Int, val recent: Boolean)

data class ExitPlan(val owner: String, val needed: BigInteger, val balance: BigInteger)

/** Where the wallet stands, for the screens and the widget. */
sealed interface AccountState {
    data object Loading : AccountState
    data object NoWallet : AccountState
    data class Ready(
        val owner: String,
        /** The deposit address to show. */
        val address: String,
        /** Every address that's been shown. */
        val watch: List<String>,
        val balances: List<Balance>,
        val incoming: List<Incoming>,
    ) : AccountState
}

/**
 * The app's operations, over the engine: one wallet, on one chain. The screens
 * and the watcher call these and show [state]; everything about notes, proofs
 * and the relayer stays in the engine. One thing at a time goes to the engine,
 * as they all read and write the same notes.
 */
class Shield(private val app: ShieldApp) {
    private val _state = MutableStateFlow<AccountState>(AccountState.Loading)
    val state: StateFlow<AccountState> = _state
    private val lock = Mutex()
    private var chainCache: ChainInfo? = null

    /** The send or exit going on, run in the app's scope so it outlives the screen that started it. */
    sealed interface Work {
        data class Sending(val symbol: String, val stage: String, val part: Int, val parts: Int, val txHash: String?) : Work
        data class Sent(val txHashes: List<String>) : Work
        data class Failed(val code: String, val message: String) : Work
    }

    private val _work = MutableStateFlow<Work?>(null)
    val work: StateFlow<Work?> = _work

    suspend fun chain(): ChainInfo = chainCache ?: (app.engine.request("info") as JSONObject).let { c ->
        val assets = c.getJSONArray("assets")
        val rpcs = c.getJSONArray("rpcUrls")
        ChainInfo(
            chainId = c.getInt("chainId"),
            name = c.getString("name"),
            explorerUrl = c.getString("explorerUrl"),
            rpcUrls = (0 until rpcs.length()).map { rpcs.getString(it) },
            assets = (0 until assets.length()).map { i ->
                val a = assets.getJSONObject(i)
                AssetInfo(a.getString("symbol"), a.getString("token"), a.getInt("decimals"))
            },
        )
    }.also {
        chainCache = it
        saveForWidget()
    }

    /** What the wallet looks like on start: the store's answer first, then the chain's. */
    suspend fun load() {
        val status = lock.withLock { app.engine.request("status") as JSONObject }
        if (!status.getBoolean("ready")) {
            _state.value = AccountState.NoWallet
            ShieldWidget.clear(app)
            return
        }
        chain()
        ready(status, keepBalances = true)
        refresh()
    }

    /** Makes the wallet and returns its 12 words, which the user sees here and writes down. */
    suspend fun create(): String {
        chain()
        val made = lock.withLock { app.engine.request("create") as JSONObject }
        ready(made, keepBalances = false)
        return made.getString("words")
    }

    suspend fun restore(words: String) {
        chain()
        val restored = lock.withLock { app.engine.request("restore", JSONObject().put("words", words)) as JSONObject }
        ready(restored, keepBalances = false)
        refresh()
    }

    suspend fun words(): String = lock.withLock { (app.engine.request("words") as JSONObject).getString("words") }

    /** Reads what's in the pool. */
    suspend fun refresh() {
        val list = lock.withLock { app.engine.request("sync") as JSONArray }
        val balances = (0 until list.length()).map { parseBalance(list.getJSONObject(it)) }
        _state.update { (it as? AccountState.Ready)?.copy(balances = balances) ?: it }
        saveForWidget()
    }

    /**
     * Looks at the deposit addresses and moves what can go into the pool.
     * `force` names assets the user has said to shield despite the fee.
     * Returns what's waiting at them, or being moved.
     */
    suspend fun check(force: List<String> = emptyList()): List<Incoming> {
        val result = lock.withLock {
            val params = JSONObject().put("force", JSONArray(force))
            app.engine.request("check", params) as JSONObject
        }
        val incoming = result.getJSONArray("incoming").let { list -> (0 until list.length()).map { parseIncoming(list.getJSONObject(it)) } }
        _state.update { state -> (state as? AccountState.Ready)?.copy(address = result.getString("address"), watch = strings(result.getJSONArray("watch")), incoming = incoming) ?: state }
        saveForWidget()
        if (result.getBoolean("moved")) refresh()
        return incoming
    }

    suspend fun quoteOut(symbol: String, amount: BigInteger?, recipient: String): Quote {
        val params = JSONObject().put("symbol", symbol).put("amount", amount?.toString() ?: "max").put("recipient", recipient)
        val q = lock.withLock { app.engine.request("quoteOut", params) as JSONObject }
        return Quote(q.getLong("id"), symbol, BigInteger(q.getString("receive")), BigInteger(q.getString("fee")), q.getInt("parts"), q.getBoolean("recent"))
    }

    /** Sends the accepted quote; [work] follows it, and closing the screen doesn't stop it. */
    fun sendOut(quote: Quote) = run(quote.symbol) {
        val result = lock.withLock { app.engine.request("sendOut", JSONObject().put("id", quote.id)) as JSONObject }
        strings(result.getJSONArray("txHashes"))
    }

    suspend fun exitPlan(): ExitPlan {
        val p = lock.withLock { app.engine.request("exitPlan") as JSONObject }
        return ExitPlan(p.getString("owner"), BigInteger(p.getString("needed")), BigInteger(p.getString("balance")))
    }

    fun exit(symbol: String, commitment: String, recipient: String) = run(symbol) {
        val params = JSONObject().put("commitment", commitment).put("recipient", recipient).put("symbol", symbol)
        val result = lock.withLock { app.engine.request("exit", params) as JSONObject }
        strings(result.getJSONArray("txHashes"))
    }

    fun dismissWork() = _work.update { if (it is Work.Sending) it else null }

    private fun run(symbol: String, block: suspend () -> List<String>) {
        if (_work.value is Work.Sending) return
        _work.value = Work.Sending(symbol, "starting", 1, 1, null)
        app.scope.launch {
            // Listening before the block runs, so the first stage isn't missed.
            val follow = launch(start = CoroutineStart.UNDISPATCHED) {
                app.engine.events.collect { event -> follow(event) }
            }
            _work.value = try {
                Work.Sent(block())
            } catch (error: CancellationException) {
                throw error
            } catch (error: EngineException) {
                Work.Failed(error.code, error.message ?: error.code)
            } catch (error: Exception) {
                Work.Failed("error", error.message ?: error.javaClass.simpleName)
            } finally {
                follow.cancel()
            }
            runCatching { refresh() }
        }
    }

    private fun follow(event: EngineEvent) {
        if (event.name != "progress") return
        val p = event.params
        _work.update { work ->
            (work as? Work.Sending)?.copy(
                stage = p.optString("stage", work.stage),
                part = p.optInt("part", work.part),
                parts = p.optInt("parts", work.parts),
                txHash = p.optString("txHash").ifEmpty { work.txHash },
            ) ?: work
        }
    }

    private fun ready(status: JSONObject, keepBalances: Boolean) {
        _state.update { state ->
            val before = state as? AccountState.Ready
            AccountState.Ready(
                owner = status.getString("owner"),
                address = status.getString("address"),
                watch = strings(status.getJSONArray("watch")),
                balances = if (keepBalances) before?.balances.orEmpty() else emptyList(),
                incoming = before?.incoming.orEmpty(),
            )
        }
        saveForWidget()
    }

    private fun saveForWidget() {
        val ready = _state.value as? AccountState.Ready ?: return
        ShieldWidget.save(app, ready)
        chainCache?.let { WatchList.save(app, ready, it) }
    }

    private fun strings(list: JSONArray) = (0 until list.length()).map { list.getString(it) }

    private fun parseIncoming(i: JSONObject) = Incoming(
        symbol = i.getString("symbol"),
        decimals = i.getInt("decimals"),
        amount = BigInteger(i.getString("amount")),
        state = when (i.getString("state")) {
            "below_minimum" -> Incoming.State.BELOW_MINIMUM
            "fee_high" -> Incoming.State.FEE_HIGH
            "gas_unavailable" -> Incoming.State.GAS_UNAVAILABLE
            else -> Incoming.State.SHIELDING
        },
        minimum = i.optString("minimum").ifEmpty { null }?.let(::BigInteger),
        fee = i.optString("fee").ifEmpty { null }?.let(::BigInteger),
        txHash = i.optString("txHash").ifEmpty { null },
    )

    private fun parseBalance(b: JSONObject): Balance {
        fun deposits(name: String) = b.optJSONArray(name)?.let { a ->
            (0 until a.length()).map { i ->
                val d = a.getJSONObject(i)
                Deposit(d.getString("commitment"), BigInteger(d.getString("value")), d.optString("label").ifEmpty { null })
            }
        }.orEmpty()
        return Balance(
            symbol = b.getString("symbol"),
            decimals = b.getInt("decimals"),
            spendable = BigInteger(b.getString("spendable")),
            pending = BigInteger(b.getString("pending")),
            waiting = deposits("pendingDeposits"),
            declined = deposits("declined"),
        )
    }

    companion object {
        fun format(units: BigInteger, decimals: Int, maxFraction: Int = 6): String {
            val value = BigDecimal(units, decimals)
            val shown = value.setScale(minOf(decimals, maxFraction), java.math.RoundingMode.DOWN).stripTrailingZeros()
            return if (shown.scale() < 0) shown.setScale(0).toPlainString() else shown.toPlainString()
        }
    }
}
