package io.virtroid.client

import android.app.Activity
import android.app.Application
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Bundle
import android.os.PowerManager
import android.view.View
import android.view.ViewTreeObserver
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.security.AppLockStore
import io.virtroid.client.security.AwayTracker
import io.virtroid.client.security.ClipboardAutoClear
import io.virtroid.client.security.ConfidentialRuntimeGuard
import io.virtroid.client.security.IdentityPasswordStore
import io.virtroid.client.security.LockCover
import io.virtroid.client.security.RuntimeCapabilityStore
import io.virtroid.client.security.SnapshotRollbackGuard
import io.virtroid.client.security.applyScreenCaptureProtection
import io.virtroid.client.push.NotificationRelayManager
import io.virtroid.client.push.RuntimeNotificationKeys
import io.virtroid.client.ui.VirtroidMotion
import kotlinx.coroutines.runBlocking

class VirtroidApplication : Application(), Application.ActivityLifecycleCallbacks {
    private val away = AwayTracker()
    private val startedActivities = mutableListOf<Activity>()

    // Not every screen in sight is paused as the display goes off: one under
    // another app's window, such as a permission dialog, already was.
    private val displayOff = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) = coverStarted()
    }

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
        registerReceiver(displayOff, IntentFilter(Intent.ACTION_SCREEN_OFF))
        // Camera captures wait in the cache only until they're sent to the
        // runtime; one left by a crash or a kill goes now.
        cacheDir.listFiles { file ->
            file.name.startsWith("virtroid-photo-") || file.name.startsWith("virtroid-video-")
        }?.forEach { it.delete() }
        AppLogStore.get(this).info("App startup", "lifecycle")
    }

    override fun onActivityStarted(activity: Activity) {
        // By now the screen has set its click listeners.
        activity.findViewById<View>(android.R.id.content)?.let(VirtroidMotion::pressableRows)
        startedActivities += activity
        if (away.onStarted()) {
            // Retry a relay start that Android refused while the app was in the background.
            NotificationRelayManager(this).startIfRegistered()
        }
        // Before the screen can draw: the unlock screen opens only once it has
        // resumed. Whether to lock is still decided then (onActivityResumed).
        if (!activity.excludedFromResumeLock()) {
            val lockStore = AppLockStore(activity)
            if (lockStore.isLocked() || (away.isAway && lockStore.shouldLockAfterBackground())) {
                LockCover.cover(activity)
            } else {
                LockCover.reveal()
            }
        }
    }

    override fun onActivityStopped(activity: Activity) {
        startedActivities -= activity
        if (away.onStopped(rebuilding = activity.isChangingConfigurations)) {
            AppLockStore(activity).noteAppBackgrounded()
            IdentityPasswordStore(activity).clearUnlocked()
        }
    }

    override fun onActivityResumed(activity: Activity) {
        activity.window.decorView.viewTreeObserver.addOnWindowFocusChangeListener(clipboardFocusListener)
        activity.applyScreenCaptureProtection()
        val lockStore = AppLockStore(activity)
        if (activity is UnlockActivity) {
            away.onUnlockScreen(locked = lockStore.isLocked())
        }
        if (activity.excludedFromResumeLock()) {
            return
        }

        if (away.takeAway() && lockStore.shouldLockAfterBackground()) {
            lockStore.clearUnlocked()
            IdentityPasswordStore(activity).clearUnlocked()
            AppLogStore.get(activity).info("App locked after background resume", "auth")
        }
        // Locked until the unlock screen succeeds: a screen reached any other
        // way (Back from the unlock screen, say) asks again.
        if (lockStore.isLocked()) {
            LockCover.cover(activity)
            activity.startActivity(
                Intent(activity, UnlockActivity::class.java)
                    .putExtra(UnlockActivity.EXTRA_RETURN_TO_PREVIOUS, true)
                    .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            )
        } else {
            LockCover.reveal()
        }
    }

    override fun onActivityPaused(activity: Activity) {
        activity.window.decorView.viewTreeObserver.removeOnWindowFocusChangeListener(clipboardFocusListener)
        coverStarted()
    }

    /**
     * With the display off, covers every screen: on waking, Android shows what
     * was last drawn before a screen starts again. Covered now, that is nothing;
     * the start shows it again unless the app locks.
     */
    private fun coverStarted() {
        // Told late, the display may be on again and the screen in use.
        if (getSystemService(PowerManager::class.java)?.isInteractive != false) return
        if (!AppLockStore(this).run { isEnabled() && hasCredential() }) return
        startedActivities.filterNot { it.excludedFromResumeLock() }.forEach(LockCover::cover)
    }

    override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) = Unit
    override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit
    override fun onActivityDestroyed(activity: Activity) = Unit

    private fun Activity.excludedFromResumeLock(): Boolean {
        return this is LauncherActivity || this is UnlockActivity || this is OnboardingActivity
    }
}
