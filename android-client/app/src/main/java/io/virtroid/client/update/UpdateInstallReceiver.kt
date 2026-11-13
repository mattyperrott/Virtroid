package io.virtroid.client.update

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import androidx.core.content.IntentCompat
import io.virtroid.client.R
import io.virtroid.client.data.AppLogStore
import android.widget.Toast

/** Shows Android's install confirmation, and reports a failed update. */
class UpdateInstallReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> confirm(context, intent)
            PackageInstaller.STATUS_SUCCESS -> Unit
            else -> {
                val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE).orEmpty()
                failed(context, "App update not installed ($status): $message")
            }
        }
    }

    private fun confirm(context: Context, intent: Intent) {
        val sessionId = intent.getIntExtra(PackageInstaller.EXTRA_SESSION_ID, -1)
        val confirm = runCatching {
            IntentCompat.getParcelableExtra(intent, Intent.EXTRA_INTENT, Intent::class.java)
        }.getOrNull()
        val why = if (confirm == null) {
            "no readable confirmation"
        } else {
            runCatching { refusal(context, confirm, sessionId) }
                .getOrElse { "unreadable confirmation (${it.javaClass.simpleName})" }
        }
        if (confirm == null || why != null) {
            failed(context, "App update confirmation not shown: $why")
            return
        }
        context.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    private fun refusal(context: Context, confirm: Intent, sessionId: Int): String? =
        InstallConfirmation.refusal(
            action = confirm.action,
            componentPackage = confirm.component?.packageName,
            intentPackage = confirm.`package`,
            flags = confirm.flags,
            hasSelector = confirm.selector != null,
            confirmSessionId = confirm.getIntExtra(PackageInstaller.EXTRA_SESSION_ID, -1),
            statusSessionId = sessionId,
            sessionIsOurs = context.packageManager.packageInstaller.getSessionInfo(sessionId)
                ?.installerPackageName == context.packageName,
            ownPackage = context.packageName,
        )

    private fun failed(context: Context, log: String) {
        AppLogStore.get(context).warn(log, "update")
        Toast.makeText(context, context.getString(R.string.update_failed), Toast.LENGTH_LONG).show()
    }
}
