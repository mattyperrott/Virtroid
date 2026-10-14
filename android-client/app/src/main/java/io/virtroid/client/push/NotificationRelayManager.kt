package io.virtroid.client.push

import android.content.Context
import android.content.Intent
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

    fun ensureRegistered() {
        val previousIdentity = identityStore.load()
        previousIdentity?.let { startService() }

        val session = SessionStore(appContext)
        val accountId = session.accountId?.trim().orEmpty()
        val deviceId = session.deviceId?.trim().orEmpty()
        val baseUrl = session.baseUrl.trim().trimEnd('/')
        if (accountId.isBlank() || deviceId.isBlank() || !baseUrl.startsWith("https://")) return

        val identity = NotificationRelayIdentityStore.RelayIdentity(baseUrl, accountId, deviceId)
        val identityChanged = previousIdentity != null && previousIdentity != identity
        val encryptionPublicKey = PushEncryptionKeyStore(appContext).publicKeyMaterial()
        val relayPublicKey = NotificationRelayKeyStore().publicKeyMaterial()
        val registrationKey = listOf(baseUrl, accountId, deviceId, encryptionPublicKey, relayPublicKey).joinToString("\u0000")

        identityStore.save(baseUrl, accountId, deviceId)
        startService()
        synchronized(registrationLock) {
            if (registeredKey == registrationKey || !registrationsInFlight.add(registrationKey)) return
        }
        registrationScope.launch {
            runCatching {
                VirtroidApi().upsertNotificationSubscription(
                    baseUrl = baseUrl,
                    accountId = accountId,
                    deviceId = deviceId,
                    encryptionPublicKey = encryptionPublicKey,
                    relayPublicKey = relayPublicKey,
                )
            }.onSuccess {
                synchronized(registrationLock) { registeredKey = registrationKey }
                if (identityChanged) NotificationRelayService.reconnect(appContext)
                AppLogStore.get(appContext).info("Runtime notification relay registered", "notifications")
            }.onFailure { error ->
                AppLogStore.get(appContext).warn("Notification relay registration deferred: ${error.message}", "notifications")
            }
            synchronized(registrationLock) { registrationsInFlight.remove(registrationKey) }
        }
    }

    /** Starts the relay if this device has registered for it before. */
    fun startIfRegistered() {
        if (identityStore.load() != null) startService()
    }

    fun clearLocalRegistration() {
        identityStore.clear()
        appContext.stopService(Intent(appContext, NotificationRelayService::class.java))
        PushEncryptionKeyStore(appContext).clear()
        NotificationRelayKeyStore().deleteKey()
        synchronized(registrationLock) {
            registeredKey = null
            registrationsInFlight.clear()
        }
    }

    private fun startService() {
        NotificationRelayService.start(appContext)
    }

    private companion object {
        val registrationScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val registrationLock = Any()
        val registrationsInFlight = mutableSetOf<String>()

        @Volatile
        var registeredKey: String? = null
    }
}
