package io.virtroid.client

import android.app.Activity
import android.app.Application
import android.content.Intent
import android.os.Bundle
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.security.AppLockStore
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
        NotificationRelayManager(this).ensureRegistered()
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
        activity.applyScreenCaptureProtection()
        if (!wasBackgrounded || activity.excludedFromResumeLock()) {
            return
        }

        wasBackgrounded = false
        val lockStore = AppLockStore(activity)
        if (lockStore.shouldLockAfterBackground()) {
            lockStore.clearUnlocked()
            IdentityPasswordStore(activity).clearUnlocked()
            AppLogStore.get(activity).info("App locked after background resume", "auth")
            activity.startActivity(
                Intent(activity, UnlockActivity::class.java)
                    .putExtra(UnlockActivity.EXTRA_RETURN_TO_PREVIOUS, true),
            )
        }
    }

    override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) = Unit
    override fun onActivityPaused(activity: Activity) = Unit
    override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit
    override fun onActivityDestroyed(activity: Activity) = Unit

    private fun Activity.excludedFromResumeLock(): Boolean {
        return this is LauncherActivity || this is UnlockActivity || this is OnboardingActivity
    }
}
