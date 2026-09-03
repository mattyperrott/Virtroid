package io.virtroid.runtimeagent

import android.app.job.JobParameters
import android.app.job.JobService
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors

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
            val payload = event.toJson().toString().toByteArray(Charsets.UTF_8)
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
}
