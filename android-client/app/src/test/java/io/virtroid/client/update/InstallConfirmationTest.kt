package io.virtroid.client.update

import android.content.Intent
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Test

class InstallConfirmationTest {
    private val own = "io.virtroid.client"

    private fun refusal(
        action: String? = InstallConfirmation.ACTION_CONFIRM_INSTALL,
        componentPackage: String? = null,
        intentPackage: String? = "com.google.android.packageinstaller",
        flags: Int = 0,
        hasSelector: Boolean = false,
        confirmSessionId: Int = 7,
        statusSessionId: Int = 7,
        sessionIsOurs: Boolean = true,
    ) = InstallConfirmation.refusal(
        action, componentPackage, intentPackage, flags, hasSelector,
        confirmSessionId, statusSessionId, sessionIsOurs, own,
    )

    @Test
    fun whatTheFrameworkSendsStarts() {
        // Android 10 and later: the installer, by package.
        assertNull(refusal())
        // Android 9: the permission controller, by package.
        assertNull(refusal(InstallConfirmation.ACTION_CONFIRM_PERMISSIONS, intentPackage = "com.android.packageinstaller"))
        // A framework that names the component instead.
        assertNull(refusal(componentPackage = "com.android.packageinstaller", intentPackage = null))
    }

    @Test
    fun anythingAimedAtThisAppIsRefused() {
        assertNotNull(refusal(intentPackage = own))
        assertNotNull(refusal(componentPackage = own, intentPackage = null))
        // A component here with the installer's package on the intent can't start.
        assertNotNull(refusal(componentPackage = own, intentPackage = "com.android.packageinstaller"))
        assertNotNull(refusal(intentPackage = null))
    }

    @Test
    fun aTargetNamedTwoWaysIsRefused() {
        // The framework names the installer once; two different names aren't its intent.
        assertEquals(
            "component and package disagree",
            refusal(componentPackage = "com.android.packageinstaller", intentPackage = "com.example.other"),
        )
    }

    @Test
    fun otherActionsAreRefused() {
        assertNotNull(refusal(action = Intent.ACTION_VIEW))
        assertNotNull(refusal(action = "android.content.pm.action.CONFIRM_PRE_APPROVAL"))
        assertNotNull(refusal(action = null))
    }

    @Test
    fun uriGrantsAndSelectorsAreRefused() {
        for (grant in listOf(
            Intent.FLAG_GRANT_READ_URI_PERMISSION,
            Intent.FLAG_GRANT_WRITE_URI_PERMISSION,
            Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION,
            Intent.FLAG_GRANT_PREFIX_URI_PERMISSION,
        )) {
            assertNotNull(refusal(flags = grant))
        }
        assertNull(refusal(flags = Intent.FLAG_ACTIVITY_NEW_TASK))
        assertNotNull(refusal(hasSelector = true))
    }

    @Test
    fun onlyThisAppsOwnSessionStarts() {
        assertEquals("for another session", refusal(confirmSessionId = 8))
        assertEquals("for another session", refusal(confirmSessionId = -1, statusSessionId = -1))
        assertEquals("not this app's session", refusal(sessionIsOurs = false))
    }
}
