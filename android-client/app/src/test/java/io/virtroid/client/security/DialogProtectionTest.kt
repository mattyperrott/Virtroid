package io.virtroid.client.security

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * A dialog has a window of its own, which the activity's FLAG_SECURE doesn't
 * cover. So every dialog the app shows must come from ProtectedDialog or
 * ProtectedAlertDialogBuilder (SecureWindow.kt), which take the app's
 * screen-capture setting: nothing else may build a dialog window directly.
 */
class DialogProtectionTest {
    private val sources = File("src/main/java")

    // Classes that open a window of their own when shown, named plainly or with
    // their package (android.app.Dialog(...)); ProtectedDialog( doesn't match.
    private val direct = Regex(
        """(?<![\w.])(?:[a-z]\w*\.)*(Dialog|AppCompatDialog|ComponentDialog|BottomSheetDialog|DatePickerDialog|TimePickerDialog|ProgressDialog|MaterialAlertDialogBuilder)\(|AlertDialog\.Builder\(""",
    )

    @Test
    fun everyDialogTakesTheScreenCaptureSetting() {
        val files = sources.walkTopDown().filter { it.isFile && it.extension in setOf("kt", "java") }.toList()
        assertTrue("found the app's sources (${sources.absolutePath})", files.size > 50)
        val offenders = files
            .filterNot { it.name == "SecureWindow.kt" }
            .flatMap { file ->
                file.readLines().mapIndexedNotNull { index, line ->
                    direct.find(line)?.let { "${file.relativeTo(sources)}:${index + 1}: ${it.value}" }
                }
            }
        assertEquals(
            "Build dialogs with ProtectedDialog or ProtectedAlertDialogBuilder, not directly",
            emptyList<String>(),
            offenders,
        )
    }
}
