// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.widget

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.util.SizeF
import android.util.TypedValue
import android.view.View
import android.widget.RemoteViews
import androidx.annotation.ColorRes
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

        /**
         * One layout per size, and the launcher picks the one that fits: two rows or more get
         * [R.layout.widget_shield], one row the compact layout, and on a widget narrower than
         * the home screen the buttons drop their icons to keep their labels whole.
         */
        private fun render(context: Context): RemoteViews {
            val app = context.applicationContext as ShieldApp
            val summary = runCatching { app.store.get(SUMMARY)?.let(::JSONObject) }.getOrNull()
            val content = Content.of(context, summary, showBalances(context))
            return RemoteViews(
                mapOf(
                    SizeF(250f, 90f) to views(context, content, compact = true, icons = false),
                    SizeF(310f, 90f) to views(context, content, compact = true, icons = true),
                    SizeF(250f, 180f) to views(context, content, compact = false, icons = false),
                    SizeF(310f, 180f) to views(context, content, compact = false, icons = true),
                ),
            )
        }

        private fun views(context: Context, content: Content, compact: Boolean, icons: Boolean): RemoteViews {
            val views = RemoteViews(
                context.packageName,
                if (compact) R.layout.widget_shield_compact else R.layout.widget_shield,
            )
            val colour = { id: Int -> context.getColor(id) }
            if (compact) {
                // The one line on the right: what needs looking at, else the balance.
                val alert = content.alert
                views.setViewVisibility(R.id.widget_hidden_icon, visible(alert == null && content.hidden))
                views.setViewVisibility(R.id.widget_status_dot, visible(alert != null))
                views.setInt(R.id.widget_status_dot, "setColorFilter", colour(content.alertColour))
                views.setTextViewText(R.id.widget_balance, alert?.short ?: content.balance)
                views.setTextColor(
                    R.id.widget_balance,
                    colour(
                        when {
                            alert != null -> content.alertColour
                            content.chain == null || content.hidden -> R.color.text_secondary
                            else -> R.color.text
                        },
                    ),
                )
            } else {
                views.setViewVisibility(R.id.widget_chain, visible(content.chain != null))
                views.setTextViewText(R.id.widget_chain, content.chain)
                views.setViewVisibility(R.id.widget_label, visible(content.chain != null))
                views.setViewVisibility(R.id.widget_hidden_icon, visible(content.hidden))
                views.setTextViewText(R.id.widget_balance, content.balance)
                val alert = content.alert
                val status = alert?.long ?: content.detail
                views.setViewVisibility(R.id.widget_status_row, visible(status != null))
                views.setViewVisibility(R.id.widget_status_dot, visible(alert != null))
                views.setInt(R.id.widget_status_dot, "setColorFilter", colour(content.alertColour))
                views.setTextViewText(R.id.widget_status, status)
                views.setTextColor(
                    R.id.widget_status,
                    colour(if (alert != null) content.alertColour else R.color.text_secondary),
                )
            }

            // Before set-up there is nothing to withdraw or send: one button, which sets up.
            val setUp = content.chain == null
            views.setViewVisibility(R.id.widget_withdraw, visible(!setUp))
            views.setViewVisibility(R.id.widget_send, visible(!setUp))
            views.setTextViewText(
                R.id.widget_shield_label,
                context.getString(if (setUp) R.string.widget_set_up_action else R.string.shield),
            )
            views.setImageViewResource(
                R.id.widget_shield_icon,
                if (setUp) R.drawable.widget_ic_wallet else R.drawable.widget_ic_shield,
            )
            listOf(R.id.widget_shield_icon, R.id.widget_withdraw_icon, R.id.widget_send_icon)
                .forEach { views.setViewVisibility(it, visible(icons || setUp)) }
            // Without icons the pills are narrower too: a size down keeps "Withdraw" off their edges.
            if (!icons) {
                listOf(R.id.widget_shield_label, R.id.widget_withdraw_label, R.id.widget_send_label)
                    .forEach { views.setTextViewTextSize(it, TypedValue.COMPLEX_UNIT_SP, 13f) }
            }

            views.setOnClickPendingIntent(android.R.id.background, open(context, CardActivity.intent(context), 0))
            views.setOnClickPendingIntent(R.id.widget_shield, open(context, CardActivity.intent(context, Tab.SHIELD), 1))
            views.setOnClickPendingIntent(R.id.widget_withdraw, open(context, CardActivity.intent(context, Tab.WITHDRAW), 2))
            views.setOnClickPendingIntent(R.id.widget_send, open(context, CardActivity.intent(context, Tab.SEND), 3))
            return views
        }

        private fun visible(shown: Boolean): Int = if (shown) View.VISIBLE else View.GONE

        private fun open(context: Context, intent: Intent, code: Int): PendingIntent =
            PendingIntent.getActivity(
                context,
                code,
                intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
    }
}

/** What the widget shows, whatever its size; null [chain] means Shield isn't set up yet. */
private class Content(
    val chain: String?,
    val balance: String,
    val hidden: Boolean,
    /** The line under the balance when nothing needs looking at (before set-up, what to do). */
    val detail: String?,
    val alert: Alert?,
    @param:ColorRes val alertColour: Int,
) {
    /** Deposits that need looking at, said in full and, for one row, in short. */
    class Alert(val long: String, val short: String)

    companion object {
        fun of(context: Context, summary: JSONObject?, showBalances: Boolean): Content {
            if (summary == null) {
                return Content(
                    chain = null,
                    balance = context.getString(R.string.widget_set_up),
                    hidden = false,
                    detail = context.getString(R.string.widget_set_up_detail),
                    alert = null,
                    alertColour = R.color.text_secondary,
                )
            }
            val resources = context.resources
            val waiting = summary.optInt("waiting")
            val declined = summary.optInt("declined")
            val waitingShort = resources.getQuantityString(R.plurals.widget_waiting_short, waiting, waiting)
            val declinedShort = resources.getQuantityString(R.plurals.widget_declined_short, declined, declined)
            val both = context.getString(R.string.widget_status_both, declinedShort, waitingShort)
            val alert = when {
                declined > 0 && waiting > 0 -> Alert(both, both)
                declined > 0 -> Alert(resources.getQuantityString(R.plurals.widget_declined, declined, declined), declinedShort)
                waiting > 0 -> Alert(resources.getQuantityString(R.plurals.widget_waiting, waiting, waiting), waitingShort)
                else -> null
            }
            val balances = summary.optString("balances")
            return Content(
                chain = summary.optString("chain"),
                balance = when {
                    !showBalances -> context.getString(R.string.widget_hidden)
                    balances.isEmpty() -> context.getString(R.string.widget_empty)
                    else -> balances
                },
                hidden = !showBalances,
                detail = null,
                alert = alert,
                // A declined deposit needs the user (its funds come back only when they exit it).
                alertColour = if (declined > 0) R.color.error else R.color.warning,
            )
        }
    }
}
