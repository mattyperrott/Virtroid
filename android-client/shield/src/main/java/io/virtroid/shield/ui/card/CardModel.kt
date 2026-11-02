// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui.card

import android.app.Application
import android.graphics.Bitmap
import androidx.annotation.StringRes
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import io.virtroid.shield.AccountState
import io.virtroid.shield.Address
import io.virtroid.shield.AddressCheck
import io.virtroid.shield.AmountInput
import io.virtroid.shield.Amounts
import io.virtroid.shield.AssetInfo
import io.virtroid.shield.Balance
import io.virtroid.shield.ChainInfo
import io.virtroid.shield.Operations
import io.virtroid.shield.PendingDeposit
import io.virtroid.shield.Quote
import io.virtroid.shield.R
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.ShieldPlan
import io.virtroid.shield.WalletFunds
import io.virtroid.shield.engine.EngineException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.math.BigInteger

/** The card's tabs, in the order the bar shows them. */
enum class Tab { SHIELD, WITHDRAW, SEND }

/** Where a tab is: its steps stack up, and Back takes the top one off. */
sealed interface Step {
    data object Amount : Step
    data object Token : Step
    /** Shield: where it goes and what it comes to. */
    data object Destination : Step
    /** Withdraw and Send: who receives it. */
    data object Recipient : Step
    /** Withdraw and Send: the proof and the relayer's fee, before review. */
    data object Preparing : Step
    data object Review : Step
    /** The operation, from the wallet's prompt to how it ended. */
    data object Processing : Step
    data class Exit(val symbol: String, val deposit: PendingDeposit) : Step
}

sealed interface Loadable<out T> {
    data object Idle : Loadable<Nothing>
    data object Loading : Loadable<Nothing>
    data class Ready<T>(val value: T) : Loadable<T>
    data class Failed(val message: Message) : Loadable<Nothing>
}

/** A string resource and its arguments, resolved where it's shown. */
data class Message(@param:StringRes val id: Int, val args: List<Any> = emptyList())

/** One tab's draft, kept through Back, tab switches and errors. */
class Draft(val tab: Tab) {
    var symbol by mutableStateOf("USDC")
    var amount by mutableStateOf("")
    /** Withdraw and Send: the largest deposit less the relayer's fee. */
    var max by mutableStateOf(false)
    var recipient by mutableStateOf("")
    var stack by mutableStateOf<List<Step>>(listOf(Step.Amount))
    var plan by mutableStateOf<Loadable<ShieldPlan>>(Loadable.Idle)
    /** Send: whether the recipient has a Privacy Pools account here. */
    var receives by mutableStateOf<Loadable<Boolean>>(Loadable.Idle)
    var quote by mutableStateOf<Quote?>(null)
    var error by mutableStateOf<Message?>(null)
    val step: Step get() = stack.last()
}

/** What the amount field holds, for the line under it and the Continue button. */
sealed interface AmountState {
    /** The account or balances aren't in yet. */
    data object Waiting : AmountState
    data class Hint(val message: Message) : AmountState
    data class Invalid(val message: Message, val offerShield: Boolean = false) : AmountState
    data class Ok(val units: BigInteger, val hint: Message) : AmountState
}

/** What the recipient field holds. */
sealed interface RecipientState {
    data object Empty : RecipientState
    data class Typing(val message: Message) : RecipientState
    data class Problem(val message: Message) : RecipientState
    data object Checking : RecipientState
    data class CantReceive(val address: String) : RecipientState
    data class Fine(val address: String, val message: Message, val warning: Message?) : RecipientState
}

sealed interface Connect {
    data object Idle : Connect
    data object Pairing : Connect
    data class Waiting(val uri: String, val qr: ImageBitmap) : Connect
    data class Failed(val message: Message) : Connect
}

sealed interface Setup {
    data object Idle : Setup
    /** `signatures` asked for so far (two in all), then the publishing transaction. */
    data class Busy(val signatures: Int, val publishing: Boolean) : Setup
    data class Failed(val message: Message) : Setup
}

/**
 * The transaction card's state and steps. Drafts live as long as the card;
 * the operation a Confirm starts lives in the app ([Operations]), so it
 * carries on if the card closes, and the card picks it up when it opens.
 */
