import java.util.Properties

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

val releaseStoreFilePath = signingValue("VIRTDROID_RELEASE_STORE_FILE")
val releaseStorePassword = signingValue("VIRTDROID_RELEASE_STORE_PASSWORD")
val releaseKeyAlias = signingValue("VIRTDROID_RELEASE_KEY_ALIAS")
val releaseKeyPassword = signingValue("VIRTDROID_RELEASE_KEY_PASSWORD")
val defaultControlPlaneUrl = signingValue("VIRTDROID_DEFAULT_CONTROL_PLANE_URL")
    ?: "https://176.126.70.76"
val defaultBootstrapToken = signingValue("VIRTDROID_BOOTSTRAP_INVITE_TOKEN") ?: ""

android {
    namespace = "io.virtdroid.client"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.virtdroid.client"
        minSdk = 28
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
        buildConfigField("String", "DEFAULT_CONTROL_PLANE_URL", buildConfigString(defaultControlPlaneUrl))
        buildConfigField("String", "DEFAULT_BOOTSTRAP_INVITE_TOKEN", buildConfigString(defaultBootstrapToken))
        manifestPlaceholders["usesCleartextTraffic"] = false
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

dependencies {
    implementation("androidx.core:core-ktx:1.17.0")
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.drawerlayout:drawerlayout:1.2.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.9.4")
    implementation("com.google.android.material:material:1.13.0")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
}
