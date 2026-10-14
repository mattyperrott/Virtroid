package io.virtroid.client.api

/**
 * How long a runtime may keep running with no viewer open before it stops and
 * saves, as the control plane offers them. Running keeps its apps receiving
 * messages and notifications.
 */
object RuntimeIdlePeriods {
    const val DEFAULT_SECONDS = 30 * 60
    val SECONDS = listOf(
        30 * 60,
        60 * 60,
        3 * 60 * 60,
        6 * 60 * 60,
        8 * 60 * 60,
        12 * 60 * 60,
        24 * 60 * 60,
        3 * 24 * 60 * 60,
        7 * 24 * 60 * 60,
    )
}
