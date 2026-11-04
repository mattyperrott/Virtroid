// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import io.virtroid.shield.widget.ShieldWidget
import java.math.BigInteger

/**
 * Debug builds only: puts the widget in a state to look at, with no wallet or
 * chain behind it. All extras are optional:
 *   adb shell am broadcast -n io.virtroid.shield/.DebugWidget --es USDC 250000000 \
 *     --es waiting 250000000 --es incoming fee_high --ei declined 1 --ez show true
 */
class DebugWidget : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        fun units(name: String) = intent.getStringExtra(name)?.let(::BigInteger) ?: BigInteger.ZERO
        fun deposit(value: BigInteger) = if (value.signum() > 0) listOf(Deposit("0x0", value, null)) else emptyList()
        val usdc = Balance(
            "USDC",
            6,
            units("USDC"),
            units("waiting"),
            deposit(units("waiting")),
            if (intent.getIntExtra("declined", 0) > 0) deposit(BigInteger.valueOf(250_000_000)) else emptyList(),
        )
        val incoming = intent.getStringExtra("incoming")?.let { state ->
            listOf(
                Incoming(
                    "USDC",
                    6,
                    BigInteger.valueOf(250_000_000),
                    Incoming.State.valueOf(state.uppercase()),
                    BigInteger.valueOf(11_500_000),
                    BigInteger.valueOf(30_000_000),
                    null,
                ),
            )
        }.orEmpty()
        ShieldWidget.setShowBalances(context, intent.getBooleanExtra("show", false))
        ShieldWidget.save(context, AccountState.Ready("0x0", "0x12aB34cD56eF78aB90cD12eF34aB56cD78eF9f3C", emptyList(), listOf(usdc), incoming))
    }
}
