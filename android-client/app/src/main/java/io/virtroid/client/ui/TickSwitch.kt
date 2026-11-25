package io.virtroid.client.ui

import android.content.Context
import android.os.Build
import android.util.AttributeSet
import android.view.HapticFeedbackConstants
import com.google.android.material.switchmaterial.SwitchMaterial

/**
 * A switch felt as it flips under a finger, by tap or by drag. Set by the app,
 * as when a screen fills in its settings, it stays silent. Follows the phone's
 * own touch feedback setting.
 */
class TickSwitch @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null,
) : SwitchMaterial(context, attrs) {
    override fun setChecked(checked: Boolean) {
        val flipped = checked != isChecked
        super.setChecked(checked)
        // Both a tap and the end of a drag set it while the finger's press is still on.
        if (flipped && isPressed) performHapticFeedback(flipHaptic(checked))
    }

    private fun flipHaptic(on: Boolean): Int = when {
        Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE -> HapticFeedbackConstants.CLOCK_TICK
        on -> HapticFeedbackConstants.TOGGLE_ON
        else -> HapticFeedbackConstants.TOGGLE_OFF
    }
}
