package io.virtdroid.client

import android.content.Intent
import android.os.Bundle
import androidx.appcompat.app.AppCompatActivity
import io.virtdroid.client.data.AppLogStore
import io.virtdroid.client.data.SessionStore
import io.virtdroid.client.security.AppLockStore
import io.virtdroid.client.security.IdentityPasswordStore
import io.virtdroid.client.security.enableSecureWindow

class LauncherActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()

        val sessionStore = SessionStore(this)
        val appLockStore = AppLockStore(this)
        val identityPasswordStore = IdentityPasswordStore(this)
        val destination = when {
            !sessionStore.hasAccess() -> Intent(this, OnboardingActivity::class.java)
            !identityPasswordStore.isConfigured(sessionStore.accountId, sessionStore.deviceId) ->
                Intent(this, OnboardingActivity::class.java)
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
