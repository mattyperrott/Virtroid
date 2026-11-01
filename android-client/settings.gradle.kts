pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "VirtroidClient"
include(":app")
include(":runtime-agent")
// The cloud phone's shield app: GPL-3.0-or-later, unlike the rest (shield/README.md).
include(":shield")
include(":scrcpy-server")
project(":scrcpy-server").projectDir = file("../third_party/scrcpy-server")
