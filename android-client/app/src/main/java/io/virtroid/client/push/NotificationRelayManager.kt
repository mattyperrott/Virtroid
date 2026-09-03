package io.virtroid.client.push

import android.content.Context
import android.content.Intent
import androidx.core.content.ContextCompat
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.security.PushEncryptionKeyStore
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch

class NotificationRelayManager(context: Context) {
    private val appContext = context.applicationContext
    private val identityStore = NotificationRelayIdentityStore(appContext)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    fun ensureRegistered() {
        identityStore.load()?.let { startService() }

        val session = SessionStore(appContext)
        val accountId = session.accountId?.trim().orEmpty()
        val deviceId = session.deviceId?.trim().orEmpty()
        val baseUrl = session.baseUrl.trim().trimEnd('/')
        if (accountId.isBlank() || deviceId.isBlank() || !baseUrl.startsWith("https://")) return

        identityStore.save(baseUrl, accountId, deviceId)
        startService()
        scope.launch {
            runCatching {
                VirtroidApi().upsertNotificationSubscription(
                    baseUrl = baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    encryptionPublicKey = PushEncryptionKeyStore(appContext).publicKeyMaterial(),
                )
            }.onSuccess {
                NotificationRelayService.reconnect(appContext)
                AppLogStore.get(appContext).info("Runtime notification relay registered", "notifications")
            }.onFailure { error ->
                AppLogStore.get(appContext).warn("Notification relay registration deferred: ${error.message}", "notifications")
            }
        }
    }

    fun clearLocalRegistration() {
        identityStore.clear()
        appContext.stopService(Intent(appContext, NotificationRelayService::class.java))
        PushEncryptionKeyStore(appContext).clear()
    }

    private fun startService() {
        ContextCompat.startForegroundService(
            appContext,
            Intent(appContext, NotificationRelayService::class.java),
        )
    }
}
