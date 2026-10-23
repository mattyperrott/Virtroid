package io.virtroid.client.push

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Starts the relay after a reboot or an app update, both of which stop it. */
class NotificationRelayBootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        if (intent?.action != Intent.ACTION_BOOT_COMPLETED && intent?.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        if (NotificationRelayIdentityStore(context).load() == null) return
        NotificationRelayService.start(context)
    }
}