class CardModel(application: Application) : AndroidViewModel(application) {
    private val app = application as ShieldApp
    private val shield = app.shield
    val operations: Operations = app.operations
    val account = shield.state

    var tab by mutableStateOf(Tab.SEND)
        private set
    val drafts: Map<Tab, Draft> = Tab.entries.associateWith(::Draft)
    /** Which way the last move went, for the slide. */
    var forward by mutableStateOf(true)
        private set
    var chains by mutableStateOf<List<ChainInfo>>(emptyList())
        private set
    var chain by mutableStateOf<ChainInfo?>(null)
        private set
    var loading by mutableStateOf(false)
        private set
    var loadError by mutableStateOf<Message?>(null)
        private set
    /** The wallet's funds per asset on the current chain, for Shield. */
    val funds = mutableStateMapOf<String, Loadable<WalletFunds>>()
    var connect by mutableStateOf<Connect>(Connect.Idle)
        private set
    var setup by mutableStateOf<Setup>(Setup.Idle)
        private set

    private var started = false
    private var tabPicked = false

    val draft: Draft get() = drafts.getValue(tab)
    val configured: Boolean get() = app.walletConnect.configured || app.localTest != null
    val owner: String? get() = app.ownerAddress()
    val walletConnected: Boolean get() = owner != null

    /** The wallet app's own link, when it's on this phone and gave one. */
    fun walletLink(): String? = app.walletConnect.session.value?.walletLink

    /** Opens on `requested` if given; picks up an operation that's going on. */
    fun start(requested: Tab?) {
        if (requested != null) {
            tabPicked = true
            select(requested)
        }
        operations.current.value?.let(::resume)
        if (started) return
        started = true
        viewModelScope.launch {
            app.engine.events.collect { event -> if (event.name == "wallet") onSetupPrompt(event.params.optString("method")) }
        }
        viewModelScope.launch {
            try {
                chains = shield.chains()
                chain = shield.chain()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                loadError = describe(error)
                return@launch
            }
            load()
        }
    }

