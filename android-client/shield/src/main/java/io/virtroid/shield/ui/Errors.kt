// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.content.Context
import io.virtroid.shield.R

/** What the engine's failures mean to the user. */
object Errors {
    fun describe(context: Context, code: String, message: String?): String = when (code) {
        "bad_words" -> context.getString(R.string.error_bad_words)
        "nothing_spendable" -> context.getString(R.string.error_nothing_spendable)
        "below_fee" -> context.getString(R.string.error_below_fee)
        "too_much" -> context.getString(R.string.error_too_much)
        "fee_changed", "quote_replaced", "no_quote" -> context.getString(R.string.error_quote_gone)
        "reverted" -> context.getString(R.string.error_reverted)
        "partly_sent" -> context.getString(R.string.error_partly_sent, message.orEmpty())
        "engine_restarted" -> context.getString(R.string.error_engine_restarted)
        "busy" -> context.getString(R.string.error_busy)
        else -> context.getString(R.string.error_detail, message ?: code)
    }
}
