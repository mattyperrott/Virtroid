package io.virtroid.client.ui

import android.animation.Animator
import android.animation.AnimatorListenerAdapter
import android.animation.ValueAnimator
import android.annotation.SuppressLint
import android.content.Context
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Path
import android.graphics.PathMeasure
import android.os.Build
import android.os.SystemClock
import android.util.AttributeSet
import android.view.HapticFeedbackConstants
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.animation.LinearInterpolator
import android.view.animation.PathInterpolator
import android.widget.FrameLayout
import androidx.core.graphics.withSave
import androidx.core.view.children
import io.virtroid.client.R
import kotlin.math.abs
import kotlin.math.exp
import kotlin.math.min

/**
 * Holds a list that can be pulled down to refresh. The Virtroid mark draws
 * itself in the gap as the finger pulls; let go once it is whole and it holds
 * there, a light running round it, until [finish]. The first child is the
 * list, and all children move together.
 */
class PullRefreshLayout @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
) : FrameLayout(context, attrs) {
    /** Called once for each pull let go past the threshold; [finish] ends it. */
    var onRefresh: () -> Unit = {}

    /** Whether a pull may start now. */
    var canPull: () -> Boolean = { true }

    private val density = resources.displayMetrics.density
    private val touchSlop = ViewConfiguration.get(context).scaledTouchSlop
    private val holdAt = HOLD_DP * density
    private val mark = PullMark(context)
    private val settling = PathInterpolator(0.2f, 0f, 0f, 1f)

    private var pointer = MotionEvent.INVALID_POINTER_ID
    private var downX = 0f
    private var downY = 0f
    private var pulling = false
    private var past = false
    private var refreshing = false
    private var refreshedAtMs = 0L
    private var offset = 0f
    private var settle: ValueAnimator? = null
    private var running: ValueAnimator? = null

    init {
        setWillNotDraw(false)
    }

    override fun onInterceptTouchEvent(event: MotionEvent): Boolean {
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                pulling = false
                pointer = event.getPointerId(0)
                downX = event.x
                downY = event.y
            }
            MotionEvent.ACTION_MOVE -> {
                val index = event.findPointerIndex(pointer)
                if (index < 0 || pulling) return pulling
                val down = event.getY(index) - downY
                val across = abs(event.getX(index) - downX)
                if (down > touchSlop && down > across && mayStart()) start(event.getY(index))
            }
            MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> pointer = MotionEvent.INVALID_POINTER_ID
        }
        return pulling
    }

    @SuppressLint("ClickableViewAccessibility") // Pulls only; refreshing has an accessibility action of its own.
    override fun onTouchEvent(event: MotionEvent): Boolean {
        when (event.actionMasked) {
            // A touch on no child: below a short list, say.
            MotionEvent.ACTION_DOWN -> return mayStart()
            MotionEvent.ACTION_MOVE -> {
                val index = event.findPointerIndex(pointer)
                if (index < 0) return false
                val y = event.getY(index)
                if (pulling) {
                    pullTo(y - downY)
                } else if (y - downY > touchSlop && y - downY > abs(event.getX(index) - downX) && mayStart()) {
                    start(y)
                }
            }
            MotionEvent.ACTION_POINTER_UP -> if (event.getPointerId(event.actionIndex) == pointer) letGo(cancelled = true)
            MotionEvent.ACTION_UP -> letGo(cancelled = false)
            MotionEvent.ACTION_CANCEL -> letGo(cancelled = true)
        }
        return true
    }

    private fun mayStart(): Boolean =
        isEnabled && !refreshing && canPull() && getChildAt(0)?.canScrollVertically(-1) == false

    private fun start(y: Float) {
        pulling = true
        past = false
        // The pull starts here, not a slop's jump below.
        downY = y
        settle?.cancel()
        parent?.requestDisallowInterceptTouchEvent(true)
    }

    private fun pullTo(distance: Float) {
        moveTo(offsetFor(distance / density) * density)
        val nowPast = offset >= holdAt
        if (nowPast != past) {
            past = nowPast
            if (past) performHapticFeedback(thresholdHaptic())
        }
    }

    private fun letGo(cancelled: Boolean) {
        if (!pulling) return
        pulling = false
        pointer = MotionEvent.INVALID_POINTER_ID
        if (past && !cancelled) {
            refreshing = true
            refreshedAtMs = SystemClock.uptimeMillis()
            settleTo(holdAt)
            startRunning()
            onRefresh()
        } else {
            settleTo(0f)
        }
    }

    /** The refresh is done. It shows for a moment, however quick it was. */
    fun finish() {
        if (!refreshing) return
        val shownFor = SystemClock.uptimeMillis() - refreshedAtMs
        if (shownFor < SHOWN_AT_LEAST_MS) {
            postDelayed(::finish, SHOWN_AT_LEAST_MS - shownFor)
            return
        }
        refreshing = false
        stopRunning()
        settleTo(0f)
    }

    private fun moveTo(value: Float) {
        offset = value
        children.forEach { it.translationY = value }
        mark.drawn = if (refreshing) 1f else (value / holdAt).coerceIn(0f, 1f)
        invalidate()
    }

    private fun settleTo(target: Float) {
        settle?.cancel()
        if (!VirtroidMotion.enabled) {
            moveTo(target)
            return
        }
        settle = ValueAnimator.ofFloat(offset, target).apply {
            duration = SETTLE_MS
            interpolator = settling
            addUpdateListener { moveTo(it.animatedValue as Float) }
            start()
        }
    }

    private fun startRunning() {
        if (running != null || !VirtroidMotion.enabled) return
        running = ValueAnimator.ofFloat(0f, 1f).apply {
            duration = RUN_MS
            repeatCount = ValueAnimator.INFINITE
            interpolator = LinearInterpolator()
            addUpdateListener {
                mark.running = it.animatedValue as Float
                invalidate()
            }
            addListener(object : AnimatorListenerAdapter() {
                override fun onAnimationEnd(animation: Animator) {
                    mark.running = null
                }
            })
            start()
        }
    }

    private fun stopRunning() {
        running?.cancel()
        running = null
    }

    override fun onVisibilityAggregated(isVisible: Boolean) {
        super.onVisibilityAggregated(isVisible)
        if (isVisible && refreshing) startRunning() else if (!isVisible) stopRunning()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        if (offset < 1f) return
        // Grows with the gap, so the gap always holds all of it.
        val height = min(MARK_DP * density, offset * MARK_OF_GAP)
        canvas.withSave {
            translate(width / 2f, offset / 2f)
            scale(height / VirtroidMarkShape.HEIGHT, height / VirtroidMarkShape.HEIGHT)
            translate(
                -VirtroidMarkShape.LEFT - VirtroidMarkShape.WIDTH / 2f,
                -VirtroidMarkShape.TOP - VirtroidMarkShape.HEIGHT / 2f,
            )
            mark.draw(this, alpha = (offset / (holdAt * 0.35f)).coerceIn(0f, 1f))
        }
    }

    private fun thresholdHaptic(): Int =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            HapticFeedbackConstants.GESTURE_THRESHOLD_ACTIVATE
        } else {
            HapticFeedbackConstants.KEYBOARD_TAP
        }

    internal companion object {
        private const val HOLD_DP = 80f
        private const val MOST_DP = 140f
        private const val MARK_DP = 54f
        private const val MARK_OF_GAP = 0.72f
        private const val SETTLE_MS = 280L
        private const val RUN_MS = 1_100L
        private const val SHOWN_AT_LEAST_MS = 700L

        /** How far the list gives for a finger [distanceDp] down: less and less, never past a limit. */
        fun offsetFor(distanceDp: Float): Float =
            if (distanceDp <= 0f) 0f else MOST_DP * (1f - exp(-distanceDp * 0.6f / MOST_DP))
    }
}

