package io.virtroid.client.ui

import android.animation.ValueAnimator
import android.annotation.SuppressLint
import android.app.Activity
import android.app.ActivityOptions
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.PorterDuff
import android.graphics.RectF
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.MotionEvent
import android.view.TextureView
import android.view.View
import android.view.ViewGroup
import android.view.animation.PathInterpolator
import androidx.core.graphics.createBitmap
import androidx.core.graphics.withClip
import androidx.core.graphics.withSave
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import io.virtroid.client.R

/**
 * A runtime's card growing into the viewer, and the viewer shrinking back into
 * it. Each screen opens the other with no window animation and leaves behind a
 * drawing of itself, which the opened screen moves over its own content. So
 * the viewer starts, and connects, as soon as it would with no animation.
 *
 * A drawing is only in memory, is handed over once, and is dropped if the
 * other screen doesn't take it within [TAKE_WITHIN_MS].
 */
object ViewerTransition {
    private const val TAKE_WITHIN_MS = 3_000L
    private val SHOWS_OLD_FRAME = Build.VERSION.SDK_INT < Build.VERSION_CODES.S
    private val WAIT_MS = if (SHOWS_OLD_FRAME) 100f else 0f

    private class Opening(val runtimeId: String, val list: Bitmap, val card: Bitmap, val bounds: RectF)

    private class Closing(val runtimeId: String, val viewer: Bitmap)

    private val main = Handler(Looper.getMainLooper())
    private var opening: Opening? = null
    private var closing: Closing? = null
    private val dropUntaken = Runnable { clear() }

    private fun clear() {
        main.removeCallbacks(dropUntaken)
        opening = null
        closing = null
    }

    private fun keep() {
        main.removeCallbacks(dropUntaken)
        main.postDelayed(dropUntaken, TAKE_WITHIN_MS)
    }

    /**
     * The list, about to open the viewer of [card]'s runtime. Returns the
     * options to open it with, or null where the card can't be seen to grow.
     */
    fun leaveList(activity: Activity, runtimeId: String, card: View?): Bundle? {
        clear()
        val content = activity.content()
        if (!VirtroidMotion.enabled || content == null || card == null || !card.isMostlyVisible()) return null
        opening = Opening(runtimeId, pictureOf(content), pictureOf(card), card.boundsIn(content))
        keep()
        return noAnimation(activity)
    }

