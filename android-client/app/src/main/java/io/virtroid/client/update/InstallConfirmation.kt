package io.virtroid.client.update

import android.content.Intent

/**
 * Checks the confirmation Android hands back with STATUS_PENDING_USER_ACTION
 * before this app starts it. Only the system's PackageInstaller holds the
 * status PendingIntent, and its receiver isn't exported, but the confirmation
 * starts with this app's identity: an intent aimed at this app could open its
 * private screens, and one with URI grants could pass on its files. So it
 * starts only if it is what the framework sends: Android 10 and later's
 * CONFIRM_INSTALL, or Android 9's CONFIRM_PERMISSIONS, aimed by name at
 * another package (the installer), for a session this app opened, with no
 * grants or selector that could widen it.
 */
internal object InstallConfirmation {
    // PackageInstaller's system API actions, which the SDK doesn't expose.
    const val ACTION_CONFIRM_INSTALL = "android.content.pm.action.CONFIRM_INSTALL"
    const val ACTION_CONFIRM_PERMISSIONS = "android.content.pm.action.CONFIRM_PERMISSIONS"

    private const val URI_GRANTS = Intent.FLAG_GRANT_READ_URI_PERMISSION or
        Intent.FLAG_GRANT_WRITE_URI_PERMISSION or
        Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION or
        Intent.FLAG_GRANT_PREFIX_URI_PERMISSION

    /** Why the confirmation may not start, or null if it may. */
    fun refusal(
        action: String?,
        componentPackage: String?,
        intentPackage: String?,
        flags: Int,
        hasSelector: Boolean,
        confirmSessionId: Int,
        statusSessionId: Int,
        sessionIsOurs: Boolean,
        ownPackage: String,
    ): String? {
        if (action != ACTION_CONFIRM_INSTALL && action != ACTION_CONFIRM_PERMISSIONS) return "unexpected action $action"
        val target = componentPackage ?: intentPackage ?: return "no target package"
        if (intentPackage != null && intentPackage != target) return "component and package disagree"
        if (target == ownPackage) return "aimed at this app"
        if (flags and URI_GRANTS != 0) return "carries URI grants"
        if (hasSelector) return "has a selector"
        if (statusSessionId < 0 || confirmSessionId != statusSessionId) return "for another session"
        if (!sessionIsOurs) return "not this app's session"
        return null
    }
}
