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
import androidx.core.animation.doOnEnd
import io.virtroid.client.R
import kotlin.math.PI
import kotlin.math.roundToInt
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

    private var phase = REST_PHASE
    private var animator: ValueAnimator? = null
    private var spotlightShader: Shader? = null
    private var coreGlowShader: Shader? = null

    // Everything that only depends on the view's size, built in onSizeChanged
    // so that drawing a frame allocates nothing.
    private var core = Node(0f, 0f, NodeKind.CORE)
    private var phone = Node(0f, 0f, NodeKind.PHONE)
    private var runtime = Node(0f, 0f, NodeKind.RUNTIME)
    private var vault = Node(0f, 0f, NodeKind.VAULT)
    private val networkPath = Path()
    private val vaultPath = Path()
    private val cone = Path()
    private val networkMeasure = PathMeasure()
    private val vaultMeasure = PathMeasure()
    private var connectionDashes: Array<DashPathEffect> = emptyArray()
    private val nodeRect = RectF()
    private val iconRect = RectF()
    private val packetPosition = FloatArray(2)

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

        core = Node(width * 0.5f, height * 0.215f, NodeKind.CORE)
        phone = Node(width * 0.19f, height * 0.36f, NodeKind.PHONE)
        runtime = Node(width * 0.81f, height * 0.36f, NodeKind.RUNTIME)
        vault = Node(width * 0.5f, height * 0.455f, NodeKind.VAULT)

        networkPath.rewind()
        networkPath.moveTo(phone.x + dp(34f), phone.y)
        networkPath.cubicTo(
            width * 0.33f,
            phone.y,
            width * 0.39f,
            core.y + dp(39f),
            core.x,
            core.y + dp(39f),
        )
        networkPath.cubicTo(
            width * 0.61f,
            core.y + dp(39f),
            width * 0.67f,
            runtime.y,
            runtime.x - dp(34f),
            runtime.y,
        )
        vaultPath.rewind()
        vaultPath.moveTo(core.x, core.y + dp(42f))
        vaultPath.cubicTo(core.x, core.y + dp(78f), vault.x, vault.y - dp(64f), vault.x, vault.y - dp(34f))
        networkMeasure.setPath(networkPath, false)
        vaultMeasure.setPath(vaultPath, false)

        cone.rewind()
        cone.moveTo(width * 0.43f, 0f)
        cone.lineTo(width * 0.57f, 0f)
        cone.lineTo(width * 0.64f, height * 0.34f)
        cone.lineTo(width * 0.36f, height * 0.34f)
        cone.close()

        // The connections' dashes move with the phase: one effect per step of
        // their 3 + 7 dp period, instead of a new effect each frame.
        val step = dashStep()
        connectionDashes = Array(DASH_STEPS) { DashPathEffect(floatArrayOf(dp(3f), dp(7f)), it * step) }
    }

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        startMotion()
    }

    override fun onDetachedFromWindow() {
        stopMotion()
        super.onDetachedFromWindow()
    }

    override fun onWindowVisibilityChanged(visibility: Int) {
        super.onWindowVisibilityChanged(visibility)
        if (visibility == VISIBLE) startMotion() else stopMotion()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        canvas.drawColor(background)
        if (width <= 0 || height <= 0) return

        drawGrid(canvas)
        drawSpotlight(canvas)
        drawConnections(canvas)

        drawNode(canvas, phone, "Physical Device", "Viewer Controller", index = 1)
        drawNode(canvas, core, "Virtroid", "Private Link", index = 0, emphasized = true)
        drawNode(canvas, runtime, "Runtime", "Remote Android", index = 2)
        drawNode(canvas, vault, "User Data", "Encrypted Vault", index = 3)
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
        fillPaint.shader = spotlightShader
        canvas.drawPath(cone, fillPaint)
        fillPaint.shader = null
        fillPaint.shader = coreGlowShader
        canvas.drawCircle(width * 0.5f, height * 0.22f, dp(110f), fillPaint)
        fillPaint.shader = null
    }

    private fun drawConnections(canvas: Canvas) {
        val dashOffset = -phase * dp(36f)
        strokePaint.color = accentShadow
        strokePaint.alpha = 116
        strokePaint.strokeWidth = dp(1f)
        if (connectionDashes.isNotEmpty()) {
            strokePaint.pathEffect = connectionDashes[Math.floorMod((dashOffset / dashStep()).roundToInt(), DASH_STEPS)]
        }
        canvas.drawPath(networkPath, strokePaint)
        canvas.drawPath(vaultPath, strokePaint)
        strokePaint.pathEffect = null

        drawPacket(canvas, networkMeasure, phase)
        drawPacket(canvas, vaultMeasure, (phase + 0.48f) % 1f)
    }

    private fun drawPacket(canvas: Canvas, measure: PathMeasure, progress: Float) {
        measure.getPosTan(measure.length * progress.coerceIn(0f, 1f), packetPosition, null)
        fillPaint.color = withAlpha(accent, 38)
        canvas.drawCircle(packetPosition[0], packetPosition[1], dp(8f), fillPaint)
        fillPaint.color = accentHighlight
        canvas.drawCircle(packetPosition[0], packetPosition[1], dp(2.6f), fillPaint)
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
        nodeRect.set(cx - half, cy - half, cx + half, cy + half)

        if (emphasized) {
            val pulse = (0.5f + 0.5f * sin(phase * 2f * PI)).toFloat()
            strokePaint.color = accent
            strokePaint.alpha = (22 + pulse * 28).toInt()
            strokePaint.strokeWidth = dp(10f)
            canvas.drawRect(nodeRect, strokePaint)
        }

        fillPaint.color = withAlpha(surface, 236)
        canvas.drawRect(nodeRect, fillPaint)
        strokePaint.color = if (emphasized) accent else accentShadow
        strokePaint.alpha = if (emphasized) 150 else 100
        strokePaint.strokeWidth = dp(1f)
        canvas.drawRect(nodeRect, strokePaint)

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
                iconRect.set(cx - dp(13f), cy - dp(12f), cx + dp(13f), cy - dp(4f))
                canvas.drawOval(iconRect, strokePaint)
                canvas.drawLine(cx - dp(13f), cy - dp(8f), cx - dp(13f), cy + dp(11f), strokePaint)
                canvas.drawLine(cx + dp(13f), cy - dp(8f), cx + dp(13f), cy + dp(11f), strokePaint)
                iconRect.set(cx - dp(13f), cy + dp(7f), cx + dp(13f), cy + dp(15f))
                canvas.drawArc(iconRect, 0f, 180f, false, strokePaint)
                iconRect.set(cx - dp(13f), cy - dp(2f), cx + dp(13f), cy + dp(6f))
                canvas.drawArc(iconRect, 0f, 180f, false, strokePaint)
            }
        }
    }

    /**
     * Two full trips of the packets each time the screen shows, starting and
     * ending on the frame shown when animations are off (REST_PHASE), so it
     * neither jumps nor keeps drawing frames on a screen left open. It plays
     * again the next time the window becomes visible.
     */
    private fun startMotion() {
        if (!isAttachedToWindow || windowVisibility != VISIBLE) return
        if (animator != null) return
        phase = REST_PHASE
        if (!ValueAnimator.areAnimatorsEnabled()) {
            invalidate()
            return
        }
        animator = ValueAnimator.ofFloat(REST_PHASE, REST_PHASE + CYCLES).apply {
            duration = CYCLE_MS * CYCLES
            interpolator = LinearInterpolator()
            addUpdateListener {
                phase = (it.animatedValue as Float) % 1f
                invalidate()
            }
            doOnEnd {
                phase = REST_PHASE
                invalidate()
            }
            start()
        }
    }

    private fun stopMotion() {
        animator?.cancel()
        animator = null
        phase = REST_PHASE
    }

    private fun dashStep(): Float = dp(10f) / DASH_STEPS

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

    private companion object {
        const val CYCLE_MS = 6_400L
        const val CYCLES = 2
        const val REST_PHASE = 0.34f
        const val DASH_STEPS = 30 // across the dashes' 3 + 7 dp period
    }
}
