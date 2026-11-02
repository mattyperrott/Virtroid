// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.File

/**
 * Debug builds only: has the local test stack's mock screening approve every
 * deposit the account is waiting on, or with `--es verdict decline` approve
 * and then revoke them (the SDK only declines a deposit it had approved),
 * for trying the card's flows by hand. Shield must be in the foreground, or
 * Android cuts its network:
 *   adb shell am broadcast -n io.virtroid.shield/.DebugScreening [--es verdict decline]
 */
class DebugScreening : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val app = context.applicationContext as ShieldApp
        if (app.localTest == null) return
        val decline = intent.getStringExtra("verdict") == "decline"
        app.scope.launch {
            try {
                val harness = File(context.filesDir, "harness-url").readText().trim()
                val ready = app.shield.refresh() as AccountState.Ready
                val labels = ready.balances.flatMap { it.waiting }.mapNotNull { it.label }
                for (label in labels) control(harness, "approve", label)
                app.shield.refresh()
                if (decline) {
                    for (label in labels) control(harness, "revoke", label)
                    app.shield.refresh()
                }
            } catch (error: Exception) {
                Log.w("ShieldScreening", "failed: ${error.message}")
            }
        }
    }

    private suspend fun control(harness: String, method: String, label: String) = withContext(Dispatchers.IO) {
        val body = JSONObject().put("m", method).put("p", JSONObject().put("label", label)).toString()
        val request = Request.Builder().url("$harness/control").post(body.toRequestBody("application/json".toMediaType())).build()
        OkHttpClient().newCall(request).execute().use { response -> Log.i("ShieldScreening", "$method $label: ${response.code}") }
    }
}
