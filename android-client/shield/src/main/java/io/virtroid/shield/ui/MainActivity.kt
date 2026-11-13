// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.addCallback
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import io.virtroid.shield.R
import io.virtroid.shield.watch.WatchService

/** Shield's one window: set-up, the address and balance, and sending out. */
class MainActivity : ComponentActivity() {
    private val model: ShieldModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContent {
            ShieldTheme {
                Screens(
                    model = model,
                    copy = ::copy,
                    paste = ::paste,
                    openAbout = { startActivity(Intent(this, AboutActivity::class.java)) },
                )
            }
        }
        onBackPressedDispatcher.addCallback(this) { if (!model.back()) finish() }
        handle(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handle(intent)
    }

    override fun onStart() {
        super.onStart()
        model.load()
        WatchService.start(this)
    }

    // Only Shield itself can start the SendOut alias; what other apps put on
    // an intent for this activity opens nothing.
    private fun handle(intent: Intent?) {
        if (intent?.component?.className == SEND_OUT) model.openSendOut()
    }

    private fun copy(text: String) {
        val clip = ClipData.newPlainText(getString(R.string.app_name), text)
        getSystemService(ClipboardManager::class.java).setPrimaryClip(clip)
        Toast.makeText(this, R.string.copied, Toast.LENGTH_SHORT).show()
    }

    private fun paste(): String? =
        getSystemService(ClipboardManager::class.java).primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.toString()

    companion object {
        // The manifest's not-exported alias for this activity.
        private const val SEND_OUT = "io.virtroid.shield.ui.SendOut"

        fun intent(context: Context, sendOut: Boolean = false): Intent =
            if (sendOut) Intent().setClassName(context, SEND_OUT) else Intent(context, MainActivity::class.java)
    }
}
