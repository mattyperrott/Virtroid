// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui.card

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.addPathNodes
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** Virtroid's palette (design-brief.md section 3): the same on every device, light or dark. */
object Palette {
    val Outer = Color(0xFF131313)
    val Panel = Color(0xFF1B1B1B)
    val Control = Color(0xFF242424)
    val Lime = Color(0xFFAFD135)
    val LimePressed = Color(0xFFD0EC62)
    val OnLime = Color(0xFF0E0E0E)
    val Text = Color(0xFFF4F4F5)
    val Secondary = Color(0xFFA1A1AA)
    val Muted = Color(0xFF8A8A93)
    val Error = Color(0xFFDF7E79)
    val Warning = Color(0xFFF5A623)
    val Border = Color(0x14FFFFFF)
    val Scrim = Color(0x99000000)
}

private val scheme = darkColorScheme(
    primary = Palette.Lime,
    onPrimary = Palette.OnLime,
    primaryContainer = Palette.Control,
    onPrimaryContainer = Palette.Text,
    secondary = Palette.Lime,
    onSecondary = Palette.OnLime,
    background = Palette.Outer,
    onBackground = Palette.Text,
    surface = Palette.Outer,
    onSurface = Palette.Text,
    surfaceVariant = Palette.Control,
    onSurfaceVariant = Palette.Secondary,
    surfaceContainer = Palette.Panel,
    surfaceContainerHigh = Palette.Control,
    surfaceContainerHighest = Palette.Control,
    outline = Palette.Border,
    outlineVariant = Palette.Border,
    error = Palette.Error,
    onError = Palette.OnLime,
)

/** Material 3's type scale in the system font, on the palette. */
@Composable
fun ShieldTheme(content: @Composable () -> Unit) = MaterialTheme(colorScheme = scheme, content = content)

/** A primary action: lime, lighter while pressed. */
@Composable
fun LimeButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    val interaction = remember { MutableInteractionSource() }
    val pressed by interaction.collectIsPressedAsState()
    Button(
        onClick = onClick,
        enabled = enabled,
        interactionSource = interaction,
        modifier = modifier.fillMaxWidth().heightIn(min = 56.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = if (pressed) Palette.LimePressed else Palette.Lime,
            contentColor = Palette.OnLime,
            disabledContainerColor = Palette.Control,
            disabledContentColor = Palette.Muted,
        ),
        contentPadding = PaddingValues(horizontal = 24.dp, vertical = 16.dp),
    ) { Text(text, style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold) }
}

/** A secondary action beside a primary one. */
@Composable
fun QuietButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    OutlinedButton(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier.fillMaxWidth().heightIn(min = 56.dp),
        border = BorderStroke(1.dp, Palette.Border),
        colors = ButtonDefaults.outlinedButtonColors(containerColor = Palette.Control, contentColor = Palette.Text),
    ) { Text(text, style = MaterialTheme.typography.titleMedium) }
}

/** The few icons the card uses, from Material's 24-unit paths. */
object CardIcons {
    val Forward = icon("M12,4l-1.41,1.41L16.17,11H4v2h12.17l-5.58,5.59L12,20l8,-8z")
    val Back = icon("M20,11H7.83l5.59,-5.59L12,4l-8,8 8,8 1.41,-1.41L7.83,13H20v-2z")
    val Expand = icon("M16.59,8.59L12,13.17 7.41,8.59 6,10l6,6 6,-6z")
    val Check = icon("M9,16.17L4.83,12l-1.42,1.41L9,19 21,7l-1.41,-1.41z")
    val Paste = icon(
        "M19,2h-4.18C14.4,0.84 13.3,0 12,0c-1.3,0 -2.4,0.84 -2.82,2L5,2c-1.1,0 -2,0.9 -2,2v16c0,1.1 0.9,2 2,2h14" +
            "c1.1,0 2,-0.9 2,-2L21,4c0,-1.1 -0.9,-2 -2,-2zM12,2c0.55,0 1,0.45 1,1s-0.45,1 -1,1 -1,-0.45 -1,-1 0.45,-1 1,-1z" +
            "M19,20L5,20L5,4h2v3h10L17,4h2v16z",
    )
    val Copy = icon(
        "M16,1L4,1c-1.1,0 -2,0.9 -2,2v14h2L4,3h12L16,1zM19,5L8,5c-1.1,0 -2,0.9 -2,2v14c0,1.1 0.9,2 2,2h11" +
            "c1.1,0 2,-0.9 2,-2L21,7c0,-1.1 -0.9,-2 -2,-2zM19,21L8,21L8,7h11v14z",
    )
    val Warning = icon("M1,21h22L12,2 1,21zM13,18h-2v-2h2v2zM13,14h-2v-4h2v4z")

