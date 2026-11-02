// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.widget.RemoteViews
import androidx.core.content.edit
import io.virtroid.shield.AccountState
import io.virtroid.shield.R
import io.virtroid.shield.Shield
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.ui.card.CardActivity
import io.virtroid.shield.ui.card.Tab
import org.json.JSONObject

/**
 * The home-screen widget: the way into the transaction card, not the card.
 * It shows the chain and whether deposits are waiting for screening or were
 * declined, and the balance only when the user turned that on in the app
 * (off by default: the home screen is the most looked-at surface there is).
 * Its buttons open the card on their tab, growing out of the widget; nothing
 * is typed, signed or sent on the widget itself.
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
        fun save(context: Context, state: AccountState.Ready, chainName: String) {
            val balances = state.balances
                .filter { it.spendable.signum() > 0 }
                .joinToString("  ·  ") { "${Shield.format(it.spendable, it.decimals, 4)} ${it.symbol}" }
            val summary = JSONObject()
                .put("chain", chainName)
                .put("balances", balances)
                .put("waiting", state.balances.sumOf { it.waiting.size })
                .put("declined", state.balances.sumOf { it.declined.size })
            (context.applicationContext as ShieldApp).store.set(SUMMARY, summary.toString())
            refreshAll(context)
        }

        fun refreshAll(context: Context) {
            val manager = AppWidgetManager.getInstance(context)
            val ids = manager.getAppWidgetIds(ComponentName(context, ShieldWidget::class.java))
            if (ids.isNotEmpty()) ShieldWidget().onUpdate(context, manager, ids)
        }

        private fun render(context: Context): RemoteViews {
            val app = context.applicationContext as ShieldApp
            val summary = runCatching { app.store.get(SUMMARY)?.let(::JSONObject) }.getOrNull()
            val views = RemoteViews(context.packageName, R.layout.widget_shield)
            if (summary == null) {
                views.setTextViewText(R.id.widget_status, "")
                views.setTextViewText(R.id.widget_balance, context.getString(R.string.widget_set_up))
            } else {
                val waiting = summary.optInt("waiting")
                val declined = summary.optInt("declined")
                val status = buildList {
                    add(summary.optString("chain"))
                    if (waiting > 0) add(context.resources.getQuantityString(R.plurals.widget_waiting, waiting, waiting))
                    if (declined > 0) add(context.resources.getQuantityString(R.plurals.widget_declined, declined, declined))
                }.joinToString("  ·  ")
                views.setTextViewText(R.id.widget_status, status)
                val balances = summary.optString("balances")
                views.setTextViewText(
                    R.id.widget_balance,
                    when {
                        !showBalances(context) -> context.getString(R.string.widget_hidden)
                        balances.isEmpty() -> context.getString(R.string.widget_empty)
                        else -> balances
                    },
                )
            }
            views.setOnClickPendingIntent(android.R.id.background, open(context, CardActivity.intent(context), 0))
            views.setOnClickPendingIntent(R.id.widget_shield, open(context, CardActivity.intent(context, Tab.SHIELD), 1))
            views.setOnClickPendingIntent(R.id.widget_withdraw, open(context, CardActivity.intent(context, Tab.WITHDRAW), 2))
            views.setOnClickPendingIntent(R.id.widget_send, open(context, CardActivity.intent(context, Tab.SEND), 3))
            return views
        }

        private fun open(context: Context, intent: Intent, code: Int): PendingIntent =
            PendingIntent.getActivity(
                context,
                code,
                intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
    }
}
