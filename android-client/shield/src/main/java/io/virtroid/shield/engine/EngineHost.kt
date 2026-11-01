// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.engine

import android.annotation.SuppressLint
import android.content.Context
import android.os.Handler
import android.os.Looper
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebSettingsCompat
import androidx.webkit.WebViewAssetLoader
import androidx.webkit.WebViewClientCompat
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.util.concurrent.atomic.AtomicInteger

/**
 * The engine (0xbow's SDK, bundled in assets/engine) in a WebView nobody
 * sees, and the bridge to it (engine/src/bridge.ts has the message shapes).
 * The page comes from the app's assets and reaches nothing else: the engine
 * hands every request it makes to [HostCalls], which sends it if its host is
 * one the engine's chains name.
 */
class EngineHost(context: Context, private val calls: HostCalls, page: String = "engine/index.html") {
    private val main = Handler(Looper.getMainLooper())
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val nextId = AtomicInteger(1)
    private val pending = HashMap<Int, CompletableDeferred<Any?>>()
    private val ready = CompletableDeferred<Unit>()
    private var reply: JavaScriptReplyProxy? = null
    private val _events = MutableSharedFlow<EngineEvent>(extraBufferCapacity = 64)

    /** Progress the screens show: proving, a wallet prompt, a transaction confirming. */
    val events: SharedFlow<EngineEvent> = _events

    private val assets = WebViewAssetLoader.Builder()
        .setDomain(ORIGIN_HOST)
        .addPathHandler("/assets/", WebViewAssetLoader.AssetsPathHandler(context))
        .build()

    @SuppressLint("SetJavaScriptEnabled")
    private val webView = WebView(context.applicationContext).apply {
        settings.javaScriptEnabled = true
        settings.allowFileAccess = false
        settings.allowContentAccess = false
        settings.domStorageEnabled = false
        if (WebViewFeature.isFeatureSupported(WebViewFeature.SAFE_BROWSING_ENABLE)) {
            // Safe Browsing would send the page's URLs to Google; it's the app's own page.
            WebSettingsCompat.setSafeBrowsingEnabled(settings, false)
        }
        webViewClient = object : WebViewClientCompat() {
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse {
                // Only the app's own assets load; anything else is refused here.
                val url = request.url
                return (if (url.host == ORIGIN_HOST) assets.shouldInterceptRequest(url) else null) ?: refused()
            }

            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean = true
        }
    }

    init {
        check(WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) {
            "This WebView can't run the engine (no WebMessageListener)"
        }
        WebViewCompat.addWebMessageListener(webView, "shieldHost", setOf(ORIGIN)) { _, message, _, isMainFrame, replyProxy ->
            if (!isMainFrame) return@addWebMessageListener
            reply = replyProxy
            message.data?.let(::receive)
        }
        webView.loadUrl("$ORIGIN/assets/$page")
    }

    /** Asks the engine to do [method]; the result is what engine/src/app.ts returns, as org.json values. */
    suspend fun request(method: String, params: JSONObject = JSONObject()): Any? {
        ready.await()
        val id = nextId.getAndIncrement()
        val answer = CompletableDeferred<Any?>()
        withContext(Dispatchers.Main) {
            pending[id] = answer
            post(JSONObject().put("t", "req").put("id", id).put("m", method).put("p", params))
        }
        return answer.await()
    }

    private fun receive(raw: String) {
        val message = runCatching { JSONObject(raw) }.getOrNull() ?: return
        when (message.optString("t")) {
            "res" -> {
                val answer = pending.remove(message.optInt("id")) ?: return
                val error = message.optJSONObject("e")
                if (error != null) {
                    answer.completeExceptionally(EngineException(error.optString("code"), error.optString("message")))
                } else {
                    answer.complete(message.opt("r").takeUnless { it == JSONObject.NULL })
                }
            }
            "call" -> scope.launch {
                val id = message.optInt("id")
                val out = JSONObject().put("t", "ret").put("id", id)
                try {
                    out.put("r", calls.handle(message.optString("m"), message.optJSONObject("p") ?: JSONObject()) ?: JSONObject.NULL)
                } catch (error: Exception) {
                    val code = (error as? EngineException)?.code ?: "host_error"
                    out.put("e", JSONObject().put("code", code).put("message", error.message ?: error.javaClass.simpleName))
                }
                post(out)
            }
            "evt" -> {
                val name = message.optString("m")
                val params = message.optJSONObject("p") ?: JSONObject()
                if (name == "ready") {
                    calls.allowHosts(params.optJSONArray("hosts") ?: JSONArray())
                    ready.complete(Unit)
                }
                if (name == "log") calls.log(params) else _events.tryEmit(EngineEvent(name, params))
            }
        }
    }

    private fun post(message: JSONObject) {
        main.post { reply?.postMessage(message.toString()) }
    }

    private fun refused() = WebResourceResponse("text/plain", "utf-8", 403, "Forbidden", emptyMap(), ByteArrayInputStream(ByteArray(0)))

    companion object {
        /** androidx.webkit's reserved domain for app assets: never a real site. */
        const val ORIGIN_HOST = "appassets.androidplatform.net"
        const val ORIGIN = "https://$ORIGIN_HOST"
    }
}

data class EngineEvent(val name: String, val params: JSONObject)

/** An error the engine (or the app's side of the bridge) reports by code; see engine/src/bridge.ts. */
class EngineException(val code: String, message: String) : Exception(message)
