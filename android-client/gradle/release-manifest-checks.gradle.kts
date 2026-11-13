// Release checks on the merged manifest (library entries included), applied
// by each app module after it sets two lists:
//   releaseExportedComponents: the components other apps may reach (its
//     launcher, say). Every other exported activity, alias, service, receiver
//     or provider must need a permission only the system and adb's shell hold.
//   releasePermissions: every permission the app may request. A library that
//     adds one fails the build until it is looked at and listed.
// Both fail on stale entries too, and any <permission> declared must be
// signature-level, so no other app can be granted it.
import javax.xml.XMLConstants
import javax.xml.parsers.DocumentBuilderFactory
import org.w3c.dom.Document
import org.w3c.dom.Element

fun listExtra(name: String): Set<String> = (extra[name] as? Set<*>)?.map { it.toString() }?.toSet()
    ?: throw GradleException("Set extra[\"$name\"] before applying ${buildscript.sourceFile?.name}.")

val allowedExports = listExtra("releaseExportedComponents")
val allowedPermissions = listExtra("releasePermissions")
// DUMP: signature|privileged|development, so the shell has it and apps can't
// (androidx.profileinstaller's receiver and our adb-driven receivers use it).
val systemOnlyPermissions = setOf("android.permission.DUMP")
val manifestRoot = layout.buildDirectory.dir("intermediates/merged_manifest/release")
val ns = "http://schemas.android.com/apk/res/android"

fun mergedReleaseManifest(root: File): Document {
    val manifest = root.walkTopDown().firstOrNull { it.isFile && it.name == "AndroidManifest.xml" }
        ?: throw GradleException("Release merged AndroidManifest.xml was not found.")
    return DocumentBuilderFactory.newInstance()
        .apply {
            isNamespaceAware = true
            setFeature(XMLConstants.FEATURE_SECURE_PROCESSING, true)
            setFeature("http://apache.org/xml/features/disallow-doctype-decl", true)
        }
        .newDocumentBuilder()
        .parse(manifest)
}

fun Document.elements(tag: String): List<Element> =
    getElementsByTagName(tag).let { nodes -> (0 until nodes.length).map { nodes.item(it) as Element } }

val verifyReleaseExportedComponents = tasks.register("verifyReleaseExportedComponents") {
    group = "verification"
    description = "Fails if the release manifest exports a component other apps could reach."
    dependsOn("processReleaseMainManifest")
    doLast {
        val doc = mergedReleaseManifest(manifestRoot.get().asFile)
        val pkg = doc.documentElement.getAttribute("package")
        val reachable = mutableListOf<String>()
        val seen = mutableSetOf<String>()
        for (tag in listOf("activity", "activity-alias", "service", "receiver", "provider")) {
            for (component in doc.elements(tag)) {
                val name = component.getAttributeNS(ns, "name").let { if (it.startsWith(".")) pkg + it else it }
                seen += name
                val exported = component.getAttributeNS(ns, "exported")
                // Without the attribute, an intent filter exports all but providers.
                val isExported = exported == "true" ||
                    (exported.isEmpty() && tag != "provider" && component.getElementsByTagName("intent-filter").length > 0)
                if (!isExported || name in allowedExports) continue
                if (component.getAttributeNS(ns, "permission") in systemOnlyPermissions) continue
                reachable += "$tag $name"
            }
        }
        if (reachable.isNotEmpty()) {
            throw GradleException(
                "Release manifest exports components other apps can reach: ${reachable.joinToString()}. " +
                    "Set android:exported=\"false\", require a system-only permission, or add them to releaseExportedComponents.",
            )
        }
        val unused = allowedExports - seen
        if (unused.isNotEmpty()) {
            throw GradleException("releaseExportedComponents names components the release manifest doesn't have: ${unused.joinToString()}.")
        }
    }
}

val verifyReleasePermissions = tasks.register("verifyReleasePermissions") {
    group = "verification"
    description = "Fails if the release manifest requests a permission not on the module's list, or declares one other apps could get."
    dependsOn("processReleaseMainManifest")
    doLast {
        val doc = mergedReleaseManifest(manifestRoot.get().asFile)
        val requested = listOf("uses-permission", "uses-permission-sdk-23")
            .flatMap { doc.elements(it) }
            .map { it.getAttributeNS(ns, "name") }
            .toSet()
        val unlisted = requested - allowedPermissions
        if (unlisted.isNotEmpty()) {
            throw GradleException(
                "Release manifest requests permissions not in releasePermissions: ${unlisted.joinToString()}. " +
                    "Remove them (tools:node=\"remove\" for a library's) or list them with the reason.",
            )
        }
        val stale = allowedPermissions - requested
        if (stale.isNotEmpty()) {
            throw GradleException("releasePermissions lists permissions the release manifest doesn't request: ${stale.joinToString()}.")
        }
        val weak = doc.elements("permission").filter { permission ->
            val levels = permission.getAttributeNS(ns, "protectionLevel").split('|').map { it.trim() }
            "signature" !in levels || levels.any { it !in setOf("signature", "knownSigner") }
        }
        if (weak.isNotEmpty()) {
            throw GradleException(
                "Release manifest declares permissions other apps could be granted: " +
                    weak.joinToString { "${it.getAttributeNS(ns, "name")} (${it.getAttributeNS(ns, "protectionLevel").ifEmpty { "normal" }})" } +
                    ". Use protectionLevel=\"signature\".",
            )
        }
    }
}

tasks.matching { it.name in setOf("assembleRelease", "packageRelease", "bundleRelease") }.configureEach {
    dependsOn(verifyReleaseExportedComponents, verifyReleasePermissions)
}
