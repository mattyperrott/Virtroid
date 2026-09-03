package io.virtroid.runtimeagent

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

class ProvisionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_PROVISION) {
            resultCode = RESULT_REJECTED
            return
        }
        val config = AgentConfig(
            baseUrl = intent.getStringExtra(EXTRA_BASE_URL).orEmpty(),
            runtimeId = intent.getStringExtra(EXTRA_RUNTIME_ID).orEmpty(),
            token = intent.getStringExtra(EXTRA_TOKEN).orEmpty(),
        )
        if (!AgentConfigStore(context).provision(config)) {
            resultCode = RESULT_REJECTED
            return
        }
        NotificationQueue(context).scheduleUpload()
        resultCode = RESULT_OK
    }

    companion object {
        const val ACTION_PROVISION = "io.virtroid.runtimeagent.PROVISION"
        const val EXTRA_BASE_URL = "base_url"
        const val EXTRA_RUNTIME_ID = "runtime_id"
        const val EXTRA_TOKEN = "token"
        const val RESULT_OK = 1
        const val RESULT_REJECTED = 2
    }
}
