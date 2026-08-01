plugins {
    id("com.android.application")
}

android {
    namespace = "org.server.scrcpy"
    compileSdk = 36

    defaultConfig {
        applicationId = "org.server.scrcpy"
        minSdk = 21
        targetSdk = 36
        versionCode = 5
        versionName = "1.3.1-virtroid.1"
    }

    buildFeatures {
        aidl = true
        buildConfig = true
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
}

tasks.register<Copy>("syncEmbeddedAsset") {
    group = "build"
    description = "Builds and copies the scrcpy server payload into the node's embedded assets."
    dependsOn("assembleRelease")
    from(layout.buildDirectory.file("outputs/apk/release/scrcpy-server-release-unsigned.apk"))
    into(rootProject.layout.projectDirectory.dir("../backend/cmd/virtnoded/assets"))
    rename { "scrcpy-server.jar" }
}
