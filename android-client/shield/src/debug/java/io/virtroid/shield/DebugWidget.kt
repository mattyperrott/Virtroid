// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import io.virtroid.shield.widget.ShieldWidget
import java.math.BigDecimal
import java.math.BigInteger

/**
 * Debug builds only: puts the widget in a state to look at, with no wallet
 * or chain, through the same ShieldWidget.save an account's refresh calls:
 *   adb shell am broadcast -n io.virtroid.shield/.DebugWidget [--es chain Arbitrum]
 *     [--es USDC 1250.5] [--es ETH 0.42] [--ei waiting 1] [--ei declined 1] [--ez show true]
 * A fresh install, before any of these, is the widget before set-up.
 */
class DebugWidget : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val waiting = intent.getIntExtra("waiting", 0)
        val declined = intent.getIntExtra("declined", 0)
        val deposit = PendingDeposit("0x0", BigInteger.ONE, null)
        val balances = listOf("ETH" to 18, "USDC" to 6).mapIndexed { index, (symbol, decimals) ->
            val amount = intent.getStringExtra(symbol)?.let { BigDecimal(it).movePointRight(decimals).toBigInteger() }
            Balance(
                symbol = symbol,
                decimals = decimals,
                spendable = amount ?: BigInteger.ZERO,
                pending = BigInteger.ZERO,
                maxSingle = BigInteger.ZERO,
                // Every waiting or declined deposit on the first asset: the widget only counts them.
                waiting = if (index == 0) List(waiting) { deposit } else emptyList(),
                declined = if (index == 0) List(declined) { deposit } else emptyList(),
            )
        }
        val state = AccountState.Ready("0x0", 0, balances, System.currentTimeMillis())
        ShieldWidget.setShowBalances(context, intent.getBooleanExtra("show", false))
        ShieldWidget.save(context, state, intent.getStringExtra("chain") ?: "Arbitrum")
    }
}
