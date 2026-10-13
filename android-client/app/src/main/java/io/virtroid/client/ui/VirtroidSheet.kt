package io.virtroid.client.ui

import android.app.Activity
import android.app.Dialog
import android.content.res.ColorStateList
import android.graphics.Color
import android.view.View
import android.view.WindowManager
import androidx.annotation.DrawableRes
import androidx.core.graphics.drawable.toDrawable
import androidx.core.view.isVisible
import io.virtroid.client.R
import io.virtroid.client.databinding.DialogVirtroidSheetBinding
import io.virtroid.client.databinding.ItemSheetRowBinding

/**
 * The app's own dialog in place of the stock white one: a title, an optional
 * message, rows of actions or items, a note, and up to two buttons.
 */
class VirtroidSheet(private val activity: Activity) {
    private val binding = DialogVirtroidSheetBinding.inflate(activity.layoutInflater)
    private val dialog = Dialog(activity).apply {
        setContentView(binding.root)
        setCancelable(true)
        window?.setBackgroundDrawable(Color.TRANSPARENT.toDrawable())
        window?.setLayout(WindowManager.LayoutParams.MATCH_PARENT, WindowManager.LayoutParams.WRAP_CONTENT)
    }

    enum class Tone { ACCENT, DANGER, MUTED }

    fun title(text: String) = apply { binding.sheetTitleText.text = text }

    fun message(text: String) = apply {
        binding.sheetBodyText.text = text
        binding.sheetBodyText.isVisible = text.isNotBlank()
    }

    fun note(text: String) = apply {
        binding.sheetNoteText.text = text
        binding.sheetNoteText.isVisible = text.isNotBlank()
    }

    /**
     * Adds a row. Tapping it runs [onClick] and closes the sheet; a row with an
     * [action] label shows that instead of a chevron.
     */
    fun row(
        @DrawableRes icon: Int,
        title: String,
        subtitle: String? = null,
        action: String? = null,
        actionTone: Tone = Tone.ACCENT,
        dimmed: Boolean = false,
        onClick: (() -> Unit)? = null,
    ) = apply {
        val row = ItemSheetRowBinding.inflate(activity.layoutInflater, binding.sheetRows, false)
        row.sheetRowIcon.setImageResource(icon)
        row.sheetRowTitle.text = title
        row.sheetRowSubtitle.text = subtitle.orEmpty()
        row.sheetRowSubtitle.isVisible = !subtitle.isNullOrBlank()
        row.sheetRowAction.text = action.orEmpty()
        row.sheetRowAction.isVisible = !action.isNullOrBlank()
        row.sheetRowAction.setTextColor(activity.getColor(toneColor(actionTone)))
        row.sheetRowChevron.isVisible = onClick != null && action.isNullOrBlank()
        if (dimmed) {
            row.root.alpha = 0.55f
            row.sheetRowIcon.imageTintList = ColorStateList.valueOf(activity.getColor(R.color.v_text_muted))
        }
        if (onClick != null) {
            row.root.setOnClickListener {
                dialog.dismiss()
                onClick()
            }
        } else {
            row.root.isClickable = false
            row.root.foreground = null
        }
        row.root.contentDescription = listOfNotNull(title, subtitle, action).joinToString(". ")
        binding.sheetRows.addView(row.root)
        binding.sheetScroll.isVisible = true
    }

    /** The main button; [destructive] makes it red for deleting or revoking. */
    fun primary(label: String, destructive: Boolean = false, onClick: () -> Unit) = apply {
        binding.sheetPrimaryButton.text = label
        binding.sheetPrimaryButton.isVisible = true
        if (destructive) {
            binding.sheetPrimaryButton.backgroundTintList = ColorStateList.valueOf(activity.getColor(R.color.v_danger))
            binding.sheetPrimaryButton.setTextColor(activity.getColor(R.color.v_bg))
        }
        binding.sheetPrimaryButton.setOnClickListener {
            dialog.dismiss()
            onClick()
        }
        alignButtons()
    }

    fun secondary(label: String, onClick: () -> Unit = {}) = apply {
        binding.sheetSecondaryButton.text = label
        binding.sheetSecondaryButton.isVisible = true
        binding.sheetSecondaryButton.setOnClickListener {
            dialog.dismiss()
            onClick()
        }
        alignButtons()
    }

    fun onCancel(action: () -> Unit) = apply { dialog.setOnCancelListener { action() } }

    fun show() {
        if (!activity.isFinishing && !activity.isDestroyed) {
            binding.sheetButtons.isVisible = binding.sheetPrimaryButton.isVisible || binding.sheetSecondaryButton.isVisible
            dialog.show()
        }
    }

    private fun alignButtons() {
        // A lone primary button needs no gap before it.
        val params = binding.sheetPrimaryButton.layoutParams as android.widget.LinearLayout.LayoutParams
        params.marginStart = if (binding.sheetSecondaryButton.visibility == View.VISIBLE) {
            (12 * activity.resources.displayMetrics.density).toInt()
        } else {
            0
        }
        binding.sheetPrimaryButton.layoutParams = params
    }

    private fun toneColor(tone: Tone): Int = when (tone) {
        Tone.ACCENT -> R.color.v_accent
        Tone.DANGER -> R.color.v_danger
        Tone.MUTED -> R.color.v_text_muted
    }
}
