package io.virtroid.client.ui

import android.animation.ValueAnimator
import android.view.View
import android.view.animation.AlphaAnimation
import android.view.animation.Animation

object VirtroidMotion {
    fun setStatusPulse(view: View, active: Boolean) {
        view.clearAnimation()
        view.alpha = 1f
        if (!active || !ValueAnimator.areAnimatorsEnabled()) return

        view.startAnimation(
            AlphaAnimation(0.42f, 1f).apply {
                duration = 900L
                repeatMode = Animation.REVERSE
                repeatCount = Animation.INFINITE
            },
        )
    }
}
