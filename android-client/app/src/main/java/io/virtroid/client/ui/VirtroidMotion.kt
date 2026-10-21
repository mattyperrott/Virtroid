package io.virtroid.client.ui

import android.animation.Animator
import android.animation.AnimatorListenerAdapter
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.annotation.SuppressLint
import android.os.Build
import android.view.HapticFeedbackConstants
import android.view.MotionEvent
import android.view.View
import android.view.animation.AlphaAnimation
import android.view.animation.Animation
import android.view.animation.DecelerateInterpolator
import android.view.animation.OvershootInterpolator

/**
 * Small, quick motion for the app's controls. Every effect is skipped when
 * the phone has animations turned off; haptics follow the phone's own touch
 * feedback setting.
 */
object VirtroidMotion {
    private const val PRESS_SCALE = 0.9f
    private const val SHAKE_MS = 420L

    private val enabled: Boolean
        get() = ValueAnimator.areAnimatorsEnabled()

    fun setStatusPulse(view: View, active: Boolean) {
        view.clearAnimation()
        view.alpha = 1f
        if (!active || !enabled) return

        view.startAnimation(
            AlphaAnimation(0.42f, 1f).apply {
                duration = 900L
                repeatMode = Animation.REVERSE
                repeatCount = Animation.INFINITE
            },
        )
    }

    /** A key that dips under the finger with a keyboard tick and springs back on release. */
    @SuppressLint("ClickableViewAccessibility")
    fun pressFeedback(view: View) {
        // Returning false leaves the click (and its accessibility action) to the view.
        view.setOnTouchListener { v, event ->
            when (event.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    v.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP)
                    if (enabled) {
                        v.animate().scaleX(PRESS_SCALE).scaleY(PRESS_SCALE)
                            .setDuration(70L).setInterpolator(DecelerateInterpolator()).start()
                    }
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL ->
                    v.animate().scaleX(1f).scaleY(1f)
                        .setDuration(if (enabled) 220L else 0L).setInterpolator(OvershootInterpolator(2.5f)).start()
            }
            false
        }
    }

    /** Something appearing: it grows in with a small overshoot. */
    fun popIn(view: View) {
        if (!enabled) return
        view.scaleX = 0.4f
        view.scaleY = 0.4f
        view.animate().scaleX(1f).scaleY(1f)
            .setDuration(220L).setInterpolator(OvershootInterpolator(3f)).start()
    }

    /** Something emptied: a quick dip and back. */
    fun dip(view: View) {
        if (!enabled) return
        view.animate().scaleX(0.6f).scaleY(0.6f).setDuration(70L).withEndAction {
            view.animate().scaleX(1f).scaleY(1f).setDuration(140L).setInterpolator(DecelerateInterpolator()).start()
        }.start()
    }

    /** A wrong entry: a short horizontal shake, then [onEnd]. */
    fun shake(view: View, onEnd: () -> Unit = {}) {
        if (!enabled) {
            onEnd()
            return
        }
        val d = 12f * view.resources.displayMetrics.density
        ObjectAnimator.ofFloat(view, View.TRANSLATION_X, 0f, -d, d, -0.7f * d, 0.7f * d, -0.35f * d, 0.35f * d, 0f).apply {
            duration = SHAKE_MS
            addListener(object : AnimatorListenerAdapter() {
                override fun onAnimationEnd(animation: Animator) {
                    view.translationX = 0f
                    onEnd()
                }
            })
            start()
        }
    }

    /** A correct entry: each view swells in turn. Returns how long the wave lasts. */
    fun wave(views: List<View>): Long {
        if (!enabled) return 0L
        views.forEachIndexed { index, view ->
            // A dot still popping in starts the wave from full size, not mid-pop.
            view.animate().cancel()
            view.scaleX = 1f
            view.scaleY = 1f
            view.animate().scaleX(1.4f).scaleY(1.4f)
                .setStartDelay(index * 35L).setDuration(90L).setInterpolator(DecelerateInterpolator())
                .withEndAction {
                    view.animate().scaleX(1f).scaleY(1f).setStartDelay(0L).setDuration(140L).start()
                }.start()
        }
        return views.size * 35L + 230L
    }

    /** One swell and back, for an icon confirming something. */
    fun pulse(view: View) {
        if (!enabled) return
        view.animate().scaleX(1.12f).scaleY(1.12f).setDuration(120L).withEndAction {
            view.animate().scaleX(1f).scaleY(1f).setDuration(180L).setInterpolator(OvershootInterpolator(2f)).start()
        }.start()
    }

    /** A screen's parts rising into place one after another as it opens. */
    fun riseIn(views: List<View>) {
        if (!enabled) return
        val rise = 16f * (views.firstOrNull()?.resources?.displayMetrics?.density ?: return)
        views.forEachIndexed { index, view ->
            view.alpha = 0f
            view.translationY = rise
            view.animate().alpha(1f).translationY(0f)
                .setStartDelay(index * 30L).setDuration(240L).setInterpolator(DecelerateInterpolator(1.6f)).start()
        }
    }

    /** The phone's "that worked" vibration, where it has one. */
    fun confirmHaptic(view: View) {
        view.performHapticFeedback(
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) HapticFeedbackConstants.CONFIRM else HapticFeedbackConstants.VIRTUAL_KEY,
        )
    }

    /** The phone's "that was wrong" vibration, where it has one. */
    fun rejectHaptic(view: View) {
        view.performHapticFeedback(
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) HapticFeedbackConstants.REJECT else HapticFeedbackConstants.LONG_PRESS,
        )
    }
}
