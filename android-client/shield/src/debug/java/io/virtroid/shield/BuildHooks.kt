// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield

import android.content.Context
import io.virtroid.shield.engine.EngineException
import io.virtroid.shield.engine.WalletRequests
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.concurrent.TimeUnit

/**
 * Debug builds' local test mode, on when the test writes the harness's URL
 * (engine/test/harness/server.mjs, reached at 10.0.2.2 from an emulator)
 * into the app's files, and then either the test wallet's address (the
 * harness signs) or `walletconnect` (a wallet connects over WalletConnect,
 * engine/test/walletconnect):
 *   adb shell run-as io.virtroid.shield sh -c 'echo http://10.0.2.2:8099 > files/harness-url'
 *   adb shell run-as io.virtroid.shield sh -c 'echo 0x… > files/harness-owner'
 */
object BuildHooks {
    fun localTest(context: Context): LocalTest? {
        val url = File(context.filesDir, "harness-url").takeIf { it.isFile }?.readText()?.trim() ?: return null
        val owner = File(context.filesDir, "harness-owner").takeIf { it.isFile }?.readText()?.trim() ?: return null
        val walletConnect = owner == "walletconnect"
        return LocalTest(
            page = "engine/index-local.html",
            owner = owner.takeUnless { walletConnect },
            wallet = if (walletConnect) null else HarnessWallet(url),
            cleartextHosts = setOf("10.0.2.2"),
        )
    }

    /** The harness's test wallet, which signs and sends on the local chain. */
    private class HarnessWallet(private val url: String) : WalletRequests {
        private val http = OkHttpClient.Builder().readTimeout(120, TimeUnit.SECONDS).build()

        override suspend fun request(chainId: Int, method: String, params: JSONArray): Any? = withContext(Dispatchers.IO) {
            val body = JSONObject().put("m", "wallet").put("p", JSONObject().put("chainId", chainId).put("method", method).put("params", params))
            val request = Request.Builder().url("$url/host").post(body.toString().toRequestBody("application/json".toMediaType())).build()
            http.newCall(request).execute().use { response ->
                val answer = JSONObject(response.body!!.string())
                answer.optJSONObject("e")?.let { throw EngineException("wallet_error", it.optString("message")) }
                answer.opt("r").takeUnless { it == JSONObject.NULL }
            }
        }
    }
}
