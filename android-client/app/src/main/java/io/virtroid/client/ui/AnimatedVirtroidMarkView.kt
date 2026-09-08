package io.virtroid.client.ui

import android.animation.ValueAnimator
import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.DashPathEffect
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PathMeasure
import android.graphics.Shader
import android.util.AttributeSet
import android.view.View
import android.view.animation.LinearInterpolator
import io.virtroid.client.R
import kotlin.math.PI
import kotlin.math.min
import kotlin.math.sin

/** Native rendering of the segmented Virtroid mark used by the operator console. */
class AnimatedVirtroidMarkView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyleAttr: Int = 0,
) : View(context, attrs, defStyleAttr) {
    private val fillPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply { style = Paint.Style.FILL }
    private val strokePaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeCap = Paint.Cap.ROUND
        strokeJoin = Paint.Join.ROUND
    }

    private val primary = context.getColor(R.color.v_accent)
    private val primaryHighlight = context.getColor(R.color.v_accent_highlight)
    private val primaryShadow = context.getColor(R.color.v_accent_shadow)
    private val white = context.getColor(R.color.v_text_primary)
    private val muted = context.getColor(R.color.v_text_muted)
    private val dark = context.getColor(R.color.virtroid_on_primary)

    private val top = Path().apply {
        moveTo(156f, 151f)
        lineTo(156f, 145f)
        quadTo(156f, 132f, 169f, 132f)
        lineTo(257f, 132f)
        quadTo(270f, 132f, 271f, 146f)
        lineTo(271f, 151f)
        close()
    }
    private val upper = polygon(156f, 158f, 270f, 158f, 270f, 192f, 156f, 172f)
    private val middle = polygon(156f, 178f, 270f, 198f, 270f, 207f, 156f, 249f)
    private val lower = polygon(160f, 255f, 273f, 211f, 275f, 258f, 160f, 265f)
    private val lowerEdge = polygon(161f, 265f, 275f, 258f, 274f, 254f, 163f, 262f)
    private val bottom = Path().apply {
        moveTo(161f, 274f)
        lineTo(275f, 268f)
        lineTo(275f, 287f)
        quadTo(275f, 300f, 262f, 301f)
        lineTo(175f, 301f)
        quadTo(162f, 301f, 161f, 288f)
        close()
    }
    private val speaker = Path().apply {
        moveTo(208f, 282f)
        lineTo(233f, 280f)
        quadTo(237f, 280f, 237f, 283f)
        quadTo(237f, 286f, 233f, 287f)
        lineTo(208f, 289f)
        quadTo(204f, 289f, 204f, 286f)
        quadTo(204f, 283f, 208f, 282f)
        close()
    }
    private val screenClip = Path().apply {
        addPath(upper)
        addPath(middle)
        addPath(lower)
    }
    private val energy = Path().apply {
        moveTo(148f, 257f)
        lineTo(284f, 207f)
    }
    private val energyMeasure = PathMeasure(energy, false)
    private val glint = polygon(145f, 110f, 170f, 110f, 240f, 325f, 215f, 325f)

    private val screenShader = LinearGradient(
        160f,
        158f,
        255f,
        260f,
        primary,
        primaryShadow,
        Shader.TileMode.CLAMP,
    )
    private val facetShader = LinearGradient(
        186f,
        227f,
        247f,
        275f,
        primaryHighlight,
        primaryShadow,
        Shader.TileMode.CLAMP,
    )
    private val whiteShader = LinearGradient(
        156f,
        132f,
        275f,
        301f,
        intArrayOf(white, white, muted),
        floatArrayOf(0f, 0.4f, 1f),
        Shader.TileMode.CLAMP,
    )
    private val glintShader = LinearGradient(
        145f,
        110f,
        240f,
        325f,
        intArrayOf(
            Color.TRANSPARENT,
            Color.argb(179, Color.red(white), Color.green(white), Color.blue(white)),
            Color.TRANSPARENT,
        ),
        floatArrayOf(0f, 0.5f, 1f),
        Shader.TileMode.CLAMP,
    )

    private var phase = 0f
    private var animator: ValueAnimator? = null

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        startMotion()
    }

    override fun onDetachedFromWindow() {
        animator?.cancel()
        animator = null
        super.onDetachedFromWindow()
    }

    override fun onWindowVisibilityChanged(visibility: Int) {
        super.onWindowVisibilityChanged(visibility)
        if (visibility == VISIBLE) startMotion() else animator?.pause()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        val contentWidth = width - paddingLeft - paddingRight
        val contentHeight = height - paddingTop - paddingBottom
        if (contentWidth <= 0 || contentHeight <= 0) return

        val scale = min(contentWidth / ART_WIDTH, contentHeight / ART_HEIGHT)
        val left = paddingLeft + (contentWidth - ART_WIDTH * scale) / 2f - ART_LEFT * scale
        val topOffset = paddingTop + (contentHeight - ART_HEIGHT * scale) / 2f - ART_TOP * scale

        canvas.save()
        canvas.translate(left, topOffset)
        canvas.scale(scale, scale)
        canvas.translate(0f, pxToArt(dpToPx((-0.35f * sin(phase * 2f * PI)).toFloat()), scale))

        drawEnergy(canvas, scale)
        drawTop(canvas, scale)
        drawUpper(canvas, scale)
        drawMiddle(canvas, scale)
        drawLower(canvas, scale)
        drawBottom(canvas, scale)
        drawGlint(canvas, scale)
        canvas.restore()
    }

    private fun drawTop(canvas: Canvas, scale: Float) {
        withPiece(canvas, 0f, -1.8f, 0f, amount(0.12f), scale, 213.5f, 141.5f) {
            drawFill(canvas, top, whiteShader)
            strokePaint.color = white
            strokePaint.alpha = 204
            strokePaint.strokeWidth = 2f
            canvas.drawLine(158f, 149f, 269f, 149f, strokePaint)
        }
    }

    private fun drawUpper(canvas: Canvas, scale: Float) {
        withPiece(canvas, -1.2f, -0.75f, -3f, amount(0.14f), scale, 213f, 175f) {
            drawFill(canvas, upper, screenShader)
            strokePaint.color = primaryHighlight
            strokePaint.alpha = 153
            strokePaint.strokeWidth = 2f
            val highlight = Path().apply {
                moveTo(158f, 159f)
                lineTo(269f, 159f)
                lineTo(269f, 191f)
            }
            canvas.drawPath(highlight, strokePaint)
        }
    }

    private fun drawMiddle(canvas: Canvas, scale: Float) {
        withPiece(canvas, 1.35f, 0.15f, 2f, amount(0.16f), scale, 213f, 213.5f) {
            drawFill(canvas, middle, screenShader)
            strokePaint.color = primaryHighlight
            strokePaint.alpha = 90
            strokePaint.strokeWidth = 1f
            val highlight = Path().apply {
                moveTo(269f, 199f)
                lineTo(269f, 206f)
                lineTo(159f, 246f)
            }
            canvas.drawPath(highlight, strokePaint)
        }
    }

    private fun drawLower(canvas: Canvas, scale: Float) {
        withPiece(canvas, 1.2f, 1.05f, 3f, amount(0.16f), scale, 217.5f, 238f) {
            drawFill(canvas, lower, facetShader)
            strokePaint.color = primaryHighlight
            strokePaint.alpha = 100
            strokePaint.strokeWidth = 2f
            val highlight = Path().apply {
                moveTo(163f, 258f)
                lineTo(271f, 216f)
                lineTo(272f, 255f)
                lineTo(166f, 262f)
            }
            canvas.drawPath(highlight, strokePaint)
            drawFill(canvas, lowerEdge, null, primaryShadow)
        }
    }

    private fun drawBottom(canvas: Canvas, scale: Float) {
        withPiece(canvas, 0f, 2f, 0f, amount(0.18f), scale, 218f, 284.5f) {
            drawFill(canvas, bottom, whiteShader)
            drawFill(canvas, speaker, null, dark)
        }
    }

    private fun drawEnergy(canvas: Canvas, scale: Float) {
        if (phase !in 0.23f..0.60f) return
        val local = ((phase - 0.23f) / 0.37f).coerceIn(0f, 1f)
        val segment = Path()
        val end = energyMeasure.length * local
        val start = (end - energyMeasure.length * 0.24f).coerceAtLeast(0f)
        energyMeasure.getSegment(start, end, segment, true)
        val fade = min((local / 0.18f).coerceAtMost(1f), ((1f - local) / 0.16f).coerceAtMost(1f))
        strokePaint.color = primaryHighlight
        strokePaint.alpha = (220f * fade.coerceAtLeast(0f)).toInt()
        strokePaint.strokeWidth = pxToArt(dpToPx(1.1f), scale)
        strokePaint.pathEffect = DashPathEffect(floatArrayOf(12f, 8f), -energyMeasure.length * local)
        canvas.drawPath(segment, strokePaint)
        strokePaint.pathEffect = null
    }

    private fun drawGlint(canvas: Canvas, scale: Float) {
        if (phase !in 0.70f..0.89f) return
        val local = ((phase - 0.70f) / 0.19f).coerceIn(0f, 1f)
        val alpha = if (local < 0.35f) local / 0.35f else (1f - local) / 0.65f
        canvas.save()
        canvas.clipPath(screenClip)
        canvas.translate(pxToArt(dpToPx(-16f + 33f * local), scale), 0f)
        fillPaint.shader = glintShader
        fillPaint.alpha = (150f * alpha.coerceIn(0f, 1f)).toInt()
        canvas.drawPath(glint, fillPaint)
        fillPaint.shader = null
        fillPaint.alpha = 255
        canvas.restore()
    }

    private inline fun withPiece(
        canvas: Canvas,
        dxDp: Float,
        dyDp: Float,
        rotation: Float,
        amount: Float,
        scale: Float,
        pivotX: Float,
        pivotY: Float,
        draw: () -> Unit,
    ) {
        canvas.save()
        canvas.translate(
            pxToArt(dpToPx(dxDp * amount), scale),
            pxToArt(dpToPx(dyDp * amount), scale),
        )
        canvas.rotate(rotation * amount, pivotX, pivotY)
        draw()
        canvas.restore()
    }

    private fun drawFill(canvas: Canvas, path: Path, shader: Shader?, color: Int = Color.TRANSPARENT) {
        fillPaint.shader = shader
        if (shader == null) fillPaint.color = color
        fillPaint.alpha = 255
        canvas.drawPath(path, fillPaint)
        fillPaint.shader = null
    }

    private fun amount(start: Float): Float = when {
        phase <= start || phase >= 0.72f -> 0f
        phase < 0.35f -> smooth((phase - start) / (0.35f - start))
        phase <= 0.44f -> 1f
        else -> 1f - smooth((phase - 0.44f) / 0.28f)
    }

    private fun smooth(value: Float): Float {
        val clamped = value.coerceIn(0f, 1f)
        return clamped * clamped * (3f - 2f * clamped)
    }

    private fun startMotion() {
        if (!isAttachedToWindow || windowVisibility != VISIBLE) return
        if (!ValueAnimator.areAnimatorsEnabled()) {
            animator?.cancel()
            animator = null
            phase = 0f
            invalidate()
            return
        }
        if (animator?.isStarted == true) {
            animator?.resume()
            return
        }
        animator = ValueAnimator.ofFloat(0f, 1f).apply {
            duration = 5_600L
            interpolator = LinearInterpolator()
            repeatCount = ValueAnimator.INFINITE
            addUpdateListener {
                phase = it.animatedValue as Float
                invalidate()
            }
            start()
        }
    }

    private fun dpToPx(value: Float): Float = value * resources.displayMetrics.density

    private fun pxToArt(value: Float, scale: Float): Float = value / scale

    private fun polygon(vararg values: Float): Path = Path().apply {
        moveTo(values[0], values[1])
        var index = 2
        while (index < values.size) {
            lineTo(values[index], values[index + 1])
            index += 2
        }
        close()
    }

    private companion object {
        const val ART_LEFT = 140f
        const val ART_TOP = 122f
        const val ART_WIDTH = 152f
        const val ART_HEIGHT = 190f
    }
}
