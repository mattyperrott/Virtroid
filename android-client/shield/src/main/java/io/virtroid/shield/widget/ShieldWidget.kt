// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.view.View
import android.widget.RemoteViews
import androidx.annotation.ColorRes
import androidx.core.content.edit
import io.virtroid.shield.AccountState
import io.virtroid.shield.Address
import io.virtroid.shield.Incoming
import io.virtroid.shield.R
import io.virtroid.shield.Shield
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.ui.MainActivity
import io.virtroid.shield.watch.WatchService
import org.json.JSONArray
import org.json.JSONObject

/**
 * The home-screen widget, one row: the mark, what's going on in two lines, and
 * the one thing to do next. With nothing in the pool that's copying the deposit
 * address; with something, sending it out. Amounts show only when the user
 * turned that on in the app (off by default: the home screen is the most
 * looked-at surface there is). A tap anywhere else opens the app.
 */
class ShieldWidget : AppWidgetProvider() {
    override fun onUpdate(context: Context, manager: AppWidgetManager, ids: IntArray) {
        val views = render(context)
        ids.forEach { manager.updateAppWidget(it, views) }
    }

    companion object {
        private const val SUMMARY = "widget.summary"
        private const val SHOW_BALANCES = "widget.showBalances"

        fun showBalances(context: Context): Boolean =
            context.getSharedPreferences("shield", Context.MODE_PRIVATE).getBoolean(SHOW_BALANCES, false)

        fun setShowBalances(context: Context, show: Boolean) {
            context.getSharedPreferences("shield", Context.MODE_PRIVATE).edit { putBoolean(SHOW_BALANCES, show) }
            refreshAll(context)
        }

        /** Keeps what the widget shows (sealed in the app's store) and redraws it. */
        fun save(context: Context, state: AccountState.Ready) {
            fun amount(units: java.math.BigInteger, decimals: Int, symbol: String) = "${Shield.format(units, decimals, 4)} $symbol"
            val summary = JSONObject()
                .put("address", state.address)
                .put("balances", state.balances.filter { it.spendable.signum() > 0 }.joinToString("  ·  ") { amount(it.spendable, it.decimals, it.symbol) })
                .put("declined", state.balances.sumOf { it.declined.size })
                .put("screening", JSONArray(state.balances.flatMap { b -> b.waiting.map { amount(it.value, b.decimals, b.symbol) } }))
            state.incoming.firstOrNull()?.let { i ->
                summary.put(
                    "incoming",
                    JSONObject()
                        .put("state", i.state.name)
                        .put("amount", amount(i.amount, i.decimals, i.symbol))
                        .put("minimum", i.minimum?.let { amount(it, i.decimals, i.symbol) }),
                )
            }
            (context.applicationContext as ShieldApp).store.set(SUMMARY, summary.toString())
            refreshAll(context)
        }

        fun clear(context: Context) {
            (context.applicationContext as ShieldApp).store.delete(SUMMARY)
            refreshAll(context)
        }

        /** The deposit address the widget last showed, or null with no wallet. */
        fun address(context: Context): String? = summary(context)?.optString("address")?.ifEmpty { null }

        /** Whether anything's on its way, so the watcher asks the engine about it now and then. */
        fun hasWork(context: Context): Boolean {
            val summary = summary(context) ?: return false
            return summary.has("incoming") || (summary.optJSONArray("screening")?.length() ?: 0) > 0
        }

        fun refreshAll(context: Context) {
            val manager = AppWidgetManager.getInstance(context)
            val ids = manager.getAppWidgetIds(ComponentName(context, ShieldWidget::class.java))
            if (ids.isNotEmpty()) ShieldWidget().onUpdate(context, manager, ids)
        }

        private fun summary(context: Context): JSONObject? =
            runCatching { (context.applicationContext as ShieldApp).store.get(SUMMARY)?.let(::JSONObject) }.getOrNull()

        private fun render(context: Context): RemoteViews {
            val content = Content.of(context, summary(context), showBalances(context))
            val views = RemoteViews(context.packageName, R.layout.widget_shield)
            views.setTextViewText(R.id.widget_title, content.title)
            views.setTextColor(R.id.widget_title, context.getColor(content.titleColour))
            views.setTextViewText(R.id.widget_detail, content.detail)
            views.setViewVisibility(R.id.widget_progress, if (content.spinner) View.VISIBLE else View.GONE)
            views.setOnClickPendingIntent(android.R.id.background, open(context, 0, sendOut = false))
            views.setViewVisibility(R.id.widget_action, if (content.action == Action.NONE) View.GONE else View.VISIBLE)
            when (content.action) {
                Action.SET_UP -> {
                    views.setTextViewText(R.id.widget_action, context.getString(R.string.widget_set_up_action))
                    views.setOnClickPendingIntent(R.id.widget_action, open(context, 1, sendOut = false))
                }
                Action.COPY -> {
                    views.setTextViewText(R.id.widget_action, context.getString(R.string.widget_copy))
                    views.setOnClickPendingIntent(
                        R.id.widget_action,
                        PendingIntent.getForegroundService(context, 2, WatchService.copyIntent(context), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT),
                    )
                }
                Action.SEND_OUT -> {
                    views.setTextViewText(R.id.widget_action, context.getString(R.string.send_out))
                    views.setOnClickPendingIntent(R.id.widget_action, open(context, 3, sendOut = true))
                }
                Action.NONE -> Unit
            }
            return views
        }

        private fun open(context: Context, code: Int, sendOut: Boolean): PendingIntent =
            PendingIntent.getActivity(
                context,
                code,
                MainActivity.intent(context, sendOut).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
    }
}

private enum class Action { NONE, SET_UP, COPY, SEND_OUT }

/** What the widget shows, in order of what most needs looking at. */
private class Content(
    val title: String,
    val detail: String,
    val action: Action,
    val spinner: Boolean = false,
    @param:ColorRes val titleColour: Int = R.color.text,
) {
    companion object {
        fun of(context: Context, summary: JSONObject?, show: Boolean): Content {
            if (summary == null) {
                return Content(context.getString(R.string.widget_not_set_up), context.getString(R.string.widget_set_up_detail), Action.SET_UP)
            }
            val balances = summary.optString("balances")
            // What to offer beside a status: sending out if there's anything to send.
            val next = if (balances.isNotEmpty()) Action.SEND_OUT else Action.COPY
            val incoming = summary.optJSONObject("incoming")
            val screening = summary.optJSONArray("screening")
            val declined = summary.optInt("declined")
            if (declined > 0) {
                return Content(
                    context.resources.getQuantityString(R.plurals.widget_declined, declined, declined),
                    context.getString(R.string.status_declined_detail),
                    Action.NONE,
                    titleColour = R.color.error,
                )
            }
            if (incoming != null) {
                val amount = incoming.getString("amount")
                val received = if (show) context.getString(R.string.status_received, amount) else context.getString(R.string.widget_received)
                return when (Incoming.State.valueOf(incoming.getString("state"))) {
                    Incoming.State.SHIELDING -> shielding(context, if (show) amount else null)
                    Incoming.State.BELOW_MINIMUM -> Content(received, context.getString(R.string.status_below_minimum, incoming.optString("minimum")), next)
                    Incoming.State.FEE_HIGH -> Content(received, context.getString(R.string.widget_fee_high), next)
                    Incoming.State.GAS_UNAVAILABLE -> Content(received, context.getString(R.string.status_gas_unavailable), next)
                }
            }
            if (screening != null && screening.length() > 0) {
                return shielding(context, if (show) screening.getString(0) else null)
            }
            if (balances.isNotEmpty()) {
                return Content(
                    if (show) balances else context.getString(R.string.home_balance),
                    context.getString(if (show) R.string.home_balance else R.string.widget_hidden),
                    Action.SEND_OUT,
                )
            }
            return Content(context.getString(R.string.home_address), Address.short(summary.optString("address")), Action.COPY)
        }

        private fun shielding(context: Context, amount: String?) = Content(
            if (amount != null) context.getString(R.string.status_shielding, amount) else context.getString(R.string.widget_shielding),
            context.getString(R.string.status_shielding_detail),
            Action.NONE,
            spinner = true,
        )
    }
}
