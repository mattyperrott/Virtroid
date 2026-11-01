// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.Context
import io.virtroid.shield.engine.EngineException
import io.virtroid.shield.widget.ShieldWidget
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONArray
import org.json.JSONObject
import java.math.BigDecimal
import java.math.BigInteger

data class AssetInfo(val symbol: String, val token: String, val decimals: Int, val yield: Boolean)

data class ChainInfo(val chainId: Int, val name: String, val explorerUrl: String, val assets: List<AssetInfo>)

data class PendingDeposit(val commitment: String, val value: BigInteger, val label: String?)

data class Balance(
    val symbol: String,
    val decimals: Int,
    val spendable: BigInteger,
    val pending: BigInteger,
    val maxSingle: BigInteger,
    val waiting: List<PendingDeposit>,
    val declined: List<PendingDeposit>,
)

data class Quote(val receive: BigInteger, val fee: BigInteger, val expiresAt: Long?)

/** Where the account stands, for the screens and the widget. */
sealed interface AccountState {
    data object NoWallet : AccountState
    data class NeedsSetup(val owner: String) : AccountState
    data class Ready(val owner: String, val chainId: Int, val balances: List<Balance>, val syncedAt: Long) : AccountState
}

/**
 * The app's operations, over the engine: one account per wallet address and
 * chain. The screens call these and show [state]; everything about notes,
 * proofs and the relayer stays in the engine.
 */
class Shield(private val app: ShieldApp) {
    private val prefs = app.getSharedPreferences("shield", Context.MODE_PRIVATE)
    private val _state = MutableStateFlow<AccountState>(AccountState.NoWallet)
    val state: StateFlow<AccountState> = _state

    private var chainCache: List<ChainInfo>? = null

    var chainId: Int
        get() = app.localTest?.let { LOCAL_CHAIN } ?: prefs.getInt("chainId", DEFAULT_CHAIN)
        set(value) = prefs.edit().putInt("chainId", value).apply()

    suspend fun chains(): List<ChainInfo> = chainCache ?: (app.engine.request("chains") as JSONArray).let { list ->
        (0 until list.length()).map { i ->
            val c = list.getJSONObject(i)
            val assets = c.getJSONArray("assets")
            ChainInfo(
                chainId = c.getInt("chainId"),
                name = c.getString("name"),
                explorerUrl = c.getString("explorerUrl"),
                assets = (0 until assets.length()).map { j ->
                    val a = assets.getJSONObject(j)
                    AssetInfo(a.getString("symbol"), a.getString("token"), a.getInt("decimals"), a.getBoolean("yield"))
                },
            )
        }
    }.also { chainCache = it }

    suspend fun chain(): ChainInfo = chains().let { all -> all.firstOrNull { it.chainId == chainId } ?: all.first() }

    /** Opens the connected wallet's account on the current chain, if it's been set up. */
    suspend fun load(): AccountState {
        val owner = app.ownerAddress() ?: return AccountState.NoWallet.also { _state.value = it }
        return try {
            app.engine.request("open", account(owner))
            refresh(owner)
        } catch (error: EngineException) {
            if (error.code != "needs_signature") throw error
            AccountState.NeedsSetup(owner).also { _state.value = it }
        }
    }

    /**
     * Makes the account's keys: the wallet signs the account message twice
     * (both must match, see engine.ts open()), then publishes the keys on
     * chain if they aren't yet, which the wallet sends and pays gas for.
     */
    suspend fun setUp(owner: String): AccountState {
        val typed = app.engine.request("derivation", JSONObject().put("owner", owner)) as JSONObject
        val params = JSONArray().put(owner).put(typed.toString())
        val first = app.wallet.request(chainId, "eth_signTypedData_v4", params) as String
        val second = app.wallet.request(chainId, "eth_signTypedData_v4", params) as String
        val opened = app.engine.request("open", account(owner).put("signatures", JSONArray().put(first).put(second))) as JSONObject
        if (!opened.getBoolean("registered")) app.engine.request("register", account(owner))
        return refresh(owner)
    }

