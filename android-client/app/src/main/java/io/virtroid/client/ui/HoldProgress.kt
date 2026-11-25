package io.virtroid.client.ui

/**
 * How far a press held to confirm has got. Fills while held and drains once
 * let go; [advance] answers true once, when it is full.
 */
internal class HoldProgress(private val holdMs: Long, private val drainMs: Long) {
    var level = 0f
        private set
    var isHeld = false
        private set
    private var done = false

    fun hold() {
        if (!done) isHeld = true
    }

    fun release() {
        isHeld = false
    }

    fun reset() {
        isHeld = false
        done = false
        level = 0f
    }

    /** Whether there is still something to move. */
    val isMoving: Boolean
        get() = !done && (isHeld || level > 0f)

    fun advance(elapsedMs: Long): Boolean {
        if (done) return false
        // A stalled screen must not count as held: no one sees it fill.
        val step = elapsedMs.coerceIn(0L, MAX_STEP_MS).toFloat()
        level = if (isHeld) level + step / holdMs else level - step / drainMs
        level = level.coerceIn(0f, 1f)
        if (isHeld && level >= 1f) {
            done = true
            isHeld = false
            return true
        }
        return false
    }

    private companion object {
        const val MAX_STEP_MS = 50L
    }
}
