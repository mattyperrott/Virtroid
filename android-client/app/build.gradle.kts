import java.util.Properties
import org.gradle.api.GradleException

plugins {
    id("com.android.application")
}

val localProperties = Properties().apply {
    val file = rootProject.file("local.properties")
    if (file.exists()) {
        file.inputStream().use(::load)
    }
}

fun signingValue(name: String): String? {
    return providers.environmentVariable(name).orNull ?: localProperties.getProperty(name)
}

fun buildConfigString(value: String): String {
    return "\"" + value.replace("\\", "\\\\").replace("\"", "\\\"") + "\""
}

val releaseStoreFilePath = signingValue("VIRTROID_RELEASE_STORE_FILE")
val releaseStorePassword = signingValue("VIRTROID_RELEASE_STORE_PASSWORD")
val releaseKeyAlias = signingValue("VIRTROID_RELEASE_KEY_ALIAS")
val releaseKeyPassword = signingValue("VIRTROID_RELEASE_KEY_PASSWORD")
val releaseSigningValues = linkedMapOf(
    "VIRTROID_RELEASE_STORE_FILE" to releaseStoreFilePath,
    "VIRTROID_RELEASE_STORE_PASSWORD" to releaseStorePassword,
    "VIRTROID_RELEASE_KEY_ALIAS" to releaseKeyAlias,
    "VIRTROID_RELEASE_KEY_PASSWORD" to releaseKeyPassword,
)
val releaseSigningConfigured = releaseSigningValues.values.all { !it.isNullOrBlank() }
val releaseArtifactTaskNames = setOf("assembleRelease", "packageRelease", "bundleRelease", "signReleaseBundle")
val defaultControlPlaneUrl = signingValue("VIRTROID_DEFAULT_CONTROL_PLANE_URL")
    ?: "https://virtroid.network"
val defaultControlPlaneUsesCleartext = defaultControlPlaneUrl.startsWith("http://", ignoreCase = true)
val debugControlPlaneUrl = signingValue("VIRTROID_DEBUG_CONTROL_PLANE_URL")
    ?: defaultControlPlaneUrl
val debugControlPlaneUsesCleartext = debugControlPlaneUrl.startsWith("http://", ignoreCase = true)
android {
    namespace = "io.virtroid.client"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.virtroid.client"
        minSdk = 28
        targetSdk = 36
        versionCode = 3
        versionName = "0.2.1"
        buildConfigField("String", "DEFAULT_CONTROL_PLANE_URL", buildConfigString(defaultControlPlaneUrl))
        manifestPlaceholders["usesCleartextTraffic"] = defaultControlPlaneUsesCleartext
    }

    signingConfigs {
        create("release") {
            if (releaseSigningConfigured) {
                storeFile = file(checkNotNull(releaseStoreFilePath))
                storePassword = releaseStorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeyPassword
                enableV1Signing = false
                enableV2Signing = true
                enableV3Signing = true
            }
        }
    }

    buildTypes {
        debug {
            buildConfigField("String", "DEFAULT_CONTROL_PLANE_URL", buildConfigString(debugControlPlaneUrl))
            manifestPlaceholders["usesCleartextTraffic"] = debugControlPlaneUsesCleartext
        }

        release {
            isMinifyEnabled = true
            if (releaseSigningConfigured) {
                signingConfig = signingConfigs.getByName("release")
            }
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    buildFeatures {
        viewBinding = true
        buildConfig = true
    }
}

androidComponents {
    beforeVariants(selector().withBuildType("release")) {
        if (defaultControlPlaneUsesCleartext) {
            throw GradleException("Release builds require an HTTPS VIRTROID_DEFAULT_CONTROL_PLANE_URL.")
        }
    }
}

val verifyReleaseSigningConfiguration = tasks.register("verifyReleaseSigningConfiguration") {
    group = "verification"
    description = "Fails closed unless every release-signing input is present and the keystore is readable."
    doLast {
        val missing = releaseSigningValues
            .filterValues { it.isNullOrBlank() }
            .keys
        if (missing.isNotEmpty()) {
            throw GradleException(
                "Release artifact generation requires signing configuration: ${missing.joinToString()}.",
            )
        }
        val store = file(checkNotNull(releaseStoreFilePath))
        if (!store.isFile || !store.canRead()) {
            throw GradleException("Release keystore is missing or unreadable: ${store.absolutePath}")
        }
    }
}

tasks.matching {
    it.name in releaseArtifactTaskNames
}.configureEach {
    dependsOn(verifyReleaseSigningConfiguration)
}

val verifyReleaseSecurityManifest = tasks.register("verifyReleaseSecurityManifest") {
    dependsOn("processReleaseMainManifest")
    doLast {
        val manifestRoot = layout.buildDirectory.dir("intermediates/merged_manifest/release").get().asFile
        val manifest = manifestRoot.walkTopDown()
            .firstOrNull { it.isFile && it.name == "AndroidManifest.xml" }
            ?: throw GradleException("Release merged AndroidManifest.xml was not found.")
        val text = manifest.readText()
        fun requireManifestControl(ok: Boolean, message: String) {
            if (!ok) throw GradleException("Release manifest security gate failed: $message")
        }
        requireManifestControl(!text.contains("android:debuggable=\"true\""), "debuggable=true")
        requireManifestControl(!text.contains("android:testOnly=\"true\""), "testOnly=true")
        requireManifestControl(
            text.contains("android:allowBackup=\"false\"") || text.contains("android:allowBackup=\"0\""),
            "allowBackup must be false",
        )
        requireManifestControl(
            text.contains("android:usesCleartextTraffic=\"false\"") || text.contains("android:usesCleartextTraffic=\"0\""),
            "usesCleartextTraffic must be false",
        )
        requireManifestControl(
            text.contains("android:networkSecurityConfig="),
            "networkSecurityConfig must be present",
        )
        requireManifestControl(
            text.contains("android:dataExtractionRules="),
            "dataExtractionRules must be present",
        )
        requireManifestControl(
            text.contains("android.permission.FOREGROUND_SERVICE_REMOTE_MESSAGING"),
            "remote-messaging foreground-service permission must be present",
        )
        requireManifestControl(
            text.contains("io.virtroid.client.push.NotificationRelayService") &&
                text.contains("android:foregroundServiceType=\"remoteMessaging\""),
            "built-in remote-messaging relay service must be present",
        )
        requireManifestControl(
            !text.contains("firebase", ignoreCase = true) &&
                !text.contains("com.google.firebase.MESSAGING_EVENT"),
            "third-party messaging components must not be present",
        )
        requireManifestControl(!text.contains("UiPreviewActivity"), "debug preview activity leaked into release")
    }
}

tasks.matching { it.name in releaseArtifactTaskNames }.configureEach {
    dependsOn(verifyReleaseSecurityManifest)
}

dependencies {
    implementation("androidx.core:core-ktx:1.17.0")
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.biometric:biometric:1.1.0")
    implementation("androidx.drawerlayout:drawerlayout:1.2.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.9.4")
    implementation("com.google.android.material:material:1.13.0")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    testImplementation("junit:junit:4.13.2")
}
