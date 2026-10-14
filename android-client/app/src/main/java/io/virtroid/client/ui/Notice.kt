package io.virtroid.client.ui

import android.app.Activity
import android.content.res.ColorStateList
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
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
        val view = TextView(activity).apply {
            text = message
            textSize = 13f
            maxLines = 4
            setTextColor(activity.getColor(R.color.v_text_primary))
            setPadding(activity.dp(16), activity.dp(10), activity.dp(16), activity.dp(10))
            background = GradientDrawable().apply {
                cornerRadius = activity.dp(14).toFloat()
                setColor(activity.getColor(R.color.v_surface_light))
                setStroke(activity.dp(1), activity.getColor(R.color.v_border))
            }
        }
        present(activity, view, message)
    }

    /** Says why something can't be done, in red, over the bottom of the screen. */
    fun error(activity: Activity, message: String) {
        if (message.isBlank() || activity.isFinishing || activity.isDestroyed) {
            return
        }
        val view = TextView(activity).apply {
            text = message
            textSize = 13f
            maxLines = 3
            setTypeface(typeface, Typeface.BOLD)
            setTextColor(activity.getColor(R.color.v_bg))
            setPadding(activity.dp(16), activity.dp(10), activity.dp(16), activity.dp(10))
            setBackgroundColor(activity.getColor(R.color.v_danger))
        }
        present(activity, view, message)
    }

    /**
     * Confirms something done, such as a copy, in the brand colour: a tick
     * and the message on the accent, under the top bar, clear of the
     * clipboard preview Android 13 and later show at the bottom.
     */
    fun confirm(activity: Activity, message: String) {
        if (message.isBlank() || activity.isFinishing || activity.isDestroyed) {
            return
        }
        val onAccent = activity.getColor(R.color.v_bg)
        val view = LinearLayout(activity).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(activity.dp(14), activity.dp(10), activity.dp(18), activity.dp(10))
            setBackgroundColor(activity.getColor(R.color.v_accent))
            addView(
                ImageView(activity).apply {
                    setImageResource(R.drawable.ic_check)
                    imageTintList = ColorStateList.valueOf(onAccent)
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                },
                LinearLayout.LayoutParams(activity.dp(18), activity.dp(18)).apply { marginEnd = activity.dp(10) },
            )
            addView(
                TextView(activity).apply {
                    text = message
                    textSize = 13f
                    maxLines = 2
                    setTypeface(typeface, Typeface.BOLD)
                    setTextColor(onAccent)
                },
            )
        }
        present(activity, view, message, atTop = true)
    }

    private fun present(activity: Activity, view: View, message: String, atTop: Boolean = false) {
        val root = activity.findViewById<ViewGroup>(android.R.id.content) ?: return
        root.findViewWithTag<View>(TAG)?.let { root.removeView(it) }
        view.tag = TAG
        view.alpha = 0f
        view.importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_YES
        view.accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        view.contentDescription = message
        val bars = ViewCompat.getRootWindowInsets(root)?.getInsets(WindowInsetsCompat.Type.systemBars())
        val params = FrameLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT,
            ViewGroup.LayoutParams.WRAP_CONTENT,
            (if (atTop) Gravity.TOP else Gravity.BOTTOM) or Gravity.CENTER_HORIZONTAL,
        ).apply {
            if (atTop) {
                // Across the bottom edge of the screens' top bar, clear of the content.
                setMargins(activity.dp(24), (bars?.top ?: 0) + activity.dp(102), activity.dp(24), 0)
            } else {
                setMargins(activity.dp(24), 0, activity.dp(24), (bars?.bottom ?: 0) + activity.dp(96))
            }
        }
        root.addView(view, params)
        view.animate().alpha(1f).setDuration(FADE_MS).start()
        // Long messages stay up a little longer.
        val visibleMs = (2_400L + message.length * 25L).coerceAtMost(6_000L)
        view.postDelayed({
            view.animate().alpha(0f).setDuration(FADE_MS).withEndAction { root.removeView(view) }.start()
        }, visibleMs)
    }

    private fun Activity.dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()
}
