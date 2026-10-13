package io.virtroid.client

private val GENERATED_RUNTIME_NAME = Regex("^Runtime ([0-9a-f]{8})$")

/**
 * The name to show for a runtime. Generated names used to be "Runtime"
 * followed by eight hex digits; like new ones, they show as the digits alone.
 * Names the user chose are shown as they are.
 */
internal fun runtimeDisplayName(name: String): String =
    GENERATED_RUNTIME_NAME.matchEntire(name.trim())?.groupValues?.get(1) ?: name
