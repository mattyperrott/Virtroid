package io.virtroid.client.ui

import android.animation.Animator
import android.animation.AnimatorInflater
import android.animation.AnimatorListenerAdapter
import android.animation.ObjectAnimator
import android.animation.ValueAnimator
import android.annotation.SuppressLint
import android.app.Activity
import android.app.ActivityOptions
import android.content.res.ColorStateList
import android.graphics.drawable.ColorDrawable
import android.graphics.drawable.Drawable
import android.graphics.drawable.TransitionDrawable
import android.os.Build
import android.os.Bundle
import android.os.SystemClock
import android.transition.ChangeBounds
import android.transition.Fade
import android.transition.TransitionManager
import android.transition.TransitionSet
import android.view.HapticFeedbackConstants
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.view.animation.AccelerateDecelerateInterpolator
import android.view.animation.AccelerateInterpolator
import android.view.animation.AlphaAnimation
import android.view.animation.Animation
import android.view.animation.DecelerateInterpolator
import android.view.animation.LinearInterpolator
import android.view.animation.OvershootInterpolator
import android.widget.Button
import android.widget.CompoundButton
import android.widget.EditText
import android.widget.ImageView
import android.widget.TextView
import androidx.annotation.AnimRes
import androidx.annotation.ColorInt
import androidx.annotation.DrawableRes
import androidx.appcompat.content.res.AppCompatResources
import androidx.core.graphics.ColorUtils
import androidx.core.view.children
import androidx.core.view.isVisible
import com.google.android.material.button.MaterialButton
import com.google.android.material.progressindicator.CircularProgressIndicatorSpec
import com.google.android.material.progressindicator.IndeterminateDrawable
import io.virtroid.client.R
import kotlin.math.PI
import kotlin.math.sin

/**
 * Small, quick motion for the app's controls. Every effect is skipped when
 * the phone has animations turned off; haptics follow the phone's own touch
 * feedback setting.
 */
object VirtroidMotion {
    private const val PRESS_SCALE = 0.9f
    private const val SPINNER_DP = 20f
    private const val KEY_LIGHT_ALPHA = 56
    private const val KEY_FADE_MS = 320L
    private const val WAVE_DELAY_MS = 120L
    private const val WAVE_MS = 760L
    private const val SHAKE_MS = 420L
    private const val CHANGE_MS = 240L
    private const val LEAVE_MS = 100L
    private const val ARRIVE_DELAY_MS = 70L
    private const val FADE_IN_MS = 140L
    private const val FADE_OUT_MS = 220L

    /** Whether the phone has animations on. */
    val enabled: Boolean
        get() = ValueAnimator.areAnimatorsEnabled()

