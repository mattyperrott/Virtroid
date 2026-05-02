package io.virtdroid.client.device

import android.content.Context
import kotlin.math.max
import kotlin.math.min
import kotlin.math.roundToInt
import java.util.UUID

data class DeviceRuntimeProfile(
    val runtimeName: String,
    val widthPx: Int,
    val heightPx: Int,
    val densityDpi: Int,
) {
    companion object {
        private const val MAX_SAFE_LONG_EDGE = 1600
        private const val MIN_SAFE_SHORT_EDGE = 720
        private const val DIMENSION_GRANULARITY = 8

        fun from(context: Context): DeviceRuntimeProfile {
            val metrics = context.resources.displayMetrics
            val rawWidth = metrics.widthPixels.coerceAtLeast(1)
            val rawHeight = metrics.heightPixels.coerceAtLeast(1)
            val shortEdge = min(rawWidth, rawHeight)
            val longEdge = max(rawWidth, rawHeight)

            val scale = minOf(1f, MAX_SAFE_LONG_EDGE / longEdge.toFloat())
            val scaledShort = roundToAligned((shortEdge * scale).toInt()).coerceAtLeast(MIN_SAFE_SHORT_EDGE)
            val scaledLong = roundToAligned((longEdge * scale).toInt()).coerceAtLeast(MIN_SAFE_SHORT_EDGE)

            val widthPx: Int
            val heightPx: Int
            if (rawWidth <= rawHeight) {
                widthPx = scaledShort
                heightPx = scaledLong
            } else {
                widthPx = scaledLong
                heightPx = scaledShort
            }

            val runtimeName = "Runtime " + UUID.randomUUID().toString().substring(0, 8)

            val scaledDensity = (metrics.densityDpi * scale)
                .roundToInt()
                .coerceAtLeast(240)
                .let { it - (it % 20) }

            return DeviceRuntimeProfile(
                runtimeName = runtimeName,
                widthPx = widthPx,
                heightPx = heightPx,
                densityDpi = scaledDensity,
            )
        }

        private fun roundToAligned(value: Int): Int {
            if (value <= DIMENSION_GRANULARITY) {
                return DIMENSION_GRANULARITY
            }
            return value - (value % DIMENSION_GRANULARITY)
        }
    }
}
