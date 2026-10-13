package io.virtroid.client.ui

import android.app.Activity
import android.graphics.drawable.GradientDrawable
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.TextView
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import io.virtroid.client.R

/**
 * The app's own short message in place of a stock toast: a pill in the app's
 * colours over the bottom of the current screen, gone after a few seconds.
 * Screens use it only for what they do not already show, mostly errors.
 */
object Notice {
    private const val TAG = "virtroid-notice"
    private const val FADE_MS = 160L

    fun show(activity: Activity, message: String) {
        if (message.isBlank() || activity.isFinishing || activity.isDestroyed) {
            return
        }
        val root = activity.findViewById<ViewGroup>(android.R.id.content) ?: return
        root.findViewWithTag<View>(TAG)?.let { root.removeView(it) }

        val density = activity.resources.displayMetrics.density
        fun dp(value: Int) = (value * density).toInt()
        val view = TextView(activity).apply {
            tag = TAG
            text = message
            textSize = 13f
            maxLines = 4
            setTextColor(activity.getColor(R.color.v_text_primary))
            setPadding(dp(16), dp(10), dp(16), dp(10))
            background = GradientDrawable().apply {
                cornerRadius = dp(14).toFloat()
                setColor(activity.getColor(R.color.v_surface_light))
                setStroke(dp(1), activity.getColor(R.color.v_border))
            }
            alpha = 0f
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_YES
            accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        val bottomInset = ViewCompat.getRootWindowInsets(root)
            ?.getInsets(WindowInsetsCompat.Type.systemBars())?.bottom ?: 0
        val params = FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT,
            ViewGroup.LayoutParams.WRAP_CONTENT,
            Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL,
        ).apply {
            setMargins(dp(24), 0, dp(24), bottomInset + dp(96))
        }
        root.addView(view, params)
        view.animate().alpha(1f).setDuration(FADE_MS).start()
        // Long messages stay up a little longer.
        val visibleMs = (2_400L + message.length * 25L).coerceAtMost(6_000L)
        view.postDelayed({
            view.animate().alpha(0f).setDuration(FADE_MS).withEndAction { root.removeView(view) }.start()
        }, visibleMs)
    }
}
