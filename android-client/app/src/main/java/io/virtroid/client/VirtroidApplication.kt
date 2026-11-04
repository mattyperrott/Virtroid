package io.virtroid.client

import android.app.Activity
import android.app.Application
import android.content.Intent
import android.os.Bundle
import android.view.ViewTreeObserver
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.ClipboardAutoClear
import io.virtroid.client.security.ConfidentialRuntimeGuard
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.RuntimeCapabilityStore
import io.virtroid.client.security.SnapshotRollbackGuard
import io.virtroid.client.security.applyScreenCaptureProtection
import io.virtroid.client.push.NotificationRelayManager
import io.virtroid.client.push.RuntimeNotificationKeys
import kotlinx.coroutines.runBlocking

class VirtroidApplication : Application(), Application.ActivityLifecycleCallbacks {
    private var startedActivities = 0
    private var wasBackgrounded = false

    // Focus, not resume: Android shows the clipboard only once a window has it.
    private val clipboardFocusListener = ViewTreeObserver.OnWindowFocusChangeListener { hasFocus ->
        if (hasFocus) {
            ClipboardAutoClear.get(this).onFocusGained()
        }
    }

    override fun onCreate() {
        super.onCreate()
        RuntimeCapabilityStore.initialize(this)
        // A runtime's blob key is sealed only to a VM this phone has verified.
        VirtroidApi.BlobSeal.rememberNotificationKey = { runtimeId, key ->
            RuntimeNotificationKeys(this).save(runtimeId, key)
        }
        VirtroidApi.BlobSeal.snapshotFloor = { accountId, runtimeId ->
            SnapshotRollbackGuard(this).snapshotFloor(accountId, runtimeId)
        }
        VirtroidApi.BlobSeal.check = { baseUrl, lease ->
            runBlocking {
                ConfidentialRuntimeGuard(this@VirtroidApplication).checkBlobSealKey(lease) {
                    VirtroidApi().fetchSnpMeasurements(baseUrl)
                }
            }
        }
        // The process may be starting in the background (an update, a
        // service restart), where the relay can't start: the first screen
        // to open starts it (onActivityStarted).
        NotificationRelayManager(this).ensureRegistered(startRelay = false)
        registerActivityLifecycleCallbacks(this)
        // Camera captures wait in the cache only until they're sent to the
        // runtime; one left by a crash or a kill goes now.
        cacheDir.listFiles { file ->
            file.name.startsWith("virtroid-photo-") || file.name.startsWith("virtroid-video-")
        }?.forEach { it.delete() }
        AppLogStore.get(this).info("App startup", "lifecycle")
    }

    override fun onActivityStarted(activity: Activity) {
        startedActivities += 1
        if (startedActivities == 1) {
            // Retry a relay start that Android refused while the app was in the background.
            NotificationRelayManager(this).startIfRegistered()
        }
    }

    override fun onActivityStopped(activity: Activity) {
        startedActivities = (startedActivities - 1).coerceAtLeast(0)
        if (startedActivities == 0) {
            wasBackgrounded = true
            AppLockStore(activity).noteAppBackgrounded()
            IdentityPasswordStore(activity).clearUnlocked()
        }
    }

    override fun onActivityResumed(activity: Activity) {
        activity.window.decorView.viewTreeObserver.addOnWindowFocusChangeListener(clipboardFocusListener)
        activity.applyScreenCaptureProtection()
        if (activity.excludedFromResumeLock()) {
            return
        }

        val lockStore = AppLockStore(activity)
        if (wasBackgrounded) {
            wasBackgrounded = false
            if (lockStore.shouldLockAfterBackground()) {
                lockStore.clearUnlocked()
                IdentityPasswordStore(activity).clearUnlocked()
                AppLogStore.get(activity).info("App locked after background resume", "auth")
            }
        }
        // Locked until the unlock screen succeeds: a screen reached any other
        // way (Back from the unlock screen, say) asks again.
        if (lockStore.isEnabled() && lockStore.hasCredential() && !lockStore.isUnlocked()) {
            activity.startActivity(
                Intent(activity, UnlockActivity::class.java)
                    .putExtra(UnlockActivity.EXTRA_RETURN_TO_PREVIOUS, true)
                    .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            )
        }
    }

    override fun onActivityPaused(activity: Activity) {
        activity.window.decorView.viewTreeObserver.removeOnWindowFocusChangeListener(clipboardFocusListener)
    }

    override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) = Unit
    override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit
    override fun onActivityDestroyed(activity: Activity) = Unit

    private fun Activity.excludedFromResumeLock(): Boolean {
        return this is LauncherActivity || this is UnlockActivity || this is OnboardingActivity
    }
}
