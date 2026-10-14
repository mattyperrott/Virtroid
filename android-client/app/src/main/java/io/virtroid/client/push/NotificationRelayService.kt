package io.virtroid.client.push

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import io.virtroid.client.LauncherActivity
import io.virtroid.client.R
import io.virtroid.client.data.AppLogLevel
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.security.DeviceIdentityStore
import io.virtroid.client.security.PushEncryptionKeyStore
import io.virtroid.client.security.TlsPins
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import okhttp3.Call
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.IOException
import java.time.Instant
import java.util.Locale
import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.TimeUnit

class NotificationRelayService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val identityStore by lazy { NotificationRelayIdentityStore(this) }
    private val signingStore = DeviceIdentityStore()
    private val relayKeyStore = NotificationRelayKeyStore()
    private val encryptionStore by lazy { PushEncryptionKeyStore(this) }
    private val logs by lazy { AppLogStore.get(this) }
    private var streamJob: Job? = null

    @Volatile
    private var activeCall: Call? = null
    private val intentionalReconnect = AtomicBoolean(false)

    override fun onCreate() {
        super.onCreate()
        createChannels()
        ServiceCompat.startForeground(
            this,
            STATUS_NOTIFICATION_ID,
            statusNotification(),
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                ServiceInfo.FOREGROUND_SERVICE_TYPE_REMOTE_MESSAGING
            } else {
                0
            },
        )
        startStreamLoop()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_RECONNECT) {
            activeCall?.let { call ->
                intentionalReconnect.set(true)
                call.cancel()
            }
        }
        startStreamLoop()
        return START_STICKY
    }

    override fun onDestroy() {
        activeCall?.cancel()
        streamJob?.cancel()
        scope.coroutineContext[Job]?.cancel()
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun startStreamLoop() {
        if (streamJob?.isActive == true) return
        streamJob = scope.launch {
            var retryDelayMs = INITIAL_RETRY_DELAY_MS
            var consecutiveFailures = 0
            while (currentCoroutineContext().isActive) {
                val identity = identityStore.load()
                if (identity == null) {
                    stopSelf()
                    return@launch
                }
                runCatching { consumeStream(identity) }
                    .onSuccess {
                        retryDelayMs = INITIAL_RETRY_DELAY_MS
                        consecutiveFailures = 0
                    }
                    .onFailure { error ->
                        val expectedReconnect = intentionalReconnect.getAndSet(false)
                        if (currentCoroutineContext().isActive && error !is InterruptedException && !expectedReconnect) {
                            consecutiveFailures++
                            if (consecutiveFailures == 3 || (consecutiveFailures > 3 && consecutiveFailures % 10 == 0)) {
                                logs.warn("Notification relay reconnecting: ${error.message}", "notifications")
                            }
                        }
                    }
                if (!currentCoroutineContext().isActive) return@launch
                delay(retryDelayMs)
                retryDelayMs = (retryDelayMs * 2).coerceAtMost(MAX_RETRY_DELAY_MS)
            }
        }
    }

    private fun consumeStream(identity: NotificationRelayIdentityStore.RelayIdentity) {
        val path = "/api/v1/me/notification-stream"
        val request = signedRequest(identity, path, "GET")
        val call = STREAM_CLIENT.newCall(request)
        activeCall = call
        try {
            call.execute().use { response ->
                if (!response.isSuccessful) throw IOException("stream HTTP ${response.code}")
                val contentType = response.header("Content-Type").orEmpty().lowercase()
                if (!contentType.startsWith("text/event-stream")) throw IOException("unexpected stream response")
                val source = response.body?.source() ?: throw IOException("empty notification stream")
                while (!source.exhausted()) {
                    val line = source.readUtf8Line() ?: break
                    if (line.startsWith(SSE_DATA_PREFIX)) {
                        processDelivery(identity, line.removePrefix(SSE_DATA_PREFIX))
                    }
                }
            }
        } finally {
            if (activeCall === call) activeCall = null
        }
    }

    private fun processDelivery(identity: NotificationRelayIdentityStore.RelayIdentity, raw: String) {
        val delivery = runCatching {
            val json = JSONObject(raw)
            val deliveryId = UUID.fromString(json.getString("delivery_id")).toString()
            val event = parseEvent(encryptionStore.decryptEnvelope(json.getString("envelope")))
            deliveryId to event
        }.onFailure { error ->
            logs.warn("Rejected encrypted runtime notification: ${error.message}", "notifications")
        }.getOrNull() ?: return

        val event = delivery.second
        val alreadyShown = hasSeenEvent(event.eventId)
        if (!alreadyShown) {
            when (event) {
                is RuntimeRelayEvent -> {
                    if (!canPostNotifications() || !showNotification(event)) return
                }
                is SecurityRelayEvent -> recordSecurityNotice(event)
            }
            rememberEvent(event.eventId)
        }
        acknowledge(identity, delivery.first)
    }

    private fun acknowledge(identity: NotificationRelayIdentityStore.RelayIdentity, deliveryId: String) {
        val path = "/api/v1/me/notification-deliveries/$deliveryId/ack"
        val request = signedRequest(identity, path, "POST", "{}")
        ACK_CLIENT.newCall(request).execute().use { response ->
            if (!response.isSuccessful) throw IOException("ack HTTP ${response.code}")
        }
    }

    private fun signedRequest(
        identity: NotificationRelayIdentityStore.RelayIdentity,
        path: String,
        method: String,
        body: String = "",
    ): Request {
        val bodyBytes = body.toByteArray(Charsets.UTF_8)
        val requestBody = body.takeIf { it.isNotEmpty() }?.toRequestBody(JSON_MEDIA_TYPE)
        return Request.Builder()
            .url(identity.baseUrl + path)
            .method(method, requestBody)
            .apply {
                // The device key refuses to sign while the phone is locked;
                // the relay key, registered for these two requests only, can.
                runCatching {
                    signingStore.signedHeaders(
                        method = method,
                        requestUri = path,
                        accountId = identity.accountId,
                        deviceId = identity.deviceId,
                        body = bodyBytes,
                    )
                }.getOrElse {
                    relayKeyStore.signedHeaders(
                        method = method,
                        requestUri = path,
                        accountId = identity.accountId,
                        deviceId = identity.deviceId,
                        body = bodyBytes,
                    )
                }.forEach { (name, value) -> header(name, value) }
            }
            .build()
    }

    private fun showNotification(event: RuntimeRelayEvent): Boolean {
        val manager = getSystemService(NotificationManager::class.java) ?: return false
        val intent = Intent()
            .setComponent(ComponentName(this, LauncherActivity::class.java))
            .setPackage(packageName)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
        val pendingIntent = PendingIntent.getActivity(
            this,
            event.eventId.hashCode(),
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        // On a secure lock screen, only that a notification arrived: the app
        // and title show once the phone is unlocked.
        val publicVersion = Notification.Builder(this, EVENTS_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_phone)
            .setContentTitle(getString(R.string.notification_sealed_title))
            .setWhen(event.postedAt.toEpochMilli())
            .setShowWhen(true)
            .build()
        val notification = Notification.Builder(this, EVENTS_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_phone)
            .setContentTitle(event.appLabel)
            .setContentText(event.title.ifBlank { getString(R.string.notification_new) })
            .apply { if (event.packageName.isNotBlank()) setSubText(event.packageName) }
            .setWhen(event.postedAt.toEpochMilli())
            .setShowWhen(true)
            .setAutoCancel(true)
            .setCategory(Notification.CATEGORY_MESSAGE)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setPublicVersion(publicVersion)
            .setContentIntent(pendingIntent)
            .build()
        manager.notify(event.eventId.hashCode(), notification)
        return true
    }

    private fun parseEvent(raw: String): RelayEvent {
        val json = JSONObject(raw)
        if (json.getInt("version") == SEALED_EVENT_VERSION) {
            return parseSealedRuntimeEvent(json)
        }
        require(json.getInt("version") == 1)
        return if (json.optString("kind") == SECURITY_EVENT_KIND) {
            parseSecurityEvent(json)
        } else {
            parseRuntimeEvent(json)
        }
    }

    /**
     * A runtime event its VM sealed with the runtime's notification key, so
     * the control plane never read it. Without that key on this phone, it
     * still says a notification arrived, without what it was.
     */
    private fun parseSealedRuntimeEvent(json: JSONObject): RuntimeRelayEvent {
        val keys = buildSet {
            val iterator = json.keys()
            while (iterator.hasNext()) add(iterator.next())
        }
        require(keys == EXPECTED_SEALED_EVENT_KEYS)
        val eventId = UUID.fromString(json.getString("event_id")).toString()
        val runtimeId = UUID.fromString(json.getString("runtime_id")).toString()
        val sealed = android.util.Base64.decode(json.getString("sealed"), android.util.Base64.NO_WRAP)
        val opened = RuntimeNotificationKeys(this).open(runtimeId, eventId, sealed)
            ?: return RuntimeRelayEvent(eventId, "", getString(R.string.notification_sealed_title), Instant.now(), "")
        val inner = JSONObject(opened).put("version", 1)
        val event = parseRuntimeEvent(inner)
        require(event.eventId == eventId)
        return event
    }

    private fun parseRuntimeEvent(json: JSONObject): RuntimeRelayEvent {
        val keys = buildSet {
            val iterator = json.keys()
            while (iterator.hasNext()) add(iterator.next())
        }
        require(keys == EXPECTED_RUNTIME_EVENT_KEYS)
        val eventId = UUID.fromString(json.getString("event_id")).toString()
        val packageName = json.getString("package_name")
        val appLabel = json.getString("app_label")
        val title = json.getString("title")
        val postedAt = Instant.parse(json.getString("posted_at"))
        require(packageName.length <= MAX_PACKAGE_CHARS && packageName.matches(PACKAGE_PATTERN))
        require(appLabel.isNotBlank() && appLabel.codePointCount(0, appLabel.length) <= MAX_APP_LABEL_CHARS)
        require(title.codePointCount(0, title.length) <= MAX_TITLE_CHARS)
        require(isFreshEventTime(postedAt))
        return RuntimeRelayEvent(eventId, packageName, appLabel, postedAt, title)
    }

    private fun parseSecurityEvent(json: JSONObject): SecurityRelayEvent {
        val keys = buildSet {
            val iterator = json.keys()
            while (iterator.hasNext()) add(iterator.next())
        }
        require(keys == EXPECTED_SECURITY_EVENT_KEYS)
        val eventId = UUID.fromString(json.getString("event_id")).toString()
        val source = json.getString("source")
        val severity = json.getString("severity")
        val summary = json.getString("summary")
        val observedAt = Instant.parse(json.getString("observed_at"))
        require(source in SECURITY_SOURCES)
        require(severity in SECURITY_SEVERITIES)
        require(summary.isNotBlank() && summary.codePointCount(0, summary.length) <= MAX_SECURITY_SUMMARY_CHARS)
        require('\n' !in summary && '\r' !in summary)
        require(isFreshEventTime(observedAt))
        return SecurityRelayEvent(eventId, source, severity, summary, observedAt)
    }

    private fun isFreshEventTime(value: Instant): Boolean {
        return value.isAfter(Instant.now().minusSeconds(7 * 24 * 60 * 60L)) &&
            value.isBefore(Instant.now().plusSeconds(5 * 60L))
    }

    private fun recordSecurityNotice(event: SecurityRelayEvent) {
        val message = "${event.summary} · ${event.source.uppercase(Locale.ROOT)}"
        val level = when (event.severity) {
            "critical" -> AppLogLevel.CRITICAL
            "warning" -> AppLogLevel.WARN
            else -> AppLogLevel.SECURITY
        }
        logs.logCoalesced(
            level = level,
            message = message,
            source = "security",
            timestampMs = event.observedAt.toEpochMilli(),
            windowMs = SECURITY_NOTICE_COALESCE_WINDOW_MS,
        )
    }

    private fun canPostNotifications(): Boolean {
        return Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
    }

    private fun hasSeenEvent(eventId: String): Boolean =
        getSharedPreferences(DEDUP_PREFS, MODE_PRIVATE)
            .getStringSet(DEDUP_KEY, emptySet())
            .orEmpty()
            .contains(eventId)

    private fun rememberEvent(eventId: String) {
        val prefs = getSharedPreferences(DEDUP_PREFS, MODE_PRIVATE)
        val existing = prefs.getStringSet(DEDUP_KEY, emptySet()).orEmpty()
        prefs.edit()
            .putStringSet(DEDUP_KEY, (existing + eventId).toList().takeLast(MAX_DEDUP_EVENTS).toSet())
            .apply()
    }

    private fun createChannels() {
        val manager = getSystemService(NotificationManager::class.java) ?: return
        if (manager.getNotificationChannel(STATUS_CHANNEL_ID) == null) {
            manager.createNotificationChannel(
                NotificationChannel(STATUS_CHANNEL_ID, "Notification relay", NotificationManager.IMPORTANCE_LOW).apply {
                    description = "Keeps Virtroid connected for runtime notification delivery"
                    setShowBadge(false)
                },
            )
        }
        if (manager.getNotificationChannel(EVENTS_CHANNEL_ID) == null) {
            manager.createNotificationChannel(
                NotificationChannel(EVENTS_CHANNEL_ID, "Runtime notifications", NotificationManager.IMPORTANCE_DEFAULT).apply {
                    description = "Metadata-only notifications forwarded from Virtroid runtimes"
                },
            )
        }
    }

    private fun statusNotification(): Notification {
        val intent = Intent()
            .setComponent(ComponentName(this, LauncherActivity::class.java))
            .setPackage(packageName)
        val pendingIntent = PendingIntent.getActivity(
            this,
            STATUS_NOTIFICATION_ID,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, STATUS_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_phone)
            .setContentTitle("Virtroid notification relay")
            .setContentText("Listening for metadata-only runtime notifications")
            .setCategory(Notification.CATEGORY_SERVICE)
            .setOngoing(true)
            .setShowWhen(false)
            .setContentIntent(pendingIntent)
            .build()
    }

    private sealed interface RelayEvent {
        val eventId: String
    }

    private data class RuntimeRelayEvent(
        override val eventId: String,
        val packageName: String,
        val appLabel: String,
        val postedAt: Instant,
        val title: String,
    ) : RelayEvent

    private data class SecurityRelayEvent(
        override val eventId: String,
        val source: String,
        val severity: String,
        val summary: String,
        val observedAt: Instant,
    ) : RelayEvent

    companion object {
        private const val ACTION_RECONNECT = "io.virtroid.client.NOTIFICATION_RELAY_RECONNECT"
        private const val STATUS_CHANNEL_ID = "virtroid_notification_relay_status"
        private const val EVENTS_CHANNEL_ID = "virtroid_runtime_notifications"
        private const val STATUS_NOTIFICATION_ID = 2401
        private const val SSE_DATA_PREFIX = "data: "
        private const val DEDUP_PREFS = "virtroid-notification-dedup"
        private const val DEDUP_KEY = "event_ids"
        private const val MAX_DEDUP_EVENTS = 200
        private const val SECURITY_NOTICE_COALESCE_WINDOW_MS = 5 * 60 * 1_000L
        private const val MAX_PACKAGE_CHARS = 255
        private const val MAX_APP_LABEL_CHARS = 100
        private const val MAX_TITLE_CHARS = 200
        private const val MAX_SECURITY_SUMMARY_CHARS = 200
        private const val SECURITY_EVENT_KIND = "security_notice"
        private const val INITIAL_RETRY_DELAY_MS = 2_000L
        private const val MAX_RETRY_DELAY_MS = 60_000L
        private val PACKAGE_PATTERN = Regex("^[A-Za-z][A-Za-z0-9_]*(\\.[A-Za-z][A-Za-z0-9_]*)+$")
        private const val SEALED_EVENT_VERSION = 2
        private val EXPECTED_SEALED_EVENT_KEYS = setOf("version", "event_id", "runtime_id", "sealed")
        private val EXPECTED_RUNTIME_EVENT_KEYS = setOf(
            "version",
            "event_id",
            "package_name",
            "app_label",
            "posted_at",
            "title",
        )
        private val EXPECTED_SECURITY_EVENT_KEYS = setOf(
            "version",
            "kind",
            "event_id",
            "source",
            "severity",
            "summary",
            "observed_at",
        )
        private val SECURITY_SOURCES = setOf("falco", "suricata")
        private val SECURITY_SEVERITIES = setOf("notice", "warning", "critical")
        private val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()
        private val STREAM_CLIENT = OkHttpClient.Builder()
            .certificatePinner(TlsPins.certificatePinner())
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .callTimeout(0, TimeUnit.MILLISECONDS)
            .build()
        private val ACK_CLIENT = OkHttpClient.Builder()
            .certificatePinner(TlsPins.certificatePinner())
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS)
            .callTimeout(45, TimeUnit.SECONDS)
            .build()

        /**
         * Starts the relay as a foreground service. Android 12+ refuses that while
         * the app is in the background, e.g. when its process starts after a reboot
         * or an update; the relay then starts the next time the app is opened.
         */
        fun start(context: Context, action: String? = null): Boolean {
            val appContext = context.applicationContext
            val intent = Intent(appContext, NotificationRelayService::class.java)
            if (action != null) intent.action = action
            return try {
                ContextCompat.startForegroundService(appContext, intent)
                true
            } catch (error: IllegalStateException) {
                AppLogStore.get(appContext).warn(
                    "Notification relay start deferred until the app is opened: ${error.message}",
                    "notifications",
                )
                false
            }
        }

        fun reconnect(context: Context) {
            start(context, ACTION_RECONNECT)
        }
    }
}
