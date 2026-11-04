package io.virtroid.client.viewer

import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.os.Handler
import android.os.Looper
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.security.copySensitiveToClipboard

// Long enough to open a wallet on the phone and paste an address into it.
internal const val RUNTIME_CLIP_CLEAR_AFTER_MS = 120_000L

/**
 * When the viewer may touch the phone's clipboard. Each clip is known by the
 * time Android stamped on it, which tells a new copy of the same text from one
 * already dealt with, without keeping the text.
 */
internal class ClipboardGate {
    /** The runtime answered CLIPBOARD_READY; an older one never does. */
    var runtimeReady = false

    /** The viewer has input focus, the only time Android lets it read the clipboard. */
    var focused = false

    private var settledClip: Long? = null
    private var runtimeClip: Long? = null

    /**
     * Only while the owner is in the viewer: a runtime app in the background
     * must not swap what they are about to paste into something on the phone.
     */
    val takesRuntimeClips: Boolean
        get() = runtimeReady && focused

    fun shouldSend(clipTimestamp: Long): Boolean =
        runtimeReady && focused && clipTimestamp != settledClip

    /** Marks a clip of the phone's as one the runtime has. */
    fun settle(clipTimestamp: Long) {
        settledClip = clipTimestamp
    }

    /** Marks a clip as one that came from the runtime, so it doesn't go back. */
    fun settleFromRuntime(clipTimestamp: Long?) {
        settledClip = clipTimestamp
        runtimeClip = clipTimestamp
    }

    /** Whether this clip is still the one the runtime sent, and has outlived its time. */
    fun isOverdue(clipTimestamp: Long, now: Long): Boolean =
        clipTimestamp == runtimeClip && now - clipTimestamp >= RUNTIME_CLIP_CLEAR_AFTER_MS
}

/**
 * Keeps the phone's clipboard and the runtime's in step for one viewer session,
 * text only. Never logs a clip. Calls [sendToRuntime] on the main thread.
 */
internal class ViewerClipboard(
    private val context: Context,
    private val sendToRuntime: (String) -> Unit,
    private val onCopiedUnconfirmed: () -> Unit,
) {
    private val clipboard = context.getSystemService(ClipboardManager::class.java)
    private val main = Handler(Looper.getMainLooper())
    private val gate = ClipboardGate()
    private val listener = ClipboardManager.OnPrimaryClipChangedListener { sendCurrentClip() }
    private var closed = false

    fun runtimeReady() {
        main.post {
            if (!closed && !gate.runtimeReady) {
                gate.runtimeReady = true
                clipboard.addPrimaryClipChangedListener(listener)
                sendCurrentClip()
            }
        }
    }

    fun applyFromRuntime(text: String) {
        main.post {
            if (!closed && gate.takesRuntimeClips) {
                // What a runtime app put on its clipboard must not be able to
                // crash the viewer, say if Android finds it too large.
                runCatching {
                    val systemConfirms = context.copySensitiveToClipboard(CLIP_LABEL, text, RUNTIME_CLIP_CLEAR_AFTER_MS)
                    gate.settleFromRuntime(clipboard.primaryClipDescription?.timestamp)
                    if (!systemConfirms) {
                        onCopiedUnconfirmed()
                    }
                }
            }
        }
    }

    fun onWindowFocusChanged(hasFocus: Boolean) {
        gate.focused = hasFocus
        if (hasFocus) {
            clearOverdueRuntimeClip()
            sendCurrentClip()
        }
    }

    fun close() {
        closed = true
        main.removeCallbacksAndMessages(null)
        clipboard.removePrimaryClipChangedListener(listener)
    }

    // copySensitiveToClipboard's timer leaves the clip if it fires while the
    // viewer lacks focus, when Android hides the clipboard from it: the owner
    // is usually off pasting this clip into another app.
    private fun clearOverdueRuntimeClip() {
        runCatching {
            val timestamp = clipboard.primaryClipDescription?.timestamp ?: return
            if (gate.isOverdue(timestamp, System.currentTimeMillis()) && AppSettingsStore(context).autoClearClipboard) {
                clipboard.clearPrimaryClip()
            }
        }
    }

    private fun sendCurrentClip() {
        if (closed) {
            return
        }
        runCatching {
            // The description first: reading it, unlike the clip, shows no
            // "pasted from" notice, so a clip that won't be sent never shows one.
            val description = clipboard.primaryClipDescription ?: return
            if (!gate.shouldSend(description.timestamp)) {
                return
            }
            gate.settle(description.timestamp)
            if (description.hasMimeType(TEXT_MIME_TYPES)) {
                clipboard.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.text?.let {
                    sendToRuntime(it.toString())
                }
            }
        }
    }

    private companion object {
        const val CLIP_LABEL = "Virtroid cloud phone"
        const val TEXT_MIME_TYPES = "text/*"
    }
}
