package io.virtroid.client.ui

import android.animation.ValueAnimator
import android.content.Context
import android.graphics.Canvas
import android.graphics.LinearGradient
import android.graphics.Matrix
import android.graphics.Paint
import android.graphics.RectF
import android.graphics.Shader
import android.util.AttributeSet
import android.view.View
import android.view.animation.LinearInterpolator
import androidx.core.content.withStyledAttributes
import androidx.core.view.isVisible
import io.virtroid.client.R

/**
 * The outline of what is loading, in its place: blocks where its icons, text
 * and buttons will be, with a light passing over them. [startLoading] shows it
 * after a moment, so a quick answer shows none; the light moves only while it
 * is on screen, and not at all with the phone's animations off.
 */
class SkeletonView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
) : View(context, attrs) {
    private enum class Shape(val heightDp: Float) {
        RUNTIME_CARD(260f),
        APP_ROWS(4 * 77f),
        WALLET_BALANCE(36f),
        WALLET_DEPOSIT(500f),
        WALLET_HISTORY(3 * 56f),
    }

    private val density = resources.displayMetrics.density
    private var shape = Shape.RUNTIME_CARD
    private val blocks = mutableListOf<RectF>()
    private var card: RectF? = null
    private val surface = Paint().apply { color = context.getColor(R.color.v_surface) }
    private val border = Paint().apply {
        color = context.getColor(R.color.v_border)
        style = Paint.Style.STROKE
        strokeWidth = density
    }
    private val block = Paint()
    private val lightAt = Matrix()
    private var light: LinearGradient? = null
    private var animator: ValueAnimator? = null

    var isLoading = false
        private set

    init {
        context.withStyledAttributes(attrs, R.styleable.SkeletonView) {
            shape = Shape.entries[getInt(R.styleable.SkeletonView_skeletonShape, 0)]
        }
        block.color = context.getColor(R.color.v_surface_light)
        isVisible = isInEditMode
    }

    /** Shows the outline, unless [stopLoading] comes within a moment. */
    fun startLoading() {
        if (isLoading) return
        isLoading = true
        isVisible = true
        alpha = 0f
        animate().alpha(1f).setStartDelay(SHOW_AFTER_MS).setDuration(SHOW_MS).start()
    }

    /** Takes the outline away. Call it inside [VirtroidMotion.animateChanges] for it to fade into what loaded. */
    fun stopLoading() {
        if (!isLoading) return
        isLoading = false
        animate().cancel()
        isVisible = false
    }

    override fun onMeasure(widthMeasureSpec: Int, heightMeasureSpec: Int) {
        setMeasuredDimension(
            getDefaultSize(suggestedMinimumWidth, widthMeasureSpec),
            resolveSize((shape.heightDp * density).toInt(), heightMeasureSpec),
        )
    }

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        blocks.clear()
        card = null
        val width = w / density
        when (shape) {
            Shape.RUNTIME_CARD -> {
                card = box(0f, 0f, width, Shape.RUNTIME_CARD.heightDp)
                blocks += box(24f, 24f, 56f, 56f)
                blocks += box(96f, 32f, 128f, 16f)
                blocks += box(96f, 60f, 72f, 12f)
                val columns = width - 48f
                blocks += box(24f, 112f, columns * 0.36f, 10f)
                blocks += box(24f, 132f, columns * 0.44f, 14f)
                blocks += box(24f + columns * 0.56f, 112f, columns * 0.16f, 10f)
                blocks += box(24f + columns * 0.56f, 132f, columns * 0.14f, 14f)
                blocks += box(24f + columns * 0.81f, 112f, columns * 0.13f, 10f)
                blocks += box(24f + columns * 0.81f, 132f, columns * 0.11f, 14f)
                blocks += box(24f, 180f, columns - 68f, 56f)
                blocks += box(width - 80f, 180f, 56f, 56f)
            }
            Shape.APP_ROWS -> repeat(4) { row ->
                val top = row * 77f + 1f
                blocks += box(22f, top + 15f, 46f, 46f)
                blocks += box(86f, top + 19f, if (row % 2 == 0) 112f else 144f, 16f)
                blocks += box(86f, top + 46f, 132f, 11f)
                blocks += box(width - 51f, top + 26f, 24f, 24f)
            }
            Shape.WALLET_BALANCE -> blocks += box(0f, 4f, 148f, 28f)
            Shape.WALLET_DEPOSIT -> {
                card = box(0f, 0f, width, Shape.WALLET_DEPOSIT.heightDp)
                blocks += box(20f, 22f, 112f, 14f)
                blocks += box(20f, 44f, 148f, 10f)
                blocks += box((width - 200f) / 2f, 72f, 200f, 200f)
                blocks += box(20f, 292f, width - 40f, 14f)
                blocks += box((width - 142f) / 2f, 322f, 142f, 56f)
                blocks += box(20f, 394f, width - 40f, 62f)
                blocks += box(20f, 470f, width * 0.6f, 10f)
            }
            Shape.WALLET_HISTORY -> repeat(3) { row ->
                val top = row * 56f
                blocks += box(0f, top + 10f, 84f, 14f)
                blocks += box(0f, top + 32f, 120f, 10f)
                blocks += box(width - 56f, top + 12f, 56f, 14f)
            }
        }
        // One light across the whole width, so it passes over all the blocks together.
        light = LinearGradient(
            0f, 0f, w * LIGHT_WIDTH, w * LIGHT_WIDTH * 0.35f,
            intArrayOf(
                context.getColor(R.color.v_surface_light),
                context.getColor(R.color.v_surface_lighter),
                context.getColor(R.color.v_surface_light),
            ),
            null,
            Shader.TileMode.CLAMP,
        )
    }

    private fun box(left: Float, top: Float, width: Float, height: Float) =
        RectF(left * density, top * density, (left + width) * density, (top + height) * density)

    override fun onDraw(canvas: Canvas) {
        card?.let {
            canvas.drawRect(it, surface)
            val inset = density / 2f
            canvas.drawRect(it.left + inset, it.top + inset, it.right - inset, it.bottom - inset, border)
        }
        val running = animator
        block.shader = if (running == null) null else light?.apply {
            lightAt.setTranslate((-LIGHT_WIDTH + (1f + LIGHT_WIDTH) * running.animatedFraction) * width, 0f)
            setLocalMatrix(lightAt)
        }
        blocks.forEach { canvas.drawRect(it, block) }
    }

    override fun onVisibilityAggregated(isVisible: Boolean) {
        super.onVisibilityAggregated(isVisible)
        animator?.cancel()
        animator = null
        if (isVisible && VirtroidMotion.enabled) {
            animator = ValueAnimator.ofFloat(0f, 1f).apply {
                duration = SWEEP_MS
                repeatCount = ValueAnimator.INFINITE
                interpolator = LinearInterpolator()
                addUpdateListener { invalidate() }
                start()
            }
        }
    }

    private companion object {
        const val SHOW_AFTER_MS = 150L
        const val SHOW_MS = 180L
        const val SWEEP_MS = 1_300L
        const val LIGHT_WIDTH = 0.6f
    }
}
