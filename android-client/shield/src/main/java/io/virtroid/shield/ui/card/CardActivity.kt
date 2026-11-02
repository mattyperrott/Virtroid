// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui.card

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.addCallback
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import androidx.core.net.toUri
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import io.virtroid.shield.ShieldApp
import kotlinx.coroutines.launch

/**
 * The transaction card, a window over whatever is behind it: the home screen
 * when the widget opens it (it grows out of the widget), or Shield's own
 * screen. Closing it, by Back on the first step, a tap outside or Done,
 * shows what was behind it again.
 */
class CardActivity : ComponentActivity() {
    private val model: CardModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        model.start(requestedTab(intent))
        setContent {
            ShieldTheme {
                CardScreen(model, onClose = ::finish, openLink = ::openLink)
            }
        }
        onBackPressedDispatcher.addCallback(this) { if (!model.back()) finish() }
        // Bring the wallet forward when it's on this phone and has something to approve.
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                (application as ShieldApp).walletConnect.prompts.collect { session -> session.walletLink?.let(::openLink) }
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        model.start(requestedTab(intent))
    }

    private fun openLink(link: String): Boolean = try {
        startActivity(Intent(Intent.ACTION_VIEW, link.toUri()).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        true
    } catch (_: ActivityNotFoundException) {
        false
    }

    companion object {
        private const val EXTRA_TAB = "tab"

        private fun requestedTab(intent: Intent?): Tab? =
            intent?.getStringExtra(EXTRA_TAB)?.let { name -> Tab.entries.firstOrNull { it.name == name } }

        /** Opens the card on `tab`, or on its own choice (Send, or Shield when nothing is shielded yet). */
        fun intent(context: Context, tab: Tab? = null): Intent =
            Intent(context, CardActivity::class.java).apply { if (tab != null) putExtra(EXTRA_TAB, tab.name) }
    }
}
