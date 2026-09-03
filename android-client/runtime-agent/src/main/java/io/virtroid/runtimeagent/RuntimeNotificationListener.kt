package io.virtroid.runtimeagent

import android.app.Notification
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification
import java.nio.charset.StandardCharsets
import java.time.Instant
import java.util.UUID

class RuntimeNotificationListener : NotificationListenerService() {
    override fun onListenerConnected() {
        NotificationQueue(this).scheduleUpload()
    }

    override fun onNotificationPosted(sbn: StatusBarNotification) {
        if (AgentConfigStore(this).load() == null || shouldIgnore(sbn)) return
        val label = runCatching {
            packageManager.getApplicationLabel(packageManager.getApplicationInfo(sbn.packageName, 0)).toString()
        }.getOrDefault(sbn.packageName)
        val title = sbn.notification.extras
            ?.getCharSequence(Notification.EXTRA_TITLE)
            ?.toString()
            .orEmpty()
        val identity = "${sbn.key}\n${sbn.postTime}".toByteArray(StandardCharsets.UTF_8)
        NotificationQueue(this).enqueue(
            RuntimeNotificationEvent(
                eventId = UUID.nameUUIDFromBytes(identity).toString(),
                packageName = sbn.packageName.take(MAX_PACKAGE_CHARS),
                appLabel = label.takeCodePoints(MAX_LABEL_CHARS),
                postedAt = Instant.ofEpochMilli(sbn.postTime).toString(),
                title = title.takeCodePoints(MAX_TITLE_CHARS),
            ),
        )
    }

    private fun shouldIgnore(sbn: StatusBarNotification): Boolean {
        val notification = sbn.notification
        return sbn.packageName in IGNORED_PACKAGES ||
            notification.flags and Notification.FLAG_GROUP_SUMMARY != 0 ||
            notification.flags and Notification.FLAG_ONGOING_EVENT != 0
    }

    private companion object {
        const val MAX_PACKAGE_CHARS = 255
        const val MAX_LABEL_CHARS = 100
        const val MAX_TITLE_CHARS = 200
        val IGNORED_PACKAGES = setOf("android", "com.android.systemui", "io.virtroid.runtimeagent")
    }
}

private fun String.takeCodePoints(maxCodePoints: Int): String {
    val end = offsetByCodePoints(0, codePointCount(0, length).coerceAtMost(maxCodePoints))
    return substring(0, end)
}