    private fun icon(path: String): ImageVector =
        ImageVector.Builder(defaultWidth = 24.dp, defaultHeight = 24.dp, viewportWidth = 24f, viewportHeight = 24f)
            .addPath(pathData = addPathNodes(path), fill = SolidColor(Color.Black))
            .build()
}

/**
 * A token's mark with its chain's badge: each in its own recognisable colour
 * and glyph, drawn here rather than copied from the projects' artwork.
 */
@Composable
fun TokenMark(symbol: String, chainId: Int, size: Dp = 28.dp) {
    Box(Modifier.size(size + size / 6)) {
        Coin(symbol, size, Modifier.align(Alignment.TopStart))
        Box(
            Modifier.align(Alignment.BottomEnd).offset(x = 1.dp, y = 1.dp).size(size * 0.5f)
                .clip(CircleShape).background(Palette.Panel).border(1.5.dp, Palette.Panel, CircleShape),
        ) { ChainBadge(chainId, size * 0.5f) }
    }
}

@Composable
private fun Coin(symbol: String, size: Dp, modifier: Modifier = Modifier) {
    when (symbol) {
        "ETH" -> Diamond(Color(0xFF627EEA), size, modifier)
        else -> Glyph(
            text = if (symbol == "USDT") "₮" else "$",
            background = if (symbol == "USDT") Color(0xFF26A17B) else Color(0xFF2775CA),
            size = size,
            modifier = modifier,
        )
    }
}

@Composable
fun ChainBadge(chainId: Int, size: Dp) {
    when (chainId) {
        42161 -> Glyph("A", Color(0xFF213147), size, foreground = Color(0xFF28A0F0))
        8453 -> Canvas(Modifier.size(size)) {
            drawCircle(Color(0xFF0052FF))
            drawRect(Color.White, topLeft = Offset(0f, this.size.height * 0.44f), size = androidx.compose.ui.geometry.Size(this.size.width * 0.62f, this.size.height * 0.12f))
        }
        10 -> Glyph("OP", Color(0xFFFF0420), size)
        else -> Diamond(Color(0xFF627EEA), size)
    }
}

@Composable
private fun Glyph(text: String, background: Color, size: Dp, modifier: Modifier = Modifier, foreground: Color = Color.White) {
    Box(modifier.size(size).clip(CircleShape).background(background), contentAlignment = Alignment.Center) {
        Text(
            text,
            color = foreground,
            fontWeight = FontWeight.Bold,
            fontSize = (size.value * if (text.length > 1) 0.36f else 0.56f).sp,
            lineHeight = (size.value * 0.6f).sp,
        )
    }
}

@Composable
private fun Diamond(background: Color, size: Dp, modifier: Modifier = Modifier) {
    Canvas(modifier.size(size)) {
        drawCircle(background)
        val w = this.size.width
        val h = this.size.height
        val path = Path().apply {
            moveTo(w * 0.5f, h * 0.16f)
            lineTo(w * 0.73f, h * 0.52f)
            lineTo(w * 0.5f, h * 0.84f)
            lineTo(w * 0.27f, h * 0.52f)
            close()
        }
        drawPath(path, Color.White)
        drawLine(background, Offset(w * 0.27f, h * 0.52f), Offset(w * 0.5f, h * 0.62f), strokeWidth = w * 0.05f)
        drawLine(background, Offset(w * 0.73f, h * 0.52f), Offset(w * 0.5f, h * 0.62f), strokeWidth = w * 0.05f)
    }
}
