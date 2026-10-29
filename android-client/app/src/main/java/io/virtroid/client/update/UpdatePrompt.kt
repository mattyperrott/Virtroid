package io.virtroid.client.update

import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.BuildConfig
import io.virtroid.client.R
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.ui.Notice
import io.virtroid.client.ui.VirtroidSheet
import kotlinx.coroutines.launch

/**
 * Checks for a newer release and offers it. Quiet checks (at startup) say
 * nothing unless there is an update; a check the user asked for always
 * reports its result.
 */
object UpdatePrompt {
    fun check(activity: AppCompatActivity, quiet: Boolean) {
        // Quiet checks happen at most daily; a check the user asks for always runs.
        if (quiet && !AppUpdater(activity).checkDue()) return
        val logs = AppLogStore.get(activity)
        if (!quiet) {
            Notice.show(activity, activity.getString(R.string.update_checking))
        }
        activity.lifecycleScope.launch {
            val release = runCatching { AppUpdater(activity).newerRelease() }
                .onFailure { logs.warn("Update check failed: ${it.message}", "update") }
                .getOrElse {
                    if (!quiet) Notice.show(activity, activity.getString(R.string.update_check_failed))
                    return@launch
                }
            if (activity.isFinishing || activity.isDestroyed) return@launch
            if (release == null) {
                if (!quiet) Notice.show(activity, activity.getString(R.string.update_current, BuildConfig.VERSION_NAME))
                return@launch
            }
            logs.info("Virtroid ${release.version} is available", "update")
            VirtroidSheet(activity)
                .title(activity.getString(R.string.update_available_title, release.version))
                .message(activity.getString(R.string.update_available_body, BuildConfig.VERSION_NAME))
                .primary(activity.getString(R.string.update_install)) { install(activity, release) }
                .secondary(activity.getString(R.string.update_later))
                .show()
        }
    }

    private fun install(activity: AppCompatActivity, release: AppUpdater.Release) {
        if (!activity.packageManager.canRequestPackageInstalls()) {
            Notice.show(activity, activity.getString(R.string.update_allow_installs))
            activity.startActivity(
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${activity.packageName}")),
            )
            return
        }
        Notice.show(activity, activity.getString(R.string.update_downloading))
        activity.lifecycleScope.launch {
            runCatching { AppUpdater(activity).install(release) }.onFailure {
                AppLogStore.get(activity).warn("Update ${release.version} failed: ${it.message}", "update")
                if (!activity.isFinishing) {
                    Notice.show(activity, activity.getString(R.string.update_failed) + " " + (it.message ?: ""))
                }
            }
        }
    }
}
