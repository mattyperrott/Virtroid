import java.util.Properties
import org.gradle.api.GradleException

plugins {
    id("com.android.application")
}

val localProperties = Properties().apply {
    val file = rootProject.file("local.properties")
    if (file.exists()) file.inputStream().use(::load)
}

fun signingValue(name: String): String? =
    providers.environmentVariable(name).orNull ?: localProperties.getProperty(name)

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

android {
    namespace = "io.virtroid.runtimeagent"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.virtroid.runtimeagent"
        minSdk = 28
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
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
        release {
            isMinifyEnabled = true
            if (releaseSigningConfigured) signingConfig = signingConfigs.getByName("release")
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

val verifyReleaseSigningConfiguration = tasks.register("verifyReleaseSigningConfiguration") {
    group = "verification"
    doLast {
        val missing = releaseSigningValues.filterValues { it.isNullOrBlank() }.keys
        if (missing.isNotEmpty()) {
            throw GradleException("Runtime-agent release requires signing configuration: ${missing.joinToString()}.")
        }
        val store = file(checkNotNull(releaseStoreFilePath))
        if (!store.isFile || !store.canRead()) {
            throw GradleException("Release keystore is missing or unreadable: ${store.absolutePath}")
        }
    }
}

tasks.matching { it.name in setOf("assembleRelease", "packageRelease") }.configureEach {
    dependsOn(verifyReleaseSigningConfiguration)
}
