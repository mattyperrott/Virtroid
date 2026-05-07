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
val defaultControlPlaneUrl = signingValue("VIRTROID_DEFAULT_CONTROL_PLANE_URL")
    ?: "https://virtroid.network"
val defaultControlPlaneUsesCleartext = defaultControlPlaneUrl.startsWith("http://", ignoreCase = true)

android {
    namespace = "io.virtroid.client"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.virtroid.client"
        minSdk = 28
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
        buildConfigField("String", "DEFAULT_CONTROL_PLANE_URL", buildConfigString(defaultControlPlaneUrl))
        manifestPlaceholders["usesCleartextTraffic"] = defaultControlPlaneUsesCleartext
    }

    signingConfigs {
        create("release") {
            if (!releaseStoreFilePath.isNullOrBlank()) {
                storeFile = file(releaseStoreFilePath)
                storePassword = releaseStorePassword
                keyAlias = releaseKeyAlias
                keyPassword = releaseKeyPassword
                enableV1Signing = true
                enableV2Signing = true
            }
        }
    }

    buildTypes {
        debug {
            manifestPlaceholders["usesCleartextTraffic"] = true
        }

        release {
            isMinifyEnabled = true
            if (!releaseStoreFilePath.isNullOrBlank()) {
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

dependencies {
    implementation("androidx.core:core-ktx:1.17.0")
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.biometric:biometric:1.1.0")
    implementation("androidx.drawerlayout:drawerlayout:1.2.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.9.4")
    implementation("com.google.android.material:material:1.13.0")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
}
