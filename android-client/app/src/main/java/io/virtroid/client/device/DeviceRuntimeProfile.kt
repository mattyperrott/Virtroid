package io.virtroid.client.device

import android.content.Context
import android.os.Build
import android.view.WindowManager
import android.util.TypedValue
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
        private const val MIN_SAFE_SHORT_EDGE = 480
        private const val DIMENSION_GRANULARITY = 8
        private const val SESSION_TOP_BAR_DP = 76f

        fun from(context: Context): DeviceRuntimeProfile {
            val metrics = context.resources.displayMetrics
            val windowSize = context.viewerWindowSize()
            val rawWidth = windowSize.first.coerceAtLeast(1)
            val rawHeight = windowSize.second.coerceAtLeast(1)
            val portrait = rawWidth <= rawHeight
            val viewerToolbarPx = TypedValue.applyDimension(
                TypedValue.COMPLEX_UNIT_DIP,
                SESSION_TOP_BAR_DP,
                metrics,
            ).roundToInt()
            val effectiveWidth = if (portrait) rawWidth else (rawWidth - viewerToolbarPx).coerceAtLeast(rawWidth / 2)
            val effectiveHeight = if (portrait) (rawHeight - viewerToolbarPx).coerceAtLeast(rawHeight / 2) else rawHeight
            val shortEdge = min(effectiveWidth, effectiveHeight)
            val longEdge = max(effectiveWidth, effectiveHeight)

            val targetLong = min(longEdge, MAX_SAFE_LONG_EDGE)
            val scale = targetLong / longEdge.toFloat()
            val scaledLong = roundToAligned(targetLong)
            val scaledShort = roundToAligned((shortEdge * scale).toInt()).coerceAtLeast(MIN_SAFE_SHORT_EDGE)

            val widthPx: Int
            val heightPx: Int
            if (portrait) {
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

        private fun Context.viewerWindowSize(): Pair<Int, Int> {
            val windowManager = getSystemService(WindowManager::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R && windowManager != null) {
                val bounds = windowManager.currentWindowMetrics.bounds
                val width = bounds.width()
                val height = bounds.height()
                if (width > 0 && height > 0) {
                    return width to height
                }
            }
            val metrics = resources.displayMetrics
            return metrics.widthPixels to metrics.heightPixels
        }
    }
}