/** The mark as a line that draws itself piece by piece, then fills. */
private class PullMark(context: Context) {
    /** How much of it is drawn, 0 to 1. */
    var drawn = 0f

    /** Where the light running round it is, 0 to 1, or null for none. */
    var running: Float? = null

    private val accent = context.getColor(R.color.v_accent)
    private val light = context.getColor(R.color.v_text_primary)
    private val line = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        style = Paint.Style.STROKE
        strokeCap = Paint.Cap.ROUND
        strokeJoin = Paint.Join.ROUND
        strokeWidth = 5f
    }
    private val fill = Paint(Paint.ANTI_ALIAS_FLAG)
    private val pieces = VirtroidMarkShape().let { listOf(it.top, it.upper, it.middle, it.lower, it.bottom) }
    private val measures = pieces.map { PathMeasure(it, false) }
    private val segment = Path()

    fun draw(canvas: Canvas, alpha: Float) {
        val filled = ((drawn - FILL_FROM) / (1f - FILL_FROM)).coerceIn(0f, 1f)
        if (filled > 0f) {
            fill.color = accent
            fill.alpha = (255 * alpha * filled * if (running == null) 0.9f else 0.35f).toInt()
            pieces.forEach { canvas.drawPath(it, fill) }
        }
        line.color = accent
        line.alpha = (255 * alpha).toInt()
        // The pieces one after another, each starting a little before the last is done.
        val share = 1f / (measures.size - OVERLAP * (measures.size - 1))
        val lines = (drawn / FILL_FROM).coerceIn(0f, 1f)
        measures.forEachIndexed { index, measure ->
            val start = index * share * (1f - OVERLAP)
            drawSegment(canvas, measure, 0f, ((lines - start) / share).coerceIn(0f, 1f))
        }
        running?.let { at ->
            line.color = light
            line.alpha = (230 * alpha).toInt()
            measures.forEachIndexed { index, measure ->
                val head = (at + index * 0.09f) % 1f
                drawSegment(canvas, measure, head - LIGHT, head)
                // Over the seam, where the line starts and ends.
                if (head < LIGHT) drawSegment(canvas, measure, 1f + head - LIGHT, 1f)
            }
        }
    }

    private fun drawSegment(canvas: Canvas, measure: PathMeasure, from: Float, to: Float) {
        if (to <= 0f || to <= from) return
        segment.rewind()
        measure.getSegment(measure.length * from.coerceAtLeast(0f), measure.length * to, segment, true)
        canvas.drawPath(segment, line)
    }

    private companion object {
        const val FILL_FROM = 0.8f
        const val OVERLAP = 0.35f
        const val LIGHT = 0.22f
    }
}
