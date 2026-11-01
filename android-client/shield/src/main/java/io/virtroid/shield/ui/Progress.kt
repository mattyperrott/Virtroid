// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.widget.TextView
import androidx.core.net.toUri
import io.virtroid.shield.R
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.engine.EngineException
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch

/** The engine's progress in words, and the switch to the wallet app when it has something to approve. */
object Progress {
    suspend fun follow(activity: Activity, app: ShieldApp, text: TextView) = coroutineScope {
        launch {
            app.engine.events.collect { event ->
                val line = when (event.name) {
                    "progress" -> when (event.params.optString("stage")) {
                        "sync" -> activity.getString(R.string.progress_syncing)
                        "proving" -> activity.getString(R.string.progress_proving)
                        "confirming" -> activity.getString(R.string.progress_confirming)
                        else -> null
                    }
                    "wallet" -> activity.getString(R.string.progress_wallet)
                    else -> null
                }
                if (line != null) text.text = line
            }
        }
        launch {
            app.walletConnect.prompts.collect { session ->
                text.text = activity.getString(R.string.progress_wallet_named, session.walletName.ifEmpty { activity.getString(R.string.wallet_unnamed) })
                // Bring the wallet forward when it's on this phone and says how.
                session.walletLink?.let { link ->
                    try {
                        activity.startActivity(Intent(Intent.ACTION_VIEW, link.toUri()).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                    } catch (_: ActivityNotFoundException) {
                        // The wallet is on another device; the user approves there.
                    }
                }
            }
        }
    }
}

/** Engine error codes (engine/src) in words a user can act on. */
object Errors {
    fun describe(context: Context, error: EngineException): String = when (error.code) {
        "wallet_rejected" -> context.getString(R.string.error_wallet_rejected)
        "no_wallet" -> context.getString(R.string.error_no_wallet)
        "wallet_not_deterministic" -> context.getString(R.string.error_wallet_not_deterministic)
        "split_needed" -> context.getString(R.string.error_split_needed)
        "nothing_spendable" -> context.getString(R.string.error_nothing_spendable)
        "below_fee" -> context.getString(R.string.error_below_fee)
        "fee_changed" -> context.getString(R.string.error_fee_changed)
        "blocked" -> context.getString(R.string.error_blocked)
        "engine_restarted" -> context.getString(R.string.error_engine_restarted)
        else -> error.message ?: error.code
    }
}
