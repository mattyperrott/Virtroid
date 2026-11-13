package io.virtroid.client.ui

import androidx.core.view.WindowInsetsCompat

/**
 * What a screen drawn edge to edge keeps its content clear of: the system bars
 * and the display cutout, on whichever sides they are (three-button navigation
 * and cutouts sit beside the content in landscape). Screens with text fields
 * add the keyboard: drawing edge to edge, Android doesn't resize the window
 * for it, so without it the bottom of the screen stays under the keyboard.
 */
object SafeArea {
    fun bars(): Int = WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout()

    fun barsAndKeyboard(): Int = bars() or WindowInsetsCompat.Type.ime()
}
