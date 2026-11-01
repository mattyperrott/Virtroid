// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.engine

import android.content.Context
import android.util.Base64
import androidx.core.net.toUri
import io.virtroid.shield.store.SecureStore
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.Headers
import okhttp3.MediaType.Companion.toMediaTypeOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/** The user's wallet, as the engine reaches it: WalletConnect, in the app. */
fun interface WalletRequests {
    suspend fun request(chainId: Int, method: String, params: JSONArray): Any?
}

/**
 * What the engine asks of the app (engine/src: net.ts, storage.ts,
 * wallet.ts, main.ts's cache). Requests go out with OkHttp over the cloud
 * phone's own network, so Tor carries them when it's on, and only to hosts
 * the engine's chains name; nothing identifying is added.
 */
class HostCalls(
    context: Context,
    private val store: SecureStore,
    private val wallet: WalletRequests,
    /** Hosts a debug build's local test reaches over plain HTTP; empty otherwise. */
    private val cleartextHosts: Set<String> = emptySet(),
) {
    private val cacheDir = File(context.cacheDir, "circuits").apply { mkdirs() }
    private val log = EngineLog
    @Volatile private var allowed: Set<String> = emptySet()

    private val http = OkHttpClient.Builder()
        .connectTimeout(30, TimeUnit.SECONDS)
        .readTimeout(120, TimeUnit.SECONDS)
        .followRedirects(false)
        .retryOnConnectionFailure(true)
        .build()

    fun allowHosts(hosts: JSONArray) {
        allowed = (0 until hosts.length()).map { hosts.getString(it).lowercase() }.toSet()
    }

    fun log(params: JSONObject) = log.add(params.optString("level"), params.optString("service"), params.optString("msg"))

    suspend fun handle(method: String, p: JSONObject): Any? = when (method) {
        "http" -> http(p)
        "store.get" -> withContext(Dispatchers.IO) { store.get(p.getString("key")) ?: JSONObject.NULL }
        "store.set" -> withContext(Dispatchers.IO) { store.set(p.getString("key"), p.getString("value")); null }
        "store.delete" -> withContext(Dispatchers.IO) { store.delete(p.getString("key")); null }
        "store.clear" -> withContext(Dispatchers.IO) { store.clear(p.getString("prefix")); null }
        "cache.get" -> withContext(Dispatchers.IO) { cacheFile(p.getString("key")).takeIf { it.isFile }?.readText() ?: JSONObject.NULL }
        "cache.set" -> withContext(Dispatchers.IO) { cacheFile(p.getString("key")).writeText(p.getString("value")); null }
        "wallet" -> wallet.request(p.getInt("chainId"), p.getString("method"), p.optJSONArray("params") ?: JSONArray())
        else -> throw EngineException("no_method", "The app has no $method")
    }

    private suspend fun http(p: JSONObject): JSONObject = withContext(Dispatchers.IO) {
        val url = p.getString("url")
        val parsed = url.toUri()
        val host = parsed.host?.lowercase() ?: throw EngineException("blocked", "No host in $url")
        val authority = if (parsed.port > 0) "$host:${parsed.port}" else host
        val scheme = parsed.scheme
        val secure = scheme == "https" || (scheme == "http" && host in cleartextHosts)
        if (!secure || (host !in allowed && authority !in allowed)) {
            throw EngineException("blocked", "The app doesn't send requests to $authority")
        }
        val headers = Headers.Builder()
        p.optJSONObject("headers")?.let { h ->
            for (name in h.keys()) if (name.lowercase() !in DROPPED_HEADERS) headers.add(name, h.getString(name))
        }
        val method = p.optString("method", "GET")
        val body = p.optString("body").takeIf { it.isNotEmpty() && !p.isNull("body") }?.let {
            Base64.decode(it, Base64.NO_WRAP).toRequestBody(headers["content-type"]?.toMediaTypeOrNull())
        }
        val request = Request.Builder().url(url).headers(headers.build()).method(method, body).build()
        http.newCall(request).execute().use { response ->
            val out = JSONObject()
            response.headers.forEach { (name, value) -> out.put(name, value) }
            JSONObject()
                .put("status", response.code)
                .put("headers", out)
                .put("body", Base64.encodeToString(response.body?.bytes() ?: ByteArray(0), Base64.NO_WRAP))
        }
    }

    private fun cacheFile(key: String): File {
        val digest = MessageDigest.getInstance("SHA-256").digest(key.toByteArray())
        return File(cacheDir, digest.joinToString("") { "%02x".format(it) })
    }

    private companion object {
        // Set by OkHttp itself, or nothing a server needs to see.
        val DROPPED_HEADERS = setOf("host", "content-length", "connection", "user-agent", "origin", "referer", "cookie")
    }
}

/**
 * The engine's warnings and errors, kept in memory for the app's log screen
 * and never written to logcat: on a cloud phone, logs can leave the device.
 */
object EngineLog {
    private const val LIMIT = 200
    private val lines = ArrayDeque<String>()

    @Synchronized
    fun add(level: String, service: String, message: String) {
        lines.addLast("${System.currentTimeMillis()} $level $service: $message")
        while (lines.size > LIMIT) lines.removeFirst()
    }

    @Synchronized
    fun snapshot(): List<String> = lines.toList()
}
