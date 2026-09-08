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
import android.graphics.RadialGradient
import android.graphics.RectF
import android.graphics.Shader
import android.graphics.Typeface
import android.util.AttributeSet
import android.util.TypedValue
import android.view.View
import android.view.animation.LinearInterpolator
import io.virtroid.client.R
import kotlin.math.PI
import kotlin.math.sin

/** Animated client-side topology inspired by the operator console system-pulse illustration. */
class WelcomeNetworkView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
    defStyleAttr: Int = 0,
) : View(context, attrs, defStyleAttr) {
    private val background = context.getColor(R.color.v_bg)
    private val surface = context.getColor(R.color.v_surface)
    private val accent = context.getColor(R.color.v_accent)
    private val accentHighlight = context.getColor(R.color.v_accent_highlight)
    private val accentShadow = context.getColor(R.color.v_accent_shadow)
    private val primaryText = context.getColor(R.color.v_text_primary)
    private val mutedText = context.getColor(R.color.v_text_muted)

    private val fillPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply { style = Paint.Style.FILL }
    private val strokePaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeCap = Paint.Cap.ROUND
        strokeJoin = Paint.Join.ROUND
    }
    private val labelPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = primaryText
        textAlign = Paint.Align.CENTER
        textSize = sp(13f)
        typeface = Typeface.create(Typeface.SANS_SERIF, Typeface.BOLD)
    }
    private val detailPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = mutedText
        textAlign = Paint.Align.CENTER
        textSize = sp(10.5f)
        typeface = Typeface.create(Typeface.SANS_SERIF, Typeface.NORMAL)
    }

    private var phase = 0f
    private var animator: ValueAnimator? = null
    private var spotlightShader: Shader? = null
    private var coreGlowShader: Shader? = null

    override fun onSizeChanged(width: Int, height: Int, oldWidth: Int, oldHeight: Int) {
        super.onSizeChanged(width, height, oldWidth, oldHeight)
        spotlightShader = LinearGradient(
            width * 0.5f,
            0f,
            width * 0.5f,
            height * 0.45f,
            intArrayOf(
                withAlpha(accent, 0),
                withAlpha(accent, 28),
                withAlpha(accent, 0),
            ),
            floatArrayOf(0f, 0.52f, 1f),
            Shader.TileMode.CLAMP,
        )
        coreGlowShader = RadialGradient(
            width * 0.5f,
            height * 0.22f,
            dp(110f),
            intArrayOf(withAlpha(accent, 30), withAlpha(accent, 0)),
            floatArrayOf(0f, 1f),
            Shader.TileMode.CLAMP,
        )
    }

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
        canvas.drawColor(background)
        if (width <= 0 || height <= 0) return

        drawGrid(canvas)
        drawSpotlight(canvas)

        val core = Node(width * 0.5f, height * 0.215f, NodeKind.CORE)
        val phone = Node(width * 0.19f, height * 0.36f, NodeKind.PHONE)
        val runtime = Node(width * 0.81f, height * 0.36f, NodeKind.RUNTIME)
        val vault = Node(width * 0.5f, height * 0.455f, NodeKind.VAULT)

        val networkPath = Path().apply {
            moveTo(phone.x + dp(34f), phone.y)
            cubicTo(
                width * 0.33f,
                phone.y,
                width * 0.39f,
                core.y + dp(39f),
                core.x,
                core.y + dp(39f),
            )
            cubicTo(
                width * 0.61f,
                core.y + dp(39f),
                width * 0.67f,
                runtime.y,
                runtime.x - dp(34f),
                runtime.y,
            )
        }
        val vaultPath = Path().apply {
            moveTo(core.x, core.y + dp(42f))
            cubicTo(core.x, core.y + dp(78f), vault.x, vault.y - dp(64f), vault.x, vault.y - dp(34f))
        }
        drawConnections(canvas, networkPath, vaultPath)

        drawNode(canvas, phone, "This phone", "Control ready", index = 1)
        drawNode(canvas, core, "Virtroid", "Private link", index = 0, emphasized = true)
        drawNode(canvas, runtime, "Runtime", "Remote Android", index = 2)
        drawNode(canvas, vault, "Encrypted vault", "Protected", index = 3)
    }

    private fun drawGrid(canvas: Canvas) {
        val spacing = dp(42f)
        strokePaint.color = accent
        strokePaint.alpha = 10
        strokePaint.strokeWidth = dp(0.55f)
        strokePaint.pathEffect = null
        var x = 0f
        while (x <= width) {
            canvas.drawLine(x, 0f, x, height * 0.59f, strokePaint)
            x += spacing
        }
        var y = 0f
        while (y <= height * 0.59f) {
            canvas.drawLine(0f, y, width.toFloat(), y, strokePaint)
            y += spacing
        }
    }

    private fun drawSpotlight(canvas: Canvas) {
        val cone = Path().apply {
            moveTo(width * 0.43f, 0f)
            lineTo(width * 0.57f, 0f)
            lineTo(width * 0.64f, height * 0.34f)
            lineTo(width * 0.36f, height * 0.34f)
            close()
        }
        fillPaint.shader = spotlightShader
        canvas.drawPath(cone, fillPaint)
        fillPaint.shader = null
        fillPaint.shader = coreGlowShader
        canvas.drawCircle(width * 0.5f, height * 0.22f, dp(110f), fillPaint)
        fillPaint.shader = null
    }

    private fun drawConnections(canvas: Canvas, networkPath: Path, vaultPath: Path) {
        val dashOffset = -phase * dp(36f)
        strokePaint.color = accentShadow
        strokePaint.alpha = 116
        strokePaint.strokeWidth = dp(1f)
        strokePaint.pathEffect = DashPathEffect(floatArrayOf(dp(3f), dp(7f)), dashOffset)
        canvas.drawPath(networkPath, strokePaint)
        canvas.drawPath(vaultPath, strokePaint)
        strokePaint.pathEffect = null

        drawPacket(canvas, networkPath, phase)
        drawPacket(canvas, vaultPath, (phase + 0.48f) % 1f)
    }

    private fun drawPacket(canvas: Canvas, path: Path, progress: Float) {
        val measure = PathMeasure(path, false)
        val position = FloatArray(2)
        measure.getPosTan(measure.length * progress.coerceIn(0f, 1f), position, null)
        fillPaint.color = withAlpha(accent, 38)
        canvas.drawCircle(position[0], position[1], dp(8f), fillPaint)
        fillPaint.color = accentHighlight
        canvas.drawCircle(position[0], position[1], dp(2.6f), fillPaint)
    }

    private fun drawNode(
        canvas: Canvas,
        node: Node,
        label: String,
        detail: String,
        index: Int,
        emphasized: Boolean = false,
    ) {
        val offset = sin((phase * 2f * PI + index * 0.8f)).toFloat() * dp(if (emphasized) 1.7f else 1.1f)
        val cx = node.x
        val cy = node.y + offset
        val half = dp(if (emphasized) 39f else 32f)
        val rect = RectF(cx - half, cy - half, cx + half, cy + half)

        if (emphasized) {
            val pulse = (0.5f + 0.5f * sin(phase * 2f * PI)).toFloat()
            strokePaint.color = accent
            strokePaint.alpha = (22 + pulse * 28).toInt()
            strokePaint.strokeWidth = dp(10f)
            canvas.drawRect(rect, strokePaint)
        }

        fillPaint.color = withAlpha(surface, 236)
        canvas.drawRect(rect, fillPaint)
        strokePaint.color = if (emphasized) accent else accentShadow
        strokePaint.alpha = if (emphasized) 150 else 100
        strokePaint.strokeWidth = dp(1f)
        canvas.drawRect(rect, strokePaint)

        drawNodeIcon(canvas, node.kind, cx, cy, emphasized)
        canvas.drawText(label, cx, cy + half + dp(23f), labelPaint)
        canvas.drawText(detail, cx, cy + half + dp(41f), detailPaint)
    }

    private fun drawNodeIcon(canvas: Canvas, kind: NodeKind, cx: Float, cy: Float, emphasized: Boolean) {
        strokePaint.color = if (emphasized) accentHighlight else accent
        strokePaint.alpha = if (emphasized) 255 else 205
        strokePaint.strokeWidth = dp(2f)
        strokePaint.pathEffect = null
        when (kind) {
            NodeKind.CORE -> {
                repeat(2) { index ->
                    val top = cy - dp(12f) + index * dp(14f)
                    canvas.drawRect(cx - dp(15f), top, cx + dp(15f), top + dp(9f), strokePaint)
                    fillPaint.color = strokePaint.color
                    fillPaint.alpha = strokePaint.alpha
                    canvas.drawCircle(cx - dp(10f), top + dp(4.5f), dp(1.4f), fillPaint)
                }
            }
            NodeKind.PHONE -> {
                canvas.drawRect(cx - dp(10f), cy - dp(16f), cx + dp(10f), cy + dp(16f), strokePaint)
                canvas.drawLine(cx - dp(4f), cy + dp(11f), cx + dp(4f), cy + dp(11f), strokePaint)
            }
            NodeKind.RUNTIME -> {
                val size = dp(10f)
                canvas.drawRect(cx - size, cy - size, cx, cy, strokePaint)
                canvas.drawRect(cx, cy - size, cx + size, cy, strokePaint)
                canvas.drawRect(cx - size / 2f, cy, cx + size / 2f, cy + size, strokePaint)
            }
            NodeKind.VAULT -> {
                val oval = RectF(cx - dp(13f), cy - dp(12f), cx + dp(13f), cy - dp(4f))
                canvas.drawOval(oval, strokePaint)
                canvas.drawLine(cx - dp(13f), cy - dp(8f), cx - dp(13f), cy + dp(11f), strokePaint)
                canvas.drawLine(cx + dp(13f), cy - dp(8f), cx + dp(13f), cy + dp(11f), strokePaint)
                canvas.drawArc(
                    RectF(cx - dp(13f), cy + dp(7f), cx + dp(13f), cy + dp(15f)),
                    0f,
                    180f,
                    false,
                    strokePaint,
                )
                canvas.drawArc(
                    RectF(cx - dp(13f), cy - dp(2f), cx + dp(13f), cy + dp(6f)),
                    0f,
                    180f,
                    false,
                    strokePaint,
                )
            }
        }
    }

    private fun startMotion() {
        if (!isAttachedToWindow || windowVisibility != VISIBLE) return
        if (!ValueAnimator.areAnimatorsEnabled()) {
            animator?.cancel()
            animator = null
            phase = 0.34f
            invalidate()
            return
        }
        if (animator?.isStarted == true) {
            animator?.resume()
            return
        }
        animator = ValueAnimator.ofFloat(0f, 1f).apply {
            duration = 6_400L
            interpolator = LinearInterpolator()
            repeatCount = ValueAnimator.INFINITE
            addUpdateListener {
                phase = it.animatedValue as Float
                invalidate()
            }
            start()
        }
    }

    private fun dp(value: Float): Float = value * resources.displayMetrics.density

    private fun sp(value: Float): Float = TypedValue.applyDimension(
        TypedValue.COMPLEX_UNIT_SP,
        value,
        resources.displayMetrics,
    )

    private fun withAlpha(color: Int, alpha: Int): Int = Color.argb(
        alpha.coerceIn(0, 255),
        Color.red(color),
        Color.green(color),
        Color.blue(color),
    )

    private data class Node(val x: Float, val y: Float, val kind: NodeKind)

    private enum class NodeKind { PHONE, CORE, RUNTIME, VAULT }
}
