package io.virtroid.runtimeagent

import android.app.job.JobInfo
import android.app.job.JobScheduler
import android.content.ComponentName
import android.content.Context
import org.json.JSONArray
import org.json.JSONObject

internal data class RuntimeNotificationEvent(
    val eventId: String,
    val packageName: String,
    val appLabel: String,
    val postedAt: String,
    val title: String,
) {
    fun toJson(): JSONObject = JSONObject()
        .put("event_id", eventId)
        .put("package_name", packageName)
        .put("app_label", appLabel)
        .put("posted_at", postedAt)
        .put("title", title)

    companion object {
        fun fromJson(value: JSONObject): RuntimeNotificationEvent = RuntimeNotificationEvent(
            eventId = value.getString("event_id"),
            packageName = value.getString("package_name"),
            appLabel = value.getString("app_label"),
            postedAt = value.getString("posted_at"),
            title = value.optString("title"),
        )
    }
}

internal class NotificationQueue(private val context: Context) {
    private val prefs = SecureAgentPrefs(context)

    fun enqueue(event: RuntimeNotificationEvent) {
        synchronized(QUEUE_LOCK) {
            val current = readLocked().filterNot { it.eventId == event.eventId }.toMutableList()
            current += event
            writeLocked(current.takeLast(MAX_PENDING_EVENTS))
        }
        scheduleUpload()
    }

    fun read(): List<RuntimeNotificationEvent> = synchronized(QUEUE_LOCK) { readLocked() }

    fun remove(eventId: String) = synchronized(QUEUE_LOCK) {
        writeLocked(readLocked().filterNot { it.eventId == eventId })
    }

    private fun readLocked(): List<RuntimeNotificationEvent> {
        val raw = prefs.get(KEY_QUEUE) ?: return emptyList()
        return runCatching {
            val array = JSONArray(raw)
            List(array.length()) { index -> RuntimeNotificationEvent.fromJson(array.getJSONObject(index)) }
        }.getOrDefault(emptyList())
    }

    fun scheduleUpload() {
        val scheduler = context.getSystemService(JobScheduler::class.java) ?: return
        val job = JobInfo.Builder(JOB_ID, ComponentName(context, NotificationUploadJob::class.java))
            .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
            .setPersisted(true)
            .setBackoffCriteria(30_000L, JobInfo.BACKOFF_POLICY_EXPONENTIAL)
            .build()
        scheduler.schedule(job)
    }

    private fun writeLocked(events: List<RuntimeNotificationEvent>) {
        val array = JSONArray()
        events.forEach { array.put(it.toJson()) }
        prefs.put(KEY_QUEUE, array.toString())
    }

    private companion object {
        const val KEY_QUEUE = "pending_events"
        const val MAX_PENDING_EVENTS = 100
        const val JOB_ID = 7319
        val QUEUE_LOCK = Any()
    }
}
