// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import io.virtroid.shield.engine.EngineEvent
import io.virtroid.shield.engine.EngineException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import java.math.BigInteger

/**
 * The transaction going on, run in the app's scope so it outlives the card
 * that started it: closing the card doesn't stop a shield or a send, and
 * opening it again shows where it got to. One at a time, so nothing is
 * submitted twice.
 */
class Operations(private val app: ShieldApp) {
    enum class Kind { SHIELD, WITHDRAW, SEND, EXIT }

    sealed interface Stage {
        data object Starting : Stage
        /** The wallet is asking the user; `approval` when it's a token allowance, not the operation itself. */
        data class Wallet(val approval: Boolean) : Stage
        data object Proving : Stage
        data object Relaying : Stage
        data class Confirming(val txHash: String?) : Stage
        data object Syncing : Stage
    }

    /** What the user confirmed, kept for the result screen. */
    data class Summary(
        val kind: Kind,
        /** The card tab it was started from. */
        val tab: String,
        val symbol: String,
        val decimals: Int,
        val chainName: String,
        /** A shield's most from the wallet, what a send or withdrawal delivers, or the deposit an exit returns. */
        val amount: BigInteger,
        val fee: BigInteger? = null,
        val hold: BigInteger? = null,
        val recipient: String? = null,
        val pp: Boolean = false,
    )

    sealed interface Outcome {
        data class Done(val txHash: String?) : Outcome
        data class Failed(val code: String, val message: String) : Outcome
    }

    data class Operation(
        val id: Long,
        val summary: Summary,
        val stage: Stage,
        val stageSince: Long,
        /** A token approval went through on the way. */
        val approved: Boolean,
        val outcome: Outcome?,
    )

    private val _current = MutableStateFlow<Operation?>(null)
    val current: StateFlow<Operation?> = _current
    private var nextId = 0L

    val running: Boolean get() = _current.value?.outcome == null && _current.value != null

    /** Starts `block` unless another operation is still going; returns whether it started. */
    fun start(summary: Summary, block: suspend () -> String?): Boolean {
        if (running) return false
        val id = ++nextId
        _current.value = Operation(id, summary, Stage.Starting, System.currentTimeMillis(), approved = false, outcome = null)
        app.scope.launch {
            // Listening before the block runs, so the first stage isn't missed.
            val follow = launch(start = CoroutineStart.UNDISPATCHED) {
                app.engine.events.collect { event -> stageOf(event)?.let { advance(id, it) } }
            }
            val outcome = try {
                Outcome.Done(block())
            } catch (error: CancellationException) {
                throw error
            } catch (error: EngineException) {
                Outcome.Failed(error.code, error.message ?: error.code)
            } catch (error: Exception) {
                Outcome.Failed("error", error.message ?: error.javaClass.simpleName)
            } finally {
                follow.cancel()
            }
            _current.update { if (it?.id == id) it.copy(outcome = outcome) else it }
        }
        return true
    }

    /** Forgets a finished operation, once the user has seen how it ended. */
    fun dismiss(id: Long) {
        _current.update { if (it?.id == id && it.outcome != null) null else it }
    }

    private fun advance(id: Long, stage: Stage) {
        _current.update { op ->
            if (op == null || op.id != id || op.outcome != null || op.stage == stage) return@update op
            val approved = op.approved || (op.stage as? Stage.Wallet)?.approval == true && stage != op.stage
            op.copy(stage = stage, stageSince = System.currentTimeMillis(), approved = approved)
        }
    }

    private fun stageOf(event: EngineEvent): Stage? = when (event.name) {
        "wallet" -> Stage.Wallet(event.params.optBoolean("approval"))
        "progress" -> when (event.params.optString("stage")) {
            "proving" -> Stage.Proving
            "relaying" -> Stage.Relaying
            "confirming" -> Stage.Confirming(event.params.optString("txHash").ifEmpty { null })
            "sync" -> Stage.Syncing
            else -> null
        }
        else -> null
    }
}
