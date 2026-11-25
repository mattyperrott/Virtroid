package io.virtroid.client.ui

import android.graphics.Path

/** The Virtroid mark's pieces, in its art units. Not to be changed once built. */
internal class VirtroidMarkShape {
    val top = Path().apply {
        moveTo(156f, 151f)
        lineTo(156f, 145f)
        quadTo(156f, 132f, 169f, 132f)
        lineTo(257f, 132f)
        quadTo(270f, 132f, 271f, 146f)
        lineTo(271f, 151f)
        close()
    }
    val upper = polygon(156f, 158f, 270f, 158f, 270f, 192f, 156f, 172f)
    val middle = polygon(156f, 178f, 270f, 198f, 270f, 207f, 156f, 249f)
    val lower = polygon(160f, 255f, 273f, 211f, 275f, 258f, 160f, 265f)
    val bottom = Path().apply {
        moveTo(161f, 274f)
        lineTo(275f, 268f)
        lineTo(275f, 287f)
        quadTo(275f, 300f, 262f, 301f)
        lineTo(175f, 301f)
        quadTo(162f, 301f, 161f, 288f)
        close()
    }

    companion object {
        const val LEFT = 140f
        const val TOP = 122f
        const val WIDTH = 152f
        const val HEIGHT = 190f

        fun polygon(vararg values: Float): Path = Path().apply {
            moveTo(values[0], values[1])
            var index = 2
            while (index < values.size) {
                lineTo(values[index], values[index + 1])
                index += 2
            }
            close()
        }
    }
}