    /** The viewer of [runtimeId], now showing: grows out of the card, if a list has just opened it. */
    fun enterViewer(activity: Activity, runtimeId: String) {
        val from = opening?.takeIf { it.runtimeId == runtimeId }
        clear()
        val content = activity.content() ?: return
        if (from == null || !VirtroidMotion.enabled) return
        content.addView(
            GrowView(activity, from.list, from.card, from.bounds),
            ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT),
        )
    }

    /**
     * The viewer, about to go back to the list. [video] is left out of a view's
     * own drawing, so its last frame is drawn over it, unless [videoCovered].
     */
    fun leaveViewer(activity: Activity, runtimeId: String, video: TextureView, videoCovered: Boolean): Bundle? {
        clear()
        val content = activity.content()
        // Closed while out of sight, as after a time without input: nobody sees it shrink.
        val inFront = (activity as? LifecycleOwner)?.lifecycle?.currentState?.isAtLeast(Lifecycle.State.RESUMED) == true
        if (!VirtroidMotion.enabled || !inFront || content == null || !content.isLaidOut) return null
        closing = Closing(runtimeId, pictureOf(content, video.takeUnless { videoCovered }))
        keep()
        return noAnimation(activity)
    }

    /**
     * The list, now showing: the viewer it came back from shrinks into the card
     * [findCard] finds, or fades away if its card isn't there.
     */
    fun enterList(activity: Activity, findCard: (String) -> View?) {
        val from = closing
        clear()
        val content = activity.content() ?: return
        if (from == null || !VirtroidMotion.enabled) return
        content.addView(
            ShrinkView(activity, from.viewer) {
                findCard(from.runtimeId)?.takeIf { it.isMostlyVisible() }?.boundsIn(content)
            },
            ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT),
        )
    }

    /** Ends a move still running, as when its screen goes to the background. */
    fun finish(activity: Activity) {
        val content = activity.content() ?: return
        (content.childCount - 1 downTo 0).map(content::getChildAt).filterIsInstance<MoveView>().forEach(content::removeView)
    }

    private fun Activity.content(): ViewGroup? = findViewById(android.R.id.content)

    /**
     * Before Android 12 a screen that was covered but not stopped comes back
     * with what it last drew, for a frame or two: it is kept out of sight, the
     * screen being left in its place, until it has drawn the move's first frame.
     */
    private fun noAnimation(activity: Activity): Bundle? =
        if (SHOWS_OLD_FRAME) {
            ActivityOptions.makeCustomAnimation(activity, R.anim.v_wait_enter, R.anim.v_wait_exit).toBundle()
        } else {
            ActivityOptions.makeCustomAnimation(activity, 0, 0).toBundle()
        }

    private fun View.isMostlyVisible(): Boolean {
        val seen = android.graphics.Rect()
        return isLaidOut && isShown && getLocalVisibleRect(seen) && seen.height() >= height / 2
    }

    private fun View.boundsIn(ancestor: View): RectF {
        val own = IntArray(2).also(::getLocationInWindow)
        val base = IntArray(2).also(ancestor::getLocationInWindow)
        val left = (own[0] - base[0]).toFloat()
        val top = (own[1] - base[1]).toFloat()
        return RectF(left, top, left + width, top + height)
    }

    /**
     * A bitmap rather than a Picture or a display list: Android 9 replays a
     * Picture without its text and icons, and a display list still belongs to
     * the screen being left.
     */
    private fun pictureOf(view: View, video: TextureView? = null): Bitmap {
        val picture = createBitmap(view.width, view.height)
        val canvas = Canvas(picture)
        // A move still on screen is not part of the screen.
        val moves = (view as? ViewGroup)?.let { group -> (0 until group.childCount).map(group::getChildAt).filterIsInstance<MoveView>() }
        moves?.forEach { it.visibility = View.INVISIBLE }
        view.draw(canvas)
        moves?.forEach { it.visibility = View.VISIBLE }
        video?.takeIf { it.isAvailable && it.width > 1 && it.height > 1 }?.let { texture ->
            // Half size: it is on the move from its first frame.
            val frame = texture.getBitmap(createBitmap(texture.width / 2, texture.height / 2))
            canvas.drawBitmap(frame, null, texture.boundsIn(view), Paint(Paint.FILTER_BITMAP_FLAG))
            frame.recycle()
        }
        // Where video shows, a view leaves a hole in its drawing.
        canvas.drawColor(Color.BLACK, PorterDuff.Mode.DST_OVER)
        return picture
    }

    /** Draws over the whole screen while it moves, takes its touches, and removes itself at the end. */
    @SuppressLint("ViewConstructor", "ClickableViewAccessibility")
    private abstract class MoveView(activity: Activity, durationMs: Long) : View(activity) {
        protected var progress = 0f
            private set
        protected val full = RectF()
        protected val rect = RectF()
        protected val surface = activity.getColor(R.color.v_surface)
        private val border = Paint().apply {
            color = activity.getColor(R.color.v_border)
            style = Paint.Style.STROKE
            strokeWidth = activity.resources.displayMetrics.density
        }
        private val borderAlpha = border.alpha
        private val filter = Paint(Paint.FILTER_BITMAP_FLAG)
        private val easing = PathInterpolator(0.2f, 0f, 0f, 1f)
        private val lengthMs = durationMs * if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) ValueAnimator.getDurationScale() else 1f
        // Behind zero while the window is kept out of sight.
        private var playedMs = -WAIT_MS
        private var lastFrameMs = 0L
        private var done = false

        init {
            importantForAccessibility = IMPORTANT_FOR_ACCESSIBILITY_NO
            elevation = 64f * activity.resources.displayMetrics.density
            outlineProvider = null
        }

        override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
            full.set(0f, 0f, w.toFloat(), h.toFloat())
        }

        override fun onDraw(canvas: Canvas) {
            // Building a screen, or its first frames, can hold the app up: no
            // frame moves it further than a smooth one would, or it would jump.
            val now = drawingTime
            if (lastFrameMs != 0L) playedMs += (now - lastFrameMs).coerceIn(0L, MAX_STEP_MS)
            lastFrameMs = now
            progress = easing.getInterpolation((playedMs / lengthMs).coerceIn(0f, 1f))
            if (playedMs < lengthMs) {
                postInvalidateOnAnimation()
            } else {
                leave()
            }
            drawMove(canvas)
        }

        /** Removes it, and with it the pictures, from outside this drawing. */
        protected fun leave() {
            if (done) return
            done = true
            post { (parent as? ViewGroup)?.removeView(this) }
        }

        protected abstract fun drawMove(canvas: Canvas)

        protected fun between(from: RectF, to: RectF) {
            rect.set(
                from.left + (to.left - from.left) * progress,
                from.top + (to.top - from.top) * progress,
                from.right + (to.right - from.right) * progress,
                from.bottom + (to.bottom - from.bottom) * progress,
            )
        }

        /** 0 until [start], 1 from [end] on. */
        protected fun over(start: Float, end: Float): Float = ((progress - start) / (end - start)).coerceIn(0f, 1f)

        protected fun dim(level: Float): Int = Color.argb((SCRIM * level).toInt(), 0, 0, 0)

        protected fun Canvas.drawFill(color: Int, alpha: Float) {
            withClip(rect) { drawColor(Color.argb((255 * alpha).toInt(), Color.red(color), Color.green(color), Color.blue(color))) }
        }

        /** [picture] across the width of the moving rectangle, from its top. */
        protected fun Canvas.drawInRect(picture: Bitmap, alpha: Float) {
            if (alpha <= 0f || picture.width == 0) return
            val scale = rect.width() / picture.width
            withClip(rect) {
                val layer = if (alpha < 1f) saveLayerAlpha(rect, (255 * alpha).toInt()) else save()
                translate(rect.left, rect.top)
                scale(scale, scale)
                drawBitmap(picture, 0f, 0f, filter)
                restoreToCount(layer)
            }
        }

        protected fun Canvas.drawBorder(alpha: Float) {
            border.alpha = (borderAlpha * alpha).toInt()
            drawRect(rect, border)
        }

        override fun onTouchEvent(event: MotionEvent): Boolean = true

        private companion object {
            const val SCRIM = 140f
            const val MAX_STEP_MS = 24L
        }
    }

    /** Over the viewer: the list stays where it was as the card opens on to the viewer underneath. */
    @SuppressLint("ViewConstructor")
    private class GrowView(
        activity: Activity,
        private val list: Bitmap,
        private val card: Bitmap,
        private val from: RectF,
    ) : MoveView(activity, GROW_MS) {
        override fun drawMove(canvas: Canvas) {
            // The window changed size on the way, as by a rotation: nothing lines up.
            if (list.width != width || list.height != height) {
                leave()
                return
            }
            between(from, full)
            canvas.withSave {
                clipOutRect(rect)
                drawBitmap(list, 0f, 0f, null)
                drawColor(dim(progress))
            }
            canvas.drawFill(surface, 1f - over(0.2f, 0.6f))
            canvas.drawInRect(card, 1f - over(0f, 0.3f))
            canvas.drawBorder(1f - over(0.5f, 0.9f))
        }

        private companion object {
            const val GROW_MS = 420L
        }
    }

    /** Over the list: what the viewer showed shrinks on to the card underneath. */
    @SuppressLint("ViewConstructor")
    private class ShrinkView(
        activity: Activity,
        private val viewer: Bitmap,
        private val findBounds: () -> RectF?,
    ) : MoveView(activity, SHRINK_MS) {
        // Once there was no card to shrink to, one loading part way is too late.
        private var fading = false

        override fun drawMove(canvas: Canvas) {
            if (viewer.width != width || viewer.height != height) {
                leave()
                return
            }
            // Looked up every frame: a refresh may be moving the card.
            val card = if (fading) null else findBounds()
            if (card == null) {
                fading = true
                rect.set(full)
                canvas.drawInRect(viewer, 1f - over(0f, 0.6f))
                return
            }
            between(full, card)
            canvas.withSave {
                clipOutRect(rect)
                drawColor(dim(1f - progress))
            }
            canvas.drawFill(surface, 1f - over(0.75f, 1f))
            canvas.drawInRect(viewer, 1f - over(0.45f, 0.85f))
            canvas.drawBorder(over(0f, 0.3f))
        }

        private companion object {
            const val SHRINK_MS = 360L
        }
    }
}
