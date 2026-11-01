// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import kotlinx.coroutines.launch
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.File
import java.math.BigInteger

/**
 * Debug builds only: runs engine/test/harness/scenario.js's steps through
 * the app's own code (the engine in the WebView, the bridge, the sealed
 * store, Shield) in local test mode, logging each step:
 *   adb shell am broadcast -n io.virtroid.shield/.DebugScenario
 *   adb logcat -s ShieldScenario
 */
class DebugScenario : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val app = context.applicationContext as ShieldApp
        val test = app.localTest ?: return log(context, "FAIL local test mode is off")
        val pending = goAsync()
        app.scope.launch {
            val shield = app.shield
            suspend fun step(name: String, block: suspend () -> Any?) {
                val started = System.currentTimeMillis()
                val result = block()
                log(context, "ok   $name (${System.currentTimeMillis() - started} ms) ${result ?: ""}".take(400))
            }
            try {
                val harness = File(context.filesDir, "harness-url").readText().trim()
                step("load: ${test.owner}") { shield.load() }
                if (shield.state.value is AccountState.NeedsSetup) step("set up") { shield.setUp(test.owner) }
                step("shield 100 USDC") { shield.shield("USDC", BigInteger("100000000")) }
                approveWaiting(harness, shield)
                step("spendable USDC") { (shield.refresh() as AccountState.Ready).balances.first { it.symbol == "USDC" }.spendable }
                val fresh = control(harness, "fresh").getString("r")
                step("quote 25 USDC withdrawal") { shield.quoteWithdraw("USDC", BigInteger("25000000"), fresh) }
                step("confirm") { shield.confirm() }
                step("fresh address USDC") { control(harness, "balance", JSONObject().put("address", fresh).put("token", shield.chain().assets.first { it.symbol == "USDC" }.token)).get("r") }
                step("quote private send of max USDC to self") { shield.quoteSend("USDC", null, test.owner) }
                step("confirm") { shield.confirm() }
                step("final") { (shield.refresh() as AccountState.Ready).balances.joinToString { "${it.symbol}=${it.spendable}" } }
                log(context, "ok   scenario finished")
            } catch (error: Exception) {
                log(context, "FAIL ${error.javaClass.simpleName}: ${error.message}")
            } finally {
                pending.finish()
            }
        }
    }

    private suspend fun approveWaiting(harness: String, shield: Shield) {
        val ready = shield.refresh() as AccountState.Ready
        for (deposit in ready.balances.flatMap { it.waiting }) {
            control(harness, "approve", JSONObject().put("label", deposit.label))
        }
    }

    private suspend fun control(harness: String, method: String, params: JSONObject = JSONObject()): JSONObject =
        kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
            val body = JSONObject().put("m", method).put("p", params).toString().toRequestBody("application/json".toMediaType())
            OkHttpClient().newCall(Request.Builder().url("$harness/control").post(body).build()).execute().use { JSONObject(it.body!!.string()) }
        }

    private fun log(context: Context, line: String) {
        Log.i("ShieldScenario", line)
        File(context.filesDir, "scenario.log").appendText(line + "\n")
    }
}