    suspend fun refresh(owner: String = currentOwner()): AccountState {
        val list = app.engine.request("sync", account(owner)) as JSONArray
        val balances = (0 until list.length()).map { parseBalance(list.getJSONObject(it)) }
        val ready = AccountState.Ready(owner, chainId, balances, System.currentTimeMillis())
        _state.value = ready
        ShieldWidget.save(app, ready, chain().name)
        return ready
    }

    suspend fun shield(symbol: String, amount: BigInteger) {
        app.engine.request("shield", account(currentOwner()).put("symbol", symbol).put("amount", amount.toString()))
        refresh()
    }

    suspend fun quoteWithdraw(symbol: String, amount: BigInteger?, recipient: String): Quote =
        quote("quoteWithdraw", symbol, amount, recipient)

    suspend fun quoteSend(symbol: String, amount: BigInteger?, recipient: String): Quote =
        quote("quoteSend", symbol, amount, recipient)

    suspend fun confirm(): String {
        val result = app.engine.request("confirm") as JSONObject
        refresh()
        return result.getString("txHash")
    }

    suspend fun exit(commitment: String): String {
        val result = app.engine.request("exit", account(currentOwner()).put("commitment", commitment)) as JSONObject
        refresh()
        return result.getString("txHash")
    }

    private suspend fun quote(method: String, symbol: String, amount: BigInteger?, recipient: String): Quote {
        val params = account(currentOwner())
            .put("symbol", symbol)
            .put("amount", amount?.toString() ?: "max")
            .put("recipient", recipient)
        val q = app.engine.request(method, params) as JSONObject
        return Quote(BigInteger(q.getString("receive")), BigInteger(q.getString("fee")), q.optLong("expiresAt").takeIf { it > 0 })
    }

    private fun currentOwner(): String = when (val s = _state.value) {
        is AccountState.Ready -> s.owner
        is AccountState.NeedsSetup -> s.owner
        AccountState.NoWallet -> throw EngineException("no_wallet", "Connect a wallet first")
    }

    private fun account(owner: String) = JSONObject().put("chainId", chainId).put("owner", owner)

    private fun parseBalance(b: JSONObject): Balance {
        fun deposits(name: String) = b.optJSONArray(name)?.let { a ->
            (0 until a.length()).map { i ->
                val d = a.getJSONObject(i)
                PendingDeposit(d.getString("commitment"), BigInteger(d.getString("value")), d.optString("label").ifEmpty { null })
            }
        }.orEmpty()
        return Balance(
            symbol = b.getString("symbol"),
            decimals = b.getInt("decimals"),
            spendable = BigInteger(b.getString("spendable")),
            pending = BigInteger(b.getString("pending")),
            maxSingle = BigInteger(b.getString("maxSingle")),
            waiting = deposits("pendingDeposits"),
            declined = deposits("declined"),
        )
    }

    companion object {
        /** Arbitrum: cheap gas, and USDT, USDC and ETH all earn Aave yield in the pool. */
        const val DEFAULT_CHAIN = 42161

        /** The local test chain's id (0xbow's e2e anvil runs as Sepolia). */
        private const val LOCAL_CHAIN = 11155111

        fun format(units: BigInteger, decimals: Int, maxFraction: Int = 6): String {
            val value = BigDecimal(units, decimals)
            val shown = value.setScale(minOf(decimals, maxFraction), java.math.RoundingMode.DOWN).stripTrailingZeros()
            return if (shown.scale() < 0) shown.setScale(0).toPlainString() else shown.toPlainString()
        }

        /** Parses what the user typed in the asset's whole units, or null if it isn't an amount. */
        fun parse(text: String, decimals: Int): BigInteger? = runCatching {
            val value = BigDecimal(text.trim().replace(',', '.'))
            if (value.signum() <= 0 || value.scale() > decimals) null else value.movePointRight(decimals).toBigIntegerExact()
        }.getOrNull()
    }
}
