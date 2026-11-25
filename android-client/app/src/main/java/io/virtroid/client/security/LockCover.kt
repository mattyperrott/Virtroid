package io.virtroid.client.security

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import android.view.View
import android.view.Window
import java.lang.ref.WeakReference
import java.util.WeakHashMap

/**
 * Hides what a screen and its dialogs show while the app waits to be unlocked.
 * The unlock screen opens only once the screen beneath has resumed, and Android
 * may draw that screen first; hidden, it can draw nothing but its background.
 * Hiding leaves the views as they are: text, scroll and dialogs come back as
 * they were. Main thread only.
 */
object LockCover {
    private val activities = WeakHashMap<Activity, HiddenContent>()
    private val dialogs = WeakHashMap<Window, WeakReference<Activity>>()
    private val hiddenDialogs = WeakHashMap<Window, HiddenContent>()

    /** Hides [activity] and its dialogs, also ones it opens from now on, until [reveal]. */
    fun cover(activity: Activity) {
        if (activities.containsKey(activity)) {
            return
        }
        HiddenContent.hide(activity.window)?.let { activities[activity] = it }
        coverDialogs(activity)
    }

    /** Shows everything hidden: the app is unlocked. */
    fun reveal() {
        activities.values.forEach(HiddenContent::show)
        activities.clear()
        hiddenDialogs.values.forEach(HiddenContent::show)
        hiddenDialogs.clear()
    }

    /** Includes a dialog, before it is first shown, in what hiding its screen hides. */
    fun watchDialog(dialog: Window, context: Context) {
        val activity = context.findActivity() ?: return
        dialog.decorView.addOnAttachStateChangeListener(object : View.OnAttachStateChangeListener {
            // On attach, the window has drawn nothing yet.
            override fun onViewAttachedToWindow(view: View) {
                dialogs[dialog] = WeakReference(activity)
                if (activities.containsKey(activity)) {
                    coverDialogs(activity)
                }
            }

            override fun onViewDetachedFromWindow(view: View) {
                dialogs.remove(dialog)
                // One built once and shown again later must not come back hidden.
                hiddenDialogs.remove(dialog)?.show()
            }
        })
    }

    private fun coverDialogs(activity: Activity) {
        dialogs.forEach { (dialog, owner) ->
            if (owner.get() === activity && !hiddenDialogs.containsKey(dialog)) {
                HiddenContent.hide(dialog)?.let { hiddenDialogs[dialog] = it }
            }
        }
    }

    private fun Context.findActivity(): Activity? {
        var context: Context? = this
        while (context is ContextWrapper) {
            if (context is Activity) {
                return context
            }
            context = context.baseContext
        }
        return null
    }

    /**
     * A window's content made invisible, and the view in it that had focus,
     * which hiding takes away. A dialog's panel belongs to its window, not to
     * the content, so a floating window goes clear as a whole.
     */
    private class HiddenContent private constructor(window: Window, content: View) {
        private val decor = WeakReference(window.decorView.takeIf { window.isFloating })
        private val content = WeakReference(content)
        private val focused = WeakReference(window.currentFocus)

        init {
            decor.get()?.alpha = 0f
            content.visibility = View.INVISIBLE
            // A window under another app's, such as a permission dialog, is
            // started on waking but, on Android 9, not laid out: with no surface
            // to draw on, its last picture stays up. This lays it out.
            window.attributes = window.attributes
        }

        fun show() {
            decor.get()?.alpha = 1f
            content.get()?.visibility = View.VISIBLE
            focused.get()?.requestFocus()
        }

        companion object {
            fun hide(window: Window): HiddenContent? =
                window.decorView.findViewById<View>(android.R.id.content)?.let { HiddenContent(window, it) }
        }
    }
}
