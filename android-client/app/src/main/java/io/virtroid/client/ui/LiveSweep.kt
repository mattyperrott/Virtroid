package io.virtroid.client.ui

import android.animation.Animator
import android.animation.AnimatorListenerAdapter
import android.animation.ValueAnimator
import android.content.Context
import android.graphics.Canvas
import android.graphics.ColorFilter
import android.graphics.LinearGradient
import android.graphics.Matrix
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.Shader
import android.graphics.drawable.Drawable
import android.view.View
import android.view.animation.LinearInterpolator
import android.view.animation.PathInterpolator
import androidx.core.graphics.ColorUtils
import androidx.core.graphics.withClip
import io.virtroid.client.R

/**
 * A runtime coming online: one band of light crosses its card and the card's
 * edge lights up, then both are gone. Drawn over the card, so nothing in it
 * changes or moves.
 */
class LiveSweep private constructor(context: Context) : Drawable() {
    private val accent = context.getColor(R.color.v_accent)
    private val light = context.getColor(R.color.v_accent_highlight)
    private val band = Paint()
    private val edge = Paint().apply {
        color = accent
        style = Paint.Style.STROKE
        strokeWidth = 2f * context.resources.displayMetrics.density
    }
    private val crossing = PathInterpolator(0.4f, 0f, 0.2f, 1f)
    private val bandAt = Matrix()
    private var bandFor = 0
    private var progress = 0f

    override fun draw(canvas: Canvas) {
        val area = bounds
        if (area.isEmpty) return
        val width = area.width() * BAND_WIDTH
        // The card may still be changing size as this starts.
        if (bandFor != area.width()) {
            bandFor = area.width()
            band.shader = LinearGradient(
                0f, 0f, width, width * 0.35f,
                intArrayOf(0, ColorUtils.setAlphaComponent(light, BAND_ALPHA), 0),
                null,
                Shader.TileMode.CLAMP,
            )
        }
        val crossed = crossing.getInterpolation((progress / CROSSED_AT).coerceIn(0f, 1f))
        if (crossed < 1f) {
            // From wholly left of the card to wholly right of it, its slant included.
            val slant = area.height() * 0.35f
            bandAt.setTranslate(area.left - width - slant + (area.width() + width * 2f + slant) * crossed, 0f)
            band.shader.setLocalMatrix(bandAt)
            canvas.withClip(area) { drawRect(area, band) }
        }
        // Lit as the band reaches the middle, then fading.
        val lit = if (progress < LIT_AT) progress / LIT_AT else 1f - (progress - LIT_AT) / (1f - LIT_AT)
        edge.alpha = (EDGE_ALPHA * lit.coerceIn(0f, 1f)).toInt()
        val inset = edge.strokeWidth / 2f
        canvas.drawRect(area.left + inset, area.top + inset, area.right - inset, area.bottom - inset, edge)
    }

    override fun setAlpha(alpha: Int) = Unit

    override fun setColorFilter(colorFilter: ColorFilter?) = Unit

    @Deprecated("Deprecated in Java")
    override fun getOpacity(): Int = PixelFormat.TRANSLUCENT

    companion object {
        private const val LENGTH_MS = 1_300L
        // After the card's own change of buttons, which it would hide.
        private const val START_DELAY_MS = 140L
        private const val BAND_WIDTH = 0.45f
        private const val BAND_ALPHA = 96
        private const val EDGE_ALPHA = 230
        private const val CROSSED_AT = 0.7f
        private const val LIT_AT = 0.35f

        /** Plays over [card], if it is on screen and the phone's animations are on. */
        fun play(card: View) {
            if (!VirtroidMotion.enabled || !card.isShown || !card.isAttachedToWindow) return
            val sweep = LiveSweep(card.context)
            val animator = ValueAnimator.ofFloat(0f, 1f).apply {
                startDelay = START_DELAY_MS
                duration = LENGTH_MS
                interpolator = LinearInterpolator()
            }
            val leave = object : View.OnAttachStateChangeListener {
                override fun onViewAttachedToWindow(view: View) = Unit
                override fun onViewDetachedFromWindow(view: View) = animator.cancel()
            }
            animator.addUpdateListener {
                sweep.progress = it.animatedValue as Float
                sweep.setBounds(0, 0, card.width, card.height)
                sweep.invalidateSelf()
                card.invalidate()
            }
            animator.addListener(object : AnimatorListenerAdapter() {
                override fun onAnimationStart(animation: Animator) = card.overlay.add(sweep)
                override fun onAnimationEnd(animation: Animator) {
                    card.overlay.remove(sweep)
                    card.removeOnAttachStateChangeListener(leave)
                }
            })
            card.addOnAttachStateChangeListener(leave)
            animator.start()
        }
    }
}
