package io.virtroid.client.ui

import android.annotation.SuppressLint
import android.content.Context
import android.content.res.ColorStateList
import android.graphics.Canvas
import android.graphics.Paint
import android.os.SystemClock
import android.util.AttributeSet
import android.view.HapticFeedbackConstants
import android.view.MotionEvent
import android.view.ViewConfiguration
import androidx.core.view.ViewCompat
import androidx.core.view.accessibility.AccessibilityNodeInfoCompat.AccessibilityActionCompat
import com.google.android.material.button.MaterialButton
import io.virtroid.client.R

/**
 * A button that can ask to be held instead of tapped ([requireHold]), for what
 * can't be undone: it fills while held, drains when let go, and clicks when
 * full. It keeps its own time, so the phone's animation settings don't change
 * how long that is.
 *
 * Only touches have to hold. TalkBack, Switch Access, voice and a keyboard
 * click it as any button: they have no hold to give, and each already takes a
 * deliberate step.
 */
class HoldButton @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyleAttr: Int = com.google.android.material.R.attr.materialButtonStyle,
) : MaterialButton(context, attrs, defStyleAttr) {
    private val progress = HoldProgress(HOLD_MS, DRAIN_MS)
    private val fill = Paint().apply { color = context.getColor(R.color.v_danger) }
    private val onFill = context.getColor(R.color.v_bg)
    private var mustHold = false
    private var lastFrameMs = 0L
    private var ticks = 0

    // Posted from drawing; by then Back or the lock screen may have come first.
    private val confirm = Runnable {
        if (isAttachedToWindow && isShown && hasWindowFocus()) {
            VirtroidMotion.confirmHaptic(this)
            performClick()
        } else {
            empty()
        }
    }

    /** Makes the button red, to be held; [action] says what a click does, to those who don't hold it. */
    fun requireHold(action: CharSequence) {
        mustHold = true
        ViewCompat.replaceAccessibilityAction(this, AccessibilityActionCompat.ACTION_CLICK, action, null)
        // Not while another app's window is over it.
        filterTouchesWhenObscured = true
        backgroundTintList = ColorStateList.valueOf(context.getColor(R.color.v_danger_card))
        strokeColor = ColorStateList.valueOf(context.getColor(R.color.v_danger_stroke))
        strokeWidth = resources.displayMetrics.density.toInt().coerceAtLeast(1)
        rippleColor = ColorStateList.valueOf(android.graphics.Color.TRANSPARENT)
        setTextColor(context.getColor(R.color.v_danger))
    }

    @SuppressLint("ClickableViewAccessibility") // Held, it clicks in onDraw; other clicks are performClick's as ever.
    override fun onTouchEvent(event: MotionEvent): Boolean {
        if (!mustHold) return super.onTouchEvent(event)
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> if (isEnabled) {
                parent?.requestDisallowInterceptTouchEvent(true)
                isPressed = true
                performHapticFeedback(HapticFeedbackConstants.VIRTUAL_KEY)
                ticks = (progress.level * TICKS).toInt()
                progress.hold()
                lastFrameMs = 0L
                postInvalidateOnAnimation()
            }
            MotionEvent.ACTION_MOVE -> if (progress.isHeld && !isWithin(event.x, event.y)) letGo()
            MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> letGo()
        }
        return true
    }

    private fun isWithin(x: Float, y: Float): Boolean {
        val slop = ViewConfiguration.get(context).scaledTouchSlop
        return x >= -slop && y >= -slop && x < width + slop && y < height + slop
    }

    private fun empty() {
        progress.reset()
        postInvalidateOnAnimation()
    }

    private fun letGo() {
        isPressed = false
        progress.release()
        if (!VirtroidMotion.enabled && progress.isMoving) progress.reset()
        postInvalidateOnAnimation()
    }

    override fun onDraw(canvas: Canvas) {
        if (!mustHold) {
            super.onDraw(canvas)
            return
        }
        val now = SystemClock.uptimeMillis()
        val full = progress.advance(if (lastFrameMs == 0L) 0L else now - lastFrameMs)
        lastFrameMs = now

        val edge = width * progress.level
        canvas.drawRect(0f, insetTop.toFloat(), edge, (height - insetBottom).toFloat(), fill)
        super.onDraw(canvas)
        drawLabelOnFill(canvas, edge)

        if (full) {
            isPressed = false
            post(confirm)
        } else if (progress.isMoving) {
            val passed = (progress.level * TICKS).toInt()
            if (progress.isHeld && passed > ticks) performHapticFeedback(HapticFeedbackConstants.CLOCK_TICK)
            ticks = passed
            postInvalidateOnAnimation()
        } else {
            lastFrameMs = 0L
        }
    }

    /** The part of the label the fill has reached, in a colour that shows on it. */
    private fun drawLabelOnFill(canvas: Canvas, edge: Float) {
        val label = layout ?: return
        if (edge <= 0f) return
        canvas.save()
        canvas.clipRect(0f, 0f, edge, height.toFloat())
        val room = height - extendedPaddingTop - extendedPaddingBottom
        canvas.translate(compoundPaddingLeft.toFloat(), extendedPaddingTop + (room - label.height) / 2f)
        label.paint.color = onFill
        label.draw(canvas)
        canvas.restore()
    }

    /** The lock screen, another app or a dialog came over it: that was not holding on. */
    override fun onWindowFocusChanged(hasWindowFocus: Boolean) {
        super.onWindowFocusChanged(hasWindowFocus)
        if (hasWindowFocus || !mustHold) return
        removeCallbacks(confirm)
        if (progress.isHeld) letGo() else empty()
    }

    override fun onDetachedFromWindow() {
        removeCallbacks(confirm)
        progress.reset()
        super.onDetachedFromWindow()
    }

    private companion object {
        const val HOLD_MS = 1_200L
        const val DRAIN_MS = 350L
        const val TICKS = 4
    }
}
