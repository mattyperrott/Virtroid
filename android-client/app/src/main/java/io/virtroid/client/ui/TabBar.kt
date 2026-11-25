package io.virtroid.client.ui

import android.animation.ValueAnimator
import android.content.Context
import android.graphics.Canvas
import android.graphics.Rect
import android.util.AttributeSet
import android.view.View
import android.view.animation.LinearInterpolator
import android.view.animation.PathInterpolator
import android.widget.ImageView
import android.widget.LinearLayout
import androidx.appcompat.content.res.AppCompatResources
import io.virtroid.client.R

/**
 * The bottom bar: a highlight behind the selected tab, which slides to the next
 * one, stretching on the way. The screens behind the tabs cover the bar, so it
 * shows where a tap leads, and on coming back, where from.
 */
class TabBar @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
) : LinearLayout(context, attrs) {
    private val highlight = AppCompatResources.getDrawable(context, R.drawable.bg_tab_highlight)
    private val accent = context.getColor(R.color.v_accent)
    private val muted = context.getColor(R.color.v_text_muted)
    private val leading = PathInterpolator(0.2f, 0f, 0f, 1f)
    private val trailing = PathInterpolator(0.5f, 0f, 0.1f, 1f)
    private val from = Rect()
    private val at = Rect()
    private var tabs = emptyList<ImageView>()
    private var slide: ValueAnimator? = null

    var selected: ImageView? = null
        private set

    init {
        setWillNotDraw(false)
    }

    /** The bar's tabs, the [first] of them selected. */
    fun setTabs(tabs: List<ImageView>, first: ImageView) {
        this.tabs = tabs
        selected = first
        tabs.forEach { show(it) }
    }

    /** Moves the highlight to [tab] over [durationMs]. */
    fun select(tab: ImageView, durationMs: Long = SLIDE_MS) {
        if (tab === selected) return
        selected = tab
        tabs.forEach { show(it) }
        slide?.cancel()
        slide = null
        if (!VirtroidMotion.enabled || !isShown || !isLaidOut) {
            invalidate()
            return
        }
        from.set(at)
        slide = ValueAnimator.ofFloat(0f, 1f).apply {
            duration = durationMs
            interpolator = LinearInterpolator()
            addUpdateListener { invalidate() }
            start()
        }
    }

    private fun show(tab: ImageView) {
        val isSelected = tab === selected
        tab.isSelected = isSelected
        VirtroidMotion.changeTint(tab, if (isSelected) accent else muted)
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        val tab = selected ?: return
        val to = boundsOf(tab)
        val progress = slide?.animatedFraction ?: 1f
        if (progress >= 1f) {
            at.set(to)
        } else {
            // The edge ahead leaves at once and the one behind follows.
            val ahead = leading.getInterpolation(progress)
            val behind = trailing.getInterpolation(progress)
            val right = to.left > from.left
            at.set(
                between(from.left, to.left, if (right) behind else ahead),
                to.top,
                between(from.right, to.right, if (right) ahead else behind),
                to.bottom,
            )
        }
        highlight?.bounds = at
        highlight?.draw(canvas)
    }

    private fun boundsOf(tab: View): Rect =
        Rect(0, 0, tab.width, tab.height).also { offsetDescendantRectToMyCoords(tab, it) }

    private fun between(from: Int, to: Int, level: Float): Int = (from + (to - from) * level).toInt()

    companion object {
        const val SLIDE_MS = 360L

        /** Fits in what is seen of the bar while the tab's screen opens over it. */
        const val SLIDE_AWAY_MS = 180L
    }
}
