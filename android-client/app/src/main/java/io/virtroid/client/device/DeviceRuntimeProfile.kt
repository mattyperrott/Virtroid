package io.virtroid.client.device

import android.content.Context
import android.os.Build
import android.util.TypedValue
import android.view.WindowManager
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
        private val PORTRAIT_PROFILE_BUCKETS = listOf(
            DisplayBucket(widthPx = 720, heightPx = 1600, densityDpi = 320),
            DisplayBucket(widthPx = 768, heightPx = 1600, densityDpi = 340),
            DisplayBucket(widthPx = 800, heightPx = 1600, densityDpi = 360),
            DisplayBucket(widthPx = 900, heightPx = 1600, densityDpi = 360),
            DisplayBucket(widthPx = 960, heightPx = 1600, densityDpi = 400),
        )
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
            val effectiveShort = min(effectiveWidth, effectiveHeight)
            val effectiveLong = max(effectiveWidth, effectiveHeight)
            val physicalAspect = effectiveShort.toFloat() / effectiveLong.toFloat()
            val bucket = PORTRAIT_PROFILE_BUCKETS.minBy { bucket ->
                kotlin.math.abs(bucket.aspect - physicalAspect)
            }
            val widthPx = if (portrait) bucket.widthPx else bucket.heightPx
            val heightPx = if (portrait) bucket.heightPx else bucket.widthPx

            val runtimeName = "Runtime " + UUID.randomUUID().toString().substring(0, 8)

            return DeviceRuntimeProfile(
                runtimeName = runtimeName,
                widthPx = widthPx,
                heightPx = heightPx,
                densityDpi = bucket.densityDpi,
            )
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

        private data class DisplayBucket(
            val widthPx: Int,
            val heightPx: Int,
            val densityDpi: Int,
        ) {
            val aspect: Float = widthPx.toFloat() / heightPx.toFloat()
        }
    }
}
