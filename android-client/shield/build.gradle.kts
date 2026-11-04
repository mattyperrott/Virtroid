// SPDX-License-Identifier: GPL-3.0-or-later
import java.util.Properties
import org.gradle.api.GradleException

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
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
    namespace = "io.virtroid.shield"
    compileSdk = 36

    defaultConfig {
        applicationId = "io.virtroid.shield"
        // The cloud phone runs Android 16; the watcher's special-use foreground service needs 34.
        minSdk = 34
        targetSdk = 36
        versionCode = 5
        versionName = "0.3.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
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

    buildFeatures {
        viewBinding = true
        buildConfig = true
        compose = true
    }

    // The circuits' proving keys and wasm are read whole by the engine;
    // compressing them in the APK saves nothing (they're random field data).
    androidResources {
        noCompress += listOf("zkey", "wasm")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

// The engine bundle and the circuits are built from 0xbow's SDK by the
// engine's own scripts (engine/README.md), not by Gradle: fail clearly when
// they're missing instead of shipping an app with no engine.
val checkEngineAssets = tasks.register("checkEngineAssets") {
    group = "verification"
    val engine = file("src/main/assets/engine/engine.js")
    val circuits = file("src/main/assets/circuits/transact_2x2/groth16_pkey.zkey")
    doLast {
        if (!engine.isFile) throw GradleException("Missing ${engine.path}: run `pnpm build` in shield/engine.")
        if (!circuits.isFile) throw GradleException("Missing ${circuits.path}: run `pnpm circuits` in shield/engine.")
    }
}
tasks.named("preBuild") { dependsOn(checkEngineAssets) }

val verifyReleaseSigningConfiguration = tasks.register("verifyReleaseSigningConfiguration") {
    group = "verification"
    doLast {
        val missing = releaseSigningValues.filterValues { it.isNullOrBlank() }.keys
        if (missing.isNotEmpty()) {
            throw GradleException("Shield release requires signing configuration: ${missing.joinToString()}.")
        }
    }
}
tasks.matching { it.name == "assembleRelease" }.configureEach { dependsOn(verifyReleaseSigningConfiguration) }

dependencies {
    implementation("androidx.core:core-ktx:1.17.0")
    implementation("androidx.appcompat:appcompat:1.7.1")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.9.4")
    implementation("androidx.webkit:webkit:1.17.1")
    implementation("com.google.android.material:material:1.13.0")
    // The screens (ui): Compose. Compose 1.11 and
    // lifecycle 2.9: their next versions need compileSdk 37, which AGP 9.1 doesn't take.
    implementation(platform("androidx.compose:compose-bom:2026.06.01"))
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.activity:activity-compose:1.13.0")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.9.4")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.9.4")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    // Keccak, for an address's EIP-55 checksum.
    implementation("org.bouncycastle:bcprov-jdk18on:1.86")
    // The deposit address's QR code.
    implementation("com.google.zxing:core:3.5.3")
    testImplementation("junit:junit:4.13.2")
    // Android's org.json is only a stub in JVM unit tests.
    testImplementation("org.json:json:20260522")
}