    fun load() {
        if (!configured) {
            loadError = Message(R.string.not_configured)
            return
        }
        if (loading) return
        viewModelScope.launch {
            loading = true
            loadError = null
            try {
                val state = shield.load()
                if (state is AccountState.Ready) {
                    if (!tabPicked) {
                        tabPicked = true
                        val anything = state.balances.any { it.spendable.signum() > 0 }
                        if (!anything && operations.current.value == null) select(Tab.SHIELD)
                    }
                    loadFunds(drafts.getValue(Tab.SHIELD).symbol)
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                loadError = describe(error)
            } finally {
                loading = false
            }
        }
    }

    // --- navigation ---------------------------------------------------------

    fun select(next: Tab) {
        if (next == tab) return
        forward = next.ordinal > tab.ordinal
        tab = next
        if (next == Tab.SHIELD) loadFunds(draft.symbol)
    }

    fun canGoBack(d: Draft = draft): Boolean =
        d.stack.size > 1 && d.step != Step.Processing && d.step != Step.Preparing

    /** Takes the current step off; false when there's nothing to go back to. */
    fun back(): Boolean {
        val d = draft
        if (!canGoBack(d)) return false
        forward = false
        d.stack = d.stack.dropLast(1)
        d.error = null
        return true
    }

    private fun push(d: Draft, step: Step) {
        forward = true
        d.stack = d.stack + step
    }

    private fun resume(op: Operations.Operation) {
        val t = Tab.valueOf(op.summary.tab)
        tabPicked = true
        select(t)
        val d = drafts.getValue(t)
        if (d.step != Step.Processing) d.stack = d.stack + Step.Processing
    }

    // --- tokens and chains --------------------------------------------------

    fun asset(symbol: String): AssetInfo? = chain?.assets?.firstOrNull { it.symbol == symbol }

    fun balance(symbol: String): Balance? =
        (account.value as? AccountState.Ready)?.balances?.firstOrNull { it.symbol == symbol }

    fun openTokens() {
        if (tab == Tab.SHIELD) chain?.assets?.forEach { if (funds[it.symbol] !is Loadable.Ready) loadFunds(it.symbol) }
        push(draft, Step.Token)
    }

    fun pickToken(chainId: Int, symbol: String) {
        val d = draft
        d.symbol = symbol
        d.max = false
        clearWork(d)
        forward = false
        d.stack = listOf(Step.Amount)
        if (chainId != chain?.chainId) {
            if (operations.running) return
            shield.chainId = chainId
            chain = chains.firstOrNull { it.chainId == chainId }
            funds.clear()
            for (other in drafts.values) {
                clearWork(other)
                if (other.step != Step.Processing) other.stack = listOf(Step.Amount)
            }
            load()
        } else if (d.tab == Tab.SHIELD) {
            loadFunds(symbol)
        }
    }

    private fun loadFunds(symbol: String) {
        if (account.value !is AccountState.Ready || funds[symbol] is Loadable.Loading) return
        val chainId = shield.chainId
        funds[symbol] = Loadable.Loading
        viewModelScope.launch {
            val result = try {
                Loadable.Ready(shield.walletBalance(symbol))
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                Loadable.Failed(describe(error))
            }
            if (shield.chainId == chainId) funds[symbol] = result
        }
    }

    // --- amount -------------------------------------------------------------

    fun setAmount(text: String) {
        val d = draft
        if (Amounts.secondPoint(d.amount, text)) return
        d.amount = Amounts.clean(text)
        d.max = false
        clearWork(d)
    }

    fun useMax() {
        val d = draft
        val b = balance(d.symbol) ?: return
        val asset = asset(d.symbol) ?: return
        d.amount = Amounts.plain(b.maxSingle, asset.decimals)
        d.max = true
        clearWork(d)
    }

    fun amountState(d: Draft): AmountState {
        val ready = account.value as? AccountState.Ready ?: return AmountState.Waiting
        val asset = asset(d.symbol) ?: return AmountState.Waiting
        val decimals = asset.decimals
        fun show(units: BigInteger) = Amounts.show(units, decimals, d.symbol)
        val input = Amounts.read(d.amount, decimals)
        fun badInput(): AmountState.Invalid? = when (input) {
            AmountInput.NotANumber, AmountInput.Zero -> AmountState.Invalid(Message(R.string.card_enter_amount))
            is AmountInput.TooPrecise -> AmountState.Invalid(Message(R.string.card_too_precise, listOf(d.symbol, input.decimals)))
            else -> null
        }
        if (d.tab == Tab.SHIELD) {
            val wallet = funds[d.symbol]
            val hint = when (wallet) {
                is Loadable.Ready -> Message(R.string.card_in_wallet, listOf(show(wallet.value.balance)))
                is Loadable.Failed -> Message(R.string.card_wallet_unread)
                else -> Message(R.string.card_checking_wallet)
            }
            badInput()?.let { return it }
            val units = (input as? AmountInput.Units)?.units ?: return AmountState.Hint(hint)
            val f = (wallet as? Loadable.Ready)?.value ?: return AmountState.Hint(hint)
            return when {
                units > f.balance -> AmountState.Invalid(Message(R.string.card_more_than_wallet, listOf(show(f.balance))))
                f.reserve.signum() > 0 && units > f.balance - f.reserve ->
                    AmountState.Invalid(Message(R.string.card_leave_gas, listOf(show(f.reserve))))
                else -> AmountState.Ok(units, hint)
            }
        }
        val b = ready.balances.firstOrNull { it.symbol == d.symbol }
        val spendable = b?.spendable ?: BigInteger.ZERO
        val single = b?.maxSingle ?: BigInteger.ZERO
        if (spendable.signum() == 0) {
            val pending = b?.pending ?: BigInteger.ZERO
            return if (pending.signum() > 0) AmountState.Invalid(Message(R.string.card_nothing_ready, listOf(show(pending))))
            else AmountState.Invalid(Message(R.string.card_nothing_shielded, listOf(d.symbol)), offerShield = true)
        }
        val hint = Message(if (d.tab == Tab.SEND) R.string.card_up_to_send else R.string.card_up_to_withdraw, listOf(show(single)))
        if (d.max) return AmountState.Ok(single, Message(R.string.card_max_hint))
        badInput()?.let { return it }
        val units = (input as? AmountInput.Units)?.units ?: return AmountState.Hint(hint)
        return when {
            units > single && spendable > single -> AmountState.Invalid(
                Message(
                    if (d.tab == Tab.SEND) R.string.card_one_deposit_send else R.string.card_one_deposit_withdraw,
                    listOf(show(single), show(spendable)),
                ),
            )
            units > single -> AmountState.Invalid(Message(R.string.card_more_than_shielded, listOf(show(spendable))))
            else -> AmountState.Ok(units, hint)
        }
    }

    /** Continue from the amount. */
    fun next() {
        val d = draft
        val ok = amountState(d) as? AmountState.Ok ?: return
        if (d.tab == Tab.SHIELD) {
            push(d, Step.Destination)
            loadPlan(d, ok.units)
        } else {
            push(d, Step.Recipient)
        }
    }

    private fun loadPlan(d: Draft, units: BigInteger) {
        if (d.plan is Loadable.Ready || d.plan is Loadable.Loading) return
        val symbol = d.symbol
        val amount = d.amount
        val decimals = asset(symbol)?.decimals ?: return
        d.plan = Loadable.Loading
        viewModelScope.launch {
            val result = try {
                Loadable.Ready(shield.shieldPlan(symbol, units))
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (error is EngineException && error.code == "below_minimum") {
                    val minimum = error.message?.toBigIntegerOrNull() ?: BigInteger.ZERO
                    Loadable.Failed(Message(R.string.card_below_minimum, listOf(Amounts.show(minimum, decimals, symbol))))
                } else {
                    Loadable.Failed(describe(error))
                }
            }
            if (d.symbol == symbol && d.amount == amount) d.plan = result
        }
    }

    fun retryPlan() {
        val d = draft
        val ok = amountState(d) as? AmountState.Ok ?: return
        d.plan = Loadable.Idle
        loadPlan(d, ok.units)
    }

    // --- recipient ----------------------------------------------------------

    fun setRecipient(text: String) {
        val d = draft
        val value = text.trim()
        if (value == d.recipient) return
        d.recipient = value
        d.quote = null
        d.error = null
        d.receives = Loadable.Idle
        if (d.tab == Tab.SEND) (Address.check(value) as? AddressCheck.Valid)?.let { checkReceives(d, it.checksummed) }
    }

    fun recheckReceives() {
        val d = draft
        (Address.check(d.recipient) as? AddressCheck.Valid)?.let { checkReceives(d, it.checksummed) }
    }

    private fun checkReceives(d: Draft, address: String) {
        d.receives = Loadable.Loading
        val typed = d.recipient
        viewModelScope.launch {
            val result = try {
                Loadable.Ready(shield.canReceive(address))
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                Loadable.Failed(describe(error))
            }
            if (d.recipient == typed) d.receives = result
        }
    }

    fun recipientState(d: Draft): RecipientState {
        return when (val check = Address.check(d.recipient)) {
            AddressCheck.Empty -> RecipientState.Empty
            AddressCheck.Incomplete -> RecipientState.Typing(Message(R.string.card_address_incomplete))
            AddressCheck.NotHex -> RecipientState.Problem(Message(R.string.card_address_not_hex))
            AddressCheck.TooLong -> RecipientState.Problem(Message(R.string.card_address_too_long))
            AddressCheck.BadChecksum -> RecipientState.Problem(Message(R.string.card_address_checksum))
            AddressCheck.Zero -> RecipientState.Problem(Message(R.string.card_address_zero))
            is AddressCheck.Valid -> {
                val mine = Address.same(check.checksummed, owner)
                if (d.tab == Tab.WITHDRAW) {
                    RecipientState.Fine(
                        check.checksummed,
                        Message(R.string.card_address_valid),
                        if (mine) Message(R.string.card_address_is_wallet) else null,
                    )
                } else {
                    when (val r = d.receives) {
                        is Loadable.Ready -> if (r.value) {
                            RecipientState.Fine(
                                check.checksummed,
                                Message(R.string.card_address_can_receive),
                                if (mine) Message(R.string.card_address_is_own) else null,
                            )
                        } else {
                            RecipientState.CantReceive(check.checksummed)
                        }
                        is Loadable.Failed -> RecipientState.Problem(r.message)
                        else -> RecipientState.Checking
                    }
                }
            }
        }
    }

    /** Send can't reach this address privately: carry the amount and address over to Withdraw. */
    fun withdrawInstead() {
        val from = draft
        val to = drafts.getValue(Tab.WITHDRAW)
        to.symbol = from.symbol
        to.amount = from.amount
        to.max = from.max
        to.recipient = from.recipient
        clearWork(to)
        to.stack = listOf(Step.Amount, Step.Recipient)
        select(Tab.WITHDRAW)
    }

    // --- review and confirm -------------------------------------------------

    fun review() {
        val d = draft
        if (d.tab == Tab.SHIELD) {
            if (d.plan is Loadable.Ready) push(d, Step.Review)
        } else {
            prepare(d)
        }
    }

    /** Proves the send or withdrawal and gets the relayer's fee: the review's numbers. */
    fun prepare(d: Draft = draft) {
        val address = (recipientState(d) as? RecipientState.Fine)?.address ?: return
        if (operations.running) {
            d.error = Message(R.string.card_busy)
            return
        }
        val symbol = d.symbol
        val amount: BigInteger? = if (d.max) null else (amountState(d) as? AmountState.Ok)?.units ?: return
        d.error = null
        d.quote = null
        // The engine keeps one quote: another tab's is gone once this one is made.
        for (other in drafts.values) {
            if (other === d || other.quote == null) continue
            other.quote = null
            if (other.step == Step.Review) other.stack = other.stack.dropLast(1)
        }
        forward = true
        d.stack = d.stack.filter { it != Step.Review && it != Step.Preparing } + Step.Preparing
        viewModelScope.launch {
            try {
                val q = if (d.tab == Tab.SEND) shield.quoteSend(symbol, amount, address) else shield.quoteWithdraw(symbol, amount, address)
                if (d.step == Step.Preparing) {
                    d.quote = q
                    d.stack = d.stack.dropLast(1) + Step.Review
                }
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                if (error is EngineException && error.code == "recipient_unregistered") d.receives = Loadable.Ready(false)
                if (d.step == Step.Preparing) {
                    forward = false
                    d.stack = d.stack.dropLast(1)
                    d.error = describe(error)
                }
            }
        }
    }

    fun expired(q: Quote, now: Long = System.currentTimeMillis()): Boolean = q.expiresAt != null && now >= q.expiresAt

    fun confirm() {
        val d = draft
        val asset = asset(d.symbol) ?: return
        val chainName = chain?.name.orEmpty()
        val started = if (d.tab == Tab.SHIELD) {
            val units = (amountState(d) as? AmountState.Ok)?.units ?: return
            val plan = (d.plan as? Loadable.Ready)?.value ?: return
            val summary = Operations.Summary(
                kind = Operations.Kind.SHIELD,
                tab = d.tab.name,
                symbol = d.symbol,
                decimals = asset.decimals,
                chainName = chainName,
                amount = units,
                fee = plan.fee,
                hold = plan.hold,
                pp = asset.yield,
            )
            val symbol = d.symbol
            operations.start(summary) { shield.shield(symbol, units) }
        } else {
            val q = d.quote ?: return
            if (expired(q)) return
            val address = (recipientState(d) as? RecipientState.Fine)?.address ?: return
            val summary = Operations.Summary(
                kind = if (d.tab == Tab.SEND) Operations.Kind.SEND else Operations.Kind.WITHDRAW,
                tab = d.tab.name,
                symbol = d.symbol,
                decimals = asset.decimals,
                chainName = chainName,
                amount = q.receive,
                fee = q.fee,
                recipient = address,
                pp = asset.yield,
            )
            operations.start(summary) { shield.confirm(q.id) }
        }
        if (started) push(d, Step.Processing) else d.error = Message(R.string.card_busy)
    }

    fun exit(symbol: String, deposit: PendingDeposit) = push(draft, Step.Exit(symbol, deposit))

    fun confirmExit(step: Step.Exit) {
        val d = draft
        val asset = asset(step.symbol) ?: return
        val summary = Operations.Summary(
            kind = Operations.Kind.EXIT,
            tab = d.tab.name,
            symbol = step.symbol,
            decimals = asset.decimals,
            chainName = chain?.name.orEmpty(),
            amount = step.deposit.value,
            recipient = owner,
        )
        val started = operations.start(summary) { shield.exit(step.deposit.commitment) }
        if (started) push(d, Step.Processing) else d.error = Message(R.string.card_busy)
    }

    /** After a failure: back to where the user can try again, the draft as it was. */
    fun retry() {
        val op = operations.current.value ?: return
        operations.dismiss(op.id)
        val d = draft
        forward = false
        d.stack = when (op.summary.kind) {
            // The relayer's quote went with the attempt: prove and price again from the recipient.
            Operations.Kind.SEND, Operations.Kind.WITHDRAW -> d.stack.filter { it != Step.Processing && it != Step.Review }
            else -> d.stack.dropLast(1)
        }
        d.quote = null
    }

    /** "Shield again" and the like: a fresh draft on the same token. */
    fun again() {
        operations.current.value?.let { operations.dismiss(it.id) }
        val d = draft
        forward = false
        d.amount = ""
        d.max = false
        d.recipient = ""
        clearWork(d)
        d.stack = listOf(Step.Amount)
        if (d.tab == Tab.SHIELD) {
            funds.remove(d.symbol)
            loadFunds(d.symbol)
        }
    }

    fun finished() {
        operations.current.value?.let { if (it.outcome != null) operations.dismiss(it.id) }
    }

    private fun clearWork(d: Draft) {
        d.plan = Loadable.Idle
        d.quote = null
        d.error = null
    }

    // --- wallet connection and account setup ---------------------------------

    fun pair() {
        if (connect == Connect.Pairing || connect is Connect.Waiting) return
        connect = Connect.Pairing
        viewModelScope.launch {
            try {
                val ids = chains.ifEmpty { shield.chains() }.map { it.chainId }
                val pairing = app.walletConnect.pair(ids)
                val qr = withContext(Dispatchers.Default) { qr(pairing.uri) }
                connect = Connect.Waiting(pairing.uri, qr)
                pairing.approved.await()
                connect = Connect.Idle
                load()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                connect = Connect.Failed(describe(error))
            }
        }
    }

    fun setUp() {
        val owner = (account.value as? AccountState.NeedsSetup)?.owner ?: return
        if (setup is Setup.Busy) return
        setup = Setup.Busy(signatures = 0, publishing = false)
        viewModelScope.launch {
            try {
                shield.setUp(owner)
                setup = Setup.Idle
                load()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                setup = Setup.Failed(describe(error))
            }
        }
    }

    private fun onSetupPrompt(method: String) {
        val busy = setup as? Setup.Busy ?: return
        setup = when (method) {
            "eth_signTypedData_v4" -> busy.copy(signatures = busy.signatures + 1)
            "eth_sendTransaction" -> busy.copy(publishing = true)
            else -> busy
        }
    }

    // --- words --------------------------------------------------------------

    fun describe(error: Throwable): Message {
        if (error !is EngineException) return Message(R.string.card_error_detail, listOf(error.message ?: error.javaClass.simpleName))
        return when (error.code) {
            "wallet_rejected" -> Message(R.string.error_wallet_rejected)
            "no_wallet" -> Message(R.string.error_no_wallet)
            "wallet_not_deterministic" -> Message(R.string.error_wallet_not_deterministic)
            "split_needed" -> Message(R.string.error_split_needed)
            "nothing_spendable" -> Message(R.string.error_nothing_spendable)
            "below_fee" -> Message(R.string.error_below_fee)
            "fee_changed" -> Message(R.string.error_fee_changed)
            "blocked" -> Message(R.string.error_blocked)
            "engine_restarted" -> Message(R.string.error_engine_restarted)
            "recipient_unregistered" -> Message(R.string.card_recipient_unregistered, listOf(chain?.name.orEmpty()))
            "quote_replaced", "no_quote" -> Message(R.string.card_quote_gone)
            "reverted" -> Message(R.string.card_reverted)
            "unknown_asset" -> Message(R.string.card_asset_unavailable)
            else -> Message(R.string.card_error_detail, listOf(error.message ?: error.code))
        }
    }

    private fun qr(text: String): ImageBitmap {
        val size = 512
        val matrix = QRCodeWriter().encode(text, BarcodeFormat.QR_CODE, size, size, mapOf(EncodeHintType.MARGIN to 1))
        val pixels = IntArray(size * size) { if (matrix.get(it % size, it / size)) 0xFF000000.toInt() else 0xFFFFFFFF.toInt() }
        return Bitmap.createBitmap(pixels, size, size, Bitmap.Config.RGB_565).asImageBitmap()
    }
}
