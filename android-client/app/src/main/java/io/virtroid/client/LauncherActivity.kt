package io.virtroid.client

import android.content.Intent
import android.os.Bundle
import androidx.appcompat.app.AppCompatActivity
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.enableSecureWindow

class LauncherActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()

        val appLockStore = AppLockStore(this)
        if (appLockStore.shouldRequireUnlockOnLaunch()) {
            AppLogStore.get(this).info("Launch routing resolved to local vault unlock", "lifecycle")
            startActivity(
                Intent(this, UnlockActivity::class.java)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
            )
            finish()
            return
        }

        val sessionStore = SessionStore(this)
        val identityPasswordStore = IdentityPasswordStore(this)
        val appSettings = AppSettingsStore(this)
        val destination = when {
            !sessionStore.hasAccess() -> Intent(this, OnboardingActivity::class.java)
            !identityPasswordStore.isConfigured(sessionStore.accountId, sessionStore.deviceId) ->
                Intent(this, OnboardingActivity::class.java)
            !appSettings.permissionsSetupCompleted -> PermissionsActivity.createIntent(this)
            appLockStore.shouldRequireUnlockOnLaunch() -> Intent(this, UnlockActivity::class.java)
            else -> Intent(this, MainActivity::class.java)
        }
        AppLogStore.get(this).info("Launch routing resolved to ${destination.component?.className.orEmpty()}", "lifecycle")

        startActivity(
            destination.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK),
        )
        finish()
    }
}
