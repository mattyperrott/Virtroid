package io.virtroid.client.security

/**
 * Whether the user left the app since a guarded screen was last in front, which
 * is when the app may have to lock. Main thread only.
 */
internal class AwayTracker {
    private var startedScreens = 0

    /** The app was out of sight, and no guarded screen has taken that into account yet. */
    var isAway = false
        private set

    /** A screen starts; true if it is the first, so the app comes into sight. */
    fun onStarted(): Boolean {
        startedScreens += 1
        return startedScreens == 1
    }

    /**
     * A screen stops; true if the app has left sight with it. Not when Android
     * is [rebuilding] it for a rotation or a resize: nobody left the app, and
     * the rebuilt screen starts at once.
     */
    fun onStopped(rebuilding: Boolean): Boolean {
        startedScreens = (startedScreens - 1).coerceAtLeast(0)
        if (startedScreens > 0 || rebuilding) return false
        isAway = true
        return true
    }

    /**
     * The unlock screen is in front. Still [locked], the time away is answered
     * by what it asks for, and must not lock the app again after it. Unlocked,
     * as by a PIN entered just before leaving, the time away still counts.
     */
    fun onUnlockScreen(locked: Boolean) {
        if (locked) isAway = false
    }

    /** A guarded screen is in front: whether it comes after time away, which it now answers for. */
    fun takeAway(): Boolean = isAway.also { isAway = false }
}
