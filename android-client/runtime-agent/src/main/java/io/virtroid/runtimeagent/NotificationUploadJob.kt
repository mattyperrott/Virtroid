package io.virtroid.runtimeagent

import android.app.job.JobParameters
import android.app.job.JobService
import android.util.Base64
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.security.SecureRandom
import java.util.concurrent.Executors
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

class NotificationUploadJob : JobService() {
    private val executor = Executors.newSingleThreadExecutor()

    override fun onStartJob(params: JobParameters): Boolean {
        executor.execute {
            val config = AgentConfigStore(this).load()
            val queue = NotificationQueue(this)
            var retry = config == null
            if (config != null) {
                for (event in queue.read()) {
                    when (upload(config, event)) {
                        UploadResult.DELIVERED, UploadResult.DUPLICATE -> queue.remove(event.eventId)
                        UploadResult.REJECTED -> queue.remove(event.eventId)
                        UploadResult.RETRY -> {
                            retry = true
                            break
                        }
                    }
                }
            }
            jobFinished(params, retry)
        }
        return true
    }

    override fun onStopJob(params: JobParameters): Boolean = true

    override fun onDestroy() {
        executor.shutdownNow()
        super.onDestroy()
    }

    private fun upload(config: AgentConfig, event: RuntimeNotificationEvent): UploadResult = runCatching {
        val connection = URL("${config.baseUrl}/api/v1/runtime-notifications/${config.runtimeId}")
            .openConnection() as HttpURLConnection
        try {
            connection.requestMethod = "POST"
            connection.connectTimeout = 15_000
            connection.readTimeout = 20_000
            connection.doOutput = true
            connection.setRequestProperty("Authorization", "Bearer ${config.token}")
            connection.setRequestProperty("Content-Type", "application/json")
            val payload = uploadBody(config, event).toString().toByteArray(Charsets.UTF_8)
            connection.setFixedLengthStreamingMode(payload.size)
            connection.outputStream.use { it.write(payload) }
            when (connection.responseCode) {
                in 200..299 -> UploadResult.DELIVERED
                HttpURLConnection.HTTP_CONFLICT -> UploadResult.DUPLICATE
                HttpURLConnection.HTTP_BAD_REQUEST -> UploadResult.REJECTED
                HttpURLConnection.HTTP_UNAUTHORIZED,
                HttpURLConnection.HTTP_FORBIDDEN -> UploadResult.RETRY
                else -> UploadResult.RETRY
            }
        } finally {
            connection.disconnect()
        }
    }.getOrDefault(UploadResult.RETRY)

    private enum class UploadResult { DELIVERED, DUPLICATE, REJECTED, RETRY }

    companion object {
        private const val SEALED_CONTEXT = "virtroid-notification-v1"

        /**
         * With the runtime's notification key, the event's app, time and title
         * go sealed (AES-GCM, bound to this runtime and event), readable only by
         * the account's phones; otherwise as plain metadata.
         */
        internal fun uploadBody(config: AgentConfig, event: RuntimeNotificationEvent): JSONObject {
            if (config.notificationKey.isEmpty()) {
                return event.toJson()
            }
            val key = Base64.decode(config.notificationKey, Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP)
            val iv = ByteArray(12).also { SecureRandom().nextBytes(it) }
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, iv))
            cipher.updateAAD("$SEALED_CONTEXT\n${config.runtimeId}\n${event.eventId}".toByteArray(Charsets.UTF_8))
            val sealed = cipher.doFinal(event.toJson().toString().toByteArray(Charsets.UTF_8))
            key.fill(0)
            return JSONObject()
                .put("event_id", event.eventId)
                .put("sealed", Base64.encodeToString(iv + sealed, Base64.NO_WRAP))
        }
    }
}