    fun setStatusPulse(view: View, active: Boolean) {
        // Already pulsing: starting over on every refresh would make it stutter.
        if (active && enabled && view.animation != null) return
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

    /**
     * A key that lights up and dips the instant it is touched, with a keyboard
     * tick, and fades and springs back on release. The light doesn't wait for
     * the release or for the scroll view's press delay, and fades out from full
     * after even the quickest tap. [label] (the digit) turns to the accent
     * with it.
     */
    @SuppressLint("ClickableViewAccessibility")
    fun keyFeedback(view: View, label: TextView? = null) {
        val accent = view.context.getColor(R.color.v_accent)
        val rest = label?.currentTextColor ?: accent
        val light = ColorDrawable(accent).apply { alpha = 0 }
        view.foreground = light
        view.setTag(R.id.motion_key, true)
        fun show(level: Float) {
            light.alpha = (KEY_LIGHT_ALPHA * level).toInt()
            label?.setTextColor(blend(rest, accent, level))
        }
        var fade: ValueAnimator? = null
        var down = false
        // Returning false leaves the click (and its accessibility action) to the view.
        view.setOnTouchListener { v, event ->
            when (event.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    v.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP)
                    fade?.cancel()
                    down = true
                    show(1f)
                    if (enabled) {
                        v.animate().scaleX(PRESS_SCALE).scaleY(PRESS_SCALE)
                            .setStartDelay(0L).setDuration(50L).setInterpolator(DecelerateInterpolator()).start()
                    }
                }
                MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> if (down) {
                    down = false
                    if (enabled) {
                        fade = ValueAnimator.ofFloat(1f, 0f).apply {
                            duration = KEY_FADE_MS
                            interpolator = DecelerateInterpolator()
                            addUpdateListener { show(it.animatedValue as Float) }
                            start()
                        }
                        v.animate().scaleX(1f).scaleY(1f).setStartDelay(0L)
                            .setDuration(260L).setInterpolator(OvershootInterpolator(3f)).start()
                    } else {
                        show(0f)
                    }
                }
            }
            false
        }
    }

    private fun blend(from: Int, to: Int, level: Float): Int = ColorUtils.blendARGB(from, to, level)

    /**
     * Dots bobbing in turn while something is being worked out; stop it with
     * [stopWaiting]. It starts after a moment, so a quick answer shows none.
     */
    fun waitingWave(views: List<View>): Animator? {
        if (!enabled || views.isEmpty()) return null
        val rise = 7f * views.first().resources.displayMetrics.density
        return ValueAnimator.ofFloat(0f, 1f).apply {
            startDelay = WAVE_DELAY_MS
            duration = WAVE_MS
            repeatCount = ValueAnimator.INFINITE
            interpolator = LinearInterpolator()
            addUpdateListener { animator ->
                val phase = animator.animatedFraction
                views.forEachIndexed { index, view ->
                    // A hump travelling along the row, then a short rest.
                    val at = (phase * 1.5f - index * 0.5f / views.size) / 0.5f
                    view.translationY = if (at in 0f..1f) -rise * sin(at * PI).toFloat() else 0f
                }
            }
            start()
        }
    }

    /** Ends [waitingWave], settling each of [views] where it belongs. */
    fun stopWaiting(wave: Animator?, views: List<View>) {
        wave?.cancel()
        views.forEach { view ->
            if (view.translationY != 0f) {
                if (enabled) view.animate().translationY(0f).setStartDelay(0L).setDuration(90L).setInterpolator(DecelerateInterpolator()).start() else view.translationY = 0f
            }
        }
    }

    /** Something appearing: it grows in with a small overshoot. */
    fun popIn(view: View) {
        if (!enabled) return
        view.scaleX = 0.4f
        view.scaleY = 0.4f
        view.animate().scaleX(1f).scaleY(1f)
            .setStartDelay(0L).setDuration(220L).setInterpolator(OvershootInterpolator(3f)).start()
    }

    /** Something emptied: a quick dip and back. */
    fun dip(view: View) {
        if (!enabled) return
        view.animate().scaleX(0.6f).scaleY(0.6f).setStartDelay(0L).setDuration(70L).withEndAction {
            view.animate().scaleX(1f).scaleY(1f).setStartDelay(0L).setDuration(140L).setInterpolator(DecelerateInterpolator()).start()
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

    /** A correct entry: each view swells in turn. */
    fun wave(views: List<View>) {
        if (!enabled) return
        views.forEachIndexed { index, view ->
            // A dot still popping in or bobbing starts the wave from its place, at full size.
            view.animate().cancel()
            view.scaleX = 1f
            view.scaleY = 1f
            view.translationY = 0f
            view.animate().scaleX(1.5f).scaleY(1.5f)
                .setStartDelay(index * 8L).setDuration(60L).setInterpolator(DecelerateInterpolator())
                .withEndAction {
                    view.animate().scaleX(1f).scaleY(1f).setStartDelay(0L).setDuration(80L).start()
                }.start()
        }
    }

    /** One swell and back, for an icon confirming something. */
    fun pulse(view: View) {
        if (!enabled) return
        view.animate().scaleX(1.12f).scaleY(1.12f).setStartDelay(0L).setDuration(120L).withEndAction {
            view.animate().scaleX(1f).scaleY(1f).setStartDelay(0L).setDuration(180L).setInterpolator(OvershootInterpolator(2f)).start()
        }.start()
    }

    /** Options for opening the screen that takes over from [activity], zooming in towards you. */
    fun zoomInto(activity: Activity): Bundle? =
        ActivityOptions.makeCustomAnimation(activity, R.anim.v_zoom_enter, R.anim.v_zoom_exit).toBundle()

    /**
     * Gives every row and card under [root] that can be tapped, and has no
     * press motion of its own, a small dip under the finger.
     */
    fun pressableRows(root: View) {
        val tappable = root.isClickable && root.stateListAnimator == null && root.getTag(R.id.motion_key) == null
        if (tappable && root !is EditText && root !is CompoundButton && (root is ViewGroup || root is TextView)) {
            root.stateListAnimator = AnimatorInflater.loadStateListAnimator(root.context, R.animator.v_row_press_scale)
        }
        (root as? ViewGroup)?.children?.forEach(::pressableRows)
    }

    /**
     * Shows that [button] is working on what it was tapped for: a spinner takes
     * its icon's place until it is set back. The caller enables and disables it.
     */
    fun setBusy(button: Button, busy: Boolean) {
        if (button !is MaterialButton) return
        val before = button.getTag(R.id.motion_busy) as? RestingIcon
        if (busy == (before != null)) return
        if (before == null) {
            button.setTag(R.id.motion_busy, RestingIcon(button.icon, button.iconTint, button.iconGravity))
            val style = CircularProgressIndicatorSpec(button.context, null).apply {
                indicatorSize = (SPINNER_DP * button.resources.displayMetrics.density).toInt()
                trackThickness = (2f * button.resources.displayMetrics.density).toInt()
                indicatorInset = 0
                indicatorColors = intArrayOf(button.currentTextColor)
            }
            button.iconTint = null
            button.icon = IndeterminateDrawable.createCircularDrawable(button.context, style).apply { setVisible(true, true) }
            if (button.iconGravity != MaterialButton.ICON_GRAVITY_TEXT_END) {
                button.iconGravity = MaterialButton.ICON_GRAVITY_TEXT_START
            }
        } else {
            button.setTag(R.id.motion_busy, null)
            button.icon = before.icon
            button.iconTint = before.tint
            button.iconGravity = before.gravity
        }
    }

    private class RestingIcon(val icon: Drawable?, val tint: ColorStateList?, val gravity: Int)

    /** [button] did what it was tapped for: a tick pops in where its icon or spinner was. */
    fun showDone(button: MaterialButton) {
        setBusy(button, false)
        // In full colour, but not to be tapped again: the screen is moving on.
        button.isEnabled = true
        button.isClickable = false
        button.setIconResource(R.drawable.ic_check)
        button.iconTint = ColorStateList.valueOf(button.currentTextColor)
        button.iconGravity = MaterialButton.ICON_GRAVITY_TEXT_START
        confirmHaptic(button)
        if (enabled) {
            button.animate().scaleX(1.03f).scaleY(1.03f).setStartDelay(0L).setDuration(100L).withEndAction {
                button.animate().scaleX(1f).scaleY(1f).setDuration(200L).setInterpolator(OvershootInterpolator(3f)).start()
            }.start()
        }
    }

    /** [button] could not do what it was tapped for: it shakes, with the phone's "that was wrong" vibration. */
    fun showFailed(button: Button) {
        setBusy(button, false)
        button.isClickable = true
        rejectHaptic(button)
        shake(button)
    }

    /** One turn of a refresh icon; repeated taps add turns. */
    fun spin(view: View) {
        if (!enabled) return
        view.animate().rotationBy(360f).setStartDelay(0L).setDuration(600L)
            .setInterpolator(AccelerateDecelerateInterpolator()).start()
    }

    /** Options for opening something new to fill in, rising over [activity]. */
    fun riseOver(activity: Activity): Bundle? =
        ActivityOptions.makeCustomAnimation(activity, R.anim.v_rise_enter, R.anim.v_screen_hold).toBundle()

    /**
     * Closes [activity], just finished, with [exit] over [enter]. Its theme says
     * so too, but before Android 13 only for its own window: the one beneath
     * would move as its own theme has it.
     */
    @Suppress("DEPRECATION")
    fun closeWith(activity: Activity, @AnimRes enter: Int, @AnimRes exit: Int) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) activity.overridePendingTransition(enter, exit)
    }

    /** Options for going back to a screen that is still there, away from the runtime. */
    fun zoomBack(activity: Activity): Bundle? =
        ActivityOptions.makeCustomAnimation(activity, R.anim.v_zoom_back_enter, R.anim.v_zoom_back_exit).toBundle()

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

    /**
     * Lets what is about to change inside [root] (views shown, hidden, added,
     * removed or resized) move into place. Call it just before the changes.
     * What leaves is mostly gone before what replaces it shows, or with
     * [overlap] they cross-fade: for an outline and what loaded in its place.
     */
    fun animateChanges(root: ViewGroup, overlap: Boolean = false) {
        if (!enabled || !root.isLaidOut || !root.isShown) return
        val arriveDelayMs = if (overlap) 0L else ARRIVE_DELAY_MS
        val leaving = Fade(Fade.OUT).setDuration(if (overlap) CHANGE_MS else LEAVE_MS)
        val arriving = Fade(Fade.IN).setStartDelay(arriveDelayMs).setDuration(CHANGE_MS - arriveDelayMs)
        val changes = TransitionSet()
            .addTransition(leaving)
            .addTransition(arriving)
            .addTransition(ChangeBounds().setDuration(CHANGE_MS))
            .setOrdering(TransitionSet.ORDERING_TOGETHER)
            .setInterpolator(DecelerateInterpolator(1.6f))
        // Fade would show what is inside a hidden group as a ghost when a view
        // around that group is removed. (Only the set's own exclusions count.)
        root.hiddenGroups().forEach { changes.excludeChildren(it, true) }
        TransitionManager.beginDelayedTransition(root, changes)
    }

    private fun ViewGroup.hiddenGroups(): List<ViewGroup> =
        children.filterIsInstance<ViewGroup>()
            .flatMap { if (it.isVisible) it.hiddenGroups() else listOf(it) }
            .toList()

    /**
     * Fades [view] to shown or hidden. Every change of a view's visibility
     * should go through here once any does, or a fade still running undoes it.
     */
    fun setVisible(view: View, visible: Boolean, hidden: Int = View.GONE) {
        val heading = view.getTag(R.id.motion_visible) as? Boolean ?: view.isVisible
        if (heading == visible) return
        view.animate().cancel()
        if (!enabled || !view.isAttachedToWindow) {
            view.setTag(R.id.motion_visible, null)
            view.alpha = 1f
            view.visibility = if (visible) View.VISIBLE else hidden
            return
        }
        view.setTag(R.id.motion_visible, visible)
        if (visible) {
            if (!view.isVisible) view.alpha = 0f
            view.visibility = View.VISIBLE
            view.animate().alpha(1f).setStartDelay(0L).setDuration(FADE_IN_MS).setInterpolator(DecelerateInterpolator())
                .withEndAction { view.setTag(R.id.motion_visible, null) }.start()
        } else {
            view.animate().alpha(0f).setStartDelay(0L).setDuration(FADE_OUT_MS).setInterpolator(DecelerateInterpolator()).withEndAction {
                view.setTag(R.id.motion_visible, null)
                view.visibility = hidden
                view.alpha = 1f
            }.start()
        }
    }

    /** Fades [view] to [alpha], or sets it at once where nobody would see it move. */
    fun fadeTo(view: View, alpha: Float) {
        // Asked for on every refresh: already there, or on its way.
        if (view.getTag(R.id.motion_alpha) == alpha) return
        view.setTag(R.id.motion_alpha, alpha)
        if (!enabled || !view.isShown || !view.isLaidOut) {
            view.animate().cancel()
            view.alpha = alpha
        } else {
            // Only its alpha: a key springing back keeps doing so.
            val arriving = alpha > view.alpha
            view.animate().alpha(alpha)
                .setStartDelay(if (arriving) ARRIVE_DELAY_MS else 0L)
                .setDuration(if (arriving) CHANGE_MS - ARRIVE_DELAY_MS else LEAVE_MS)
                .setInterpolator(DecelerateInterpolator()).start()
        }
    }

    /**
     * Swaps a label's text with a short fade through. As with [setVisible],
     * set a label's text either always here or always directly.
     */
    fun changeText(view: TextView, text: CharSequence) {
        val heading = view.getTag(R.id.motion_text) as? CharSequence ?: view.text
        if (heading.toString() == text.toString()) return
        if (!enabled || !view.isShown || view.text.isEmpty()) {
            view.animate().cancel()
            view.setTag(R.id.motion_text, null)
            view.alpha = 1f
            view.text = text
            return
        }
        // Still fading the old text out: only what comes next changes.
        val fadingOut = view.getTag(R.id.motion_text) != null
        view.setTag(R.id.motion_text, text)
        if (fadingOut) return
        view.animate().alpha(0f).setStartDelay(0L).setDuration(90L).setInterpolator(DecelerateInterpolator()).withEndAction {
            view.text = view.getTag(R.id.motion_text) as? CharSequence ?: text
            view.setTag(R.id.motion_text, null)
            view.animate().alpha(1f).setStartDelay(0L).setDuration(150L).start()
        }.start()
    }

    /** Sets a label's text at once, where [changeText] sets it too: for a count that ticks. */
    fun tickText(view: TextView, text: CharSequence) {
        if (view.getTag(R.id.motion_text) != null) {
            view.setTag(R.id.motion_text, text)
        } else {
            view.text = text
        }
    }

    /** Ends a press on [view] under way, as if the finger had left it. */
    fun cancelPress(view: View) {
        val now = SystemClock.uptimeMillis()
        val cancel = MotionEvent.obtain(now, now, MotionEvent.ACTION_CANCEL, 0f, 0f, 0)
        view.dispatchTouchEvent(cancel)
        cancel.recycle()
    }

    /** Cross-fades a background drawable to another. */
    fun changeBackground(view: View, @DrawableRes background: Int) {
        val previous = view.getTag(R.id.motion_background)
        if (previous == background) return
        view.setTag(R.id.motion_background, background)
        val next = AppCompatResources.getDrawable(view.context, background)
        // Cross-fading from a cross-fade would keep every earlier drawable.
        val shown = view.background.let { if (it is TransitionDrawable) it.getDrawable(1) else it }
        if (!enabled || previous == null || !view.isShown || shown == null || next == null) {
            view.background = next
            return
        }
        view.background = TransitionDrawable(arrayOf(shown, next)).apply {
            isCrossFadeEnabled = true
            startTransition(CHANGE_MS.toInt())
        }
    }

    /** Blends an icon's colour to [color]. */
    fun changeTint(view: ImageView, @ColorInt color: Int) = changeColor(view, color, view::setColorFilter)

    private fun changeColor(view: View, @ColorInt color: Int, apply: (Int) -> Unit) {
        val previous = view.getTag(R.id.motion_color) as? Int
        if (previous == color) return
        val running = view.getTag(R.id.motion_color_animator) as? ValueAnimator
        val from = running?.takeIf { it.isRunning }?.animatedValue as? Int ?: previous
        running?.cancel()
        view.setTag(R.id.motion_color, color)
        if (!enabled || from == null || !view.isShown) {
            apply(color)
            return
        }
        val animator = ValueAnimator.ofArgb(from, color).apply {
            duration = CHANGE_MS
            addUpdateListener { apply(it.animatedValue as Int) }
        }
        view.setTag(R.id.motion_color_animator, animator)
        animator.start()
    }

    /** Lights a label up in [flash], settling back to its own colour: a number that just rose. */
    fun flashText(view: TextView, @ColorInt flash: Int) {
        if (!enabled) return
        (view.getTag(R.id.motion_color_animator) as? ValueAnimator)?.end()
        val rest = view.textColors
        val animator = ValueAnimator.ofArgb(flash, rest.defaultColor).apply {
            duration = 1_400L
            addUpdateListener { view.setTextColor(it.animatedValue as Int) }
            addListener(object : AnimatorListenerAdapter() {
                override fun onAnimationEnd(animation: Animator) = view.setTextColor(rest)
            })
        }
        view.setTag(R.id.motion_color_animator, animator)
        animator.start()
    }

    /** A message sliding in by [fromY] pixels as it fades in. */
    fun slideIn(view: View, fromY: Float, durationMs: Long = 220L) {
        if (!enabled) return
        view.alpha = 0f
        view.translationY = fromY
        view.animate().alpha(1f).translationY(0f)
            .setStartDelay(0L).setDuration(durationMs).setInterpolator(DecelerateInterpolator(1.6f)).start()
    }

    /** Parts of a screen that is done, sinking away before the next one opens. */
    fun leave(views: List<View>) {
        if (!enabled) return
        views.forEach { view ->
            view.animate().alpha(0f).translationY(20f * view.resources.displayMetrics.density)
                .setStartDelay(0L).setDuration(160L).setInterpolator(AccelerateInterpolator()).start()
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
