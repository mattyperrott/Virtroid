package io.virtroid.client.device

import android.content.Context
import android.os.Build
import android.view.LayoutInflater
import android.view.View
import android.view.WindowManager
import io.virtroid.client.databinding.ScreenSessionViewerBinding
import kotlin.math.max
import kotlin.math.min
import java.util.UUID

data class DeviceRuntimeProfile(
    val runtimeName: String,
    val widthPx: Int,
    val heightPx: Int,
    val densityDpi: Int,
) {
    companion object {
        // A runtime's screen is one of these shapes (turned for landscape),
        // so apps in it don't learn the phone's exact screen. Between 20:9
        // and 2:1, where most phones' viewer areas fall, they step by 1% of
        // aspect, so the stream fills the viewer's height and all but a few
        // pixels of its width. The control plane takes widths in multiples of
        // 8 and densities in multiples of 20; each density keeps the screen
        // about 360dp across.
        private val PORTRAIT_PROFILE_BUCKETS = listOf(
            DisplayBucket(widthPx = 720, heightPx = 1600, densityDpi = 320),
            DisplayBucket(widthPx = 736, heightPx = 1600, densityDpi = 320),
            DisplayBucket(widthPx = 752, heightPx = 1600, densityDpi = 340),
            DisplayBucket(widthPx = 768, heightPx = 1600, densityDpi = 340),
            DisplayBucket(widthPx = 784, heightPx = 1600, densityDpi = 340),
            DisplayBucket(widthPx = 800, heightPx = 1600, densityDpi = 360),
            DisplayBucket(widthPx = 896, heightPx = 1600, densityDpi = 360),
            DisplayBucket(widthPx = 960, heightPx = 1600, densityDpi = 400),
        )
        private const val VIEWER_AREA_PREFS = "viewer_area"

        /** The profile for this phone: the shape that best fills the session screen's stream area. */
        fun from(context: Context): DeviceRuntimeProfile {
            val window = context.viewerWindowSize()
            val (areaWidth, areaHeight) = context.rememberedViewerArea(window)
                ?: context.measuredViewerArea(window)
            return forViewerArea(areaWidth, areaHeight)
        }

        /** The profile whose screen best fills a stream area of this size. */
        internal fun forViewerArea(areaWidth: Int, areaHeight: Int): DeviceRuntimeProfile {
            val width = areaWidth.coerceAtLeast(1)
            val height = areaHeight.coerceAtLeast(1)
            val portrait = width <= height
            val aspect = min(width, height).toFloat() / max(width, height).toFloat()
            // The widest shape no wider than the area, so the stream fills its
            // height and what is left is a sliver at each side, not a band at
            // the bottom. An area narrower than every shape gets the narrowest.
            val bucket = PORTRAIT_PROFILE_BUCKETS.filter { it.aspect <= aspect }.maxByOrNull { it.aspect }
                ?: PORTRAIT_PROFILE_BUCKETS.minBy { it.aspect }
            return DeviceRuntimeProfile(
                runtimeName = UUID.randomUUID().toString().substring(0, 8),
                widthPx = if (portrait) bucket.widthPx else bucket.heightPx,
                heightPx = if (portrait) bucket.heightPx else bucket.widthPx,
                densityDpi = bucket.densityDpi,
            )
        }

        /**
         * Records the stream area the session screen measured, for this window
         * size. It accounts for what the layout alone can't, such as a camera
         * cutout the window keeps clear of.
         */
        fun rememberViewerArea(context: Context, areaWidth: Int, areaHeight: Int) {
            if (areaWidth <= 0 || areaHeight <= 0) return
            val (windowWidth, windowHeight) = context.viewerWindowSize()
            val prefs = context.getSharedPreferences(VIEWER_AREA_PREFS, Context.MODE_PRIVATE)
            val key = "${windowWidth}x$windowHeight"
            val value = "${areaWidth}x$areaHeight"
            if (prefs.getString(key, null) != value) {
                prefs.edit().putString(key, value).apply()
            }
        }

        private fun Context.rememberedViewerArea(window: Pair<Int, Int>): Pair<Int, Int>? {
            val value = getSharedPreferences(VIEWER_AREA_PREFS, Context.MODE_PRIVATE)
                .getString("${window.first}x${window.second}", null) ?: return null
            val width = value.substringBefore('x').toIntOrNull() ?: return null
            val height = value.substringAfter('x').toIntOrNull() ?: return null
            return if (width > 0 && height > 0) width to height else null
        }

        /**
         * Where the session screen shows the stream in a window of this size:
         * its layout measured with the system bars hidden, as it keeps them,
         * so the window less the viewer's toolbar.
         */
        private fun Context.measuredViewerArea(window: Pair<Int, Int>): Pair<Int, Int> {
            val binding = ScreenSessionViewerBinding.inflate(LayoutInflater.from(this))
            binding.root.measure(
                View.MeasureSpec.makeMeasureSpec(window.first.coerceAtLeast(1), View.MeasureSpec.EXACTLY),
                View.MeasureSpec.makeMeasureSpec(window.second.coerceAtLeast(1), View.MeasureSpec.EXACTLY),
            )
            val container = binding.sessionSurfaceContainer
            return container.measuredWidth to container.measuredHeight
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
