package io.virtroid.client.push

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.core.content.ContextCompat

class NotificationRelayBootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        if (intent?.action != Intent.ACTION_BOOT_COMPLETED) return
        if (NotificationRelayIdentityStore(context).load() == null) return
        ContextCompat.startForegroundService(
            context,
            Intent(context, NotificationRelayService::class.java),
        )
    }
}
