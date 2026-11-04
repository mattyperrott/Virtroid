// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.app.Application
import android.graphics.Bitmap
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import io.virtroid.shield.Address
import io.virtroid.shield.AddressCheck
import io.virtroid.shield.AmountInput
import io.virtroid.shield.Amounts
import io.virtroid.shield.Deposit
import io.virtroid.shield.ExitPlan
import io.virtroid.shield.Quote
import io.virtroid.shield.R
import io.virtroid.shield.Shield
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.engine.EngineException
import io.virtroid.shield.watch.WatchService
import kotlinx.coroutines.launch
import java.math.BigInteger

/** Which screen is up. */
sealed interface Screen {
    data object Home : Screen
    data object Restore : Screen
    /** The 12 words: [first] when they've only just been made, and must be acknowledged. */
    data class Backup(val words: String, val first: Boolean) : Screen
    data object SendForm : Screen
    data class SendReview(val quote: Quote, val recipient: String) : Screen
    data object Working : Screen
    data class Exit(val symbol: String, val decimals: Int, val deposit: Deposit, val plan: ExitPlan?) : Screen
}

class ShieldModel(application: Application) : AndroidViewModel(application) {
    private val app = application as ShieldApp
    val shield = app.shield

    var screen by mutableStateOf<Screen>(Screen.Home)
        private set
    var busy by mutableStateOf(false)
        private set
    var error by mutableStateOf<String?>(null)
        private set

    // The send form, kept across its review.
    var symbol by mutableStateOf<String?>(null)
    var amount by mutableStateOf("")
    var recipient by mutableStateOf("")

    // The restore form.
    var restoreWords by mutableStateOf("")

    private var qrFor: String? = null
    private var qrImage: ImageBitmap? = null

    fun load() = launch { shield.load() }

    fun check(force: List<String> = emptyList()) = launch { shield.check(force) }

    /** Steps back a screen; false when there's nothing to go back to. */
    fun back(): Boolean {
        error = null
        screen = when (val s = screen) {
            Screen.Home -> return false
            is Screen.Backup -> if (s.first) return true else Screen.Home
            is Screen.SendReview -> Screen.SendForm
            Screen.Working -> {
                shield.dismissWork()
                Screen.Home
            }
            else -> Screen.Home
        }
        return true
    }

    fun openRestore() {
        error = null
        screen = Screen.Restore
    }

    fun create() = launch {
        val words = shield.create()
        WatchService.start(app)
        screen = Screen.Backup(words, first = true)
    }

    fun restore() = launch {
        shield.restore(restoreWords)
        WatchService.start(app)
        restoreWords = ""
        screen = Screen.Home
    }

    fun showBackup() = launch { screen = Screen.Backup(shield.words(), first = false) }

    fun backedUp() {
        screen = Screen.Home
    }

    fun openSendOut() {
        error = null
        amount = ""
        recipient = ""
        symbol = null
        screen = Screen.SendForm
    }

    /** Asks the relayer what sending comes to. Amount empty means all of it. */
    fun review(symbol: String, decimals: Int) {
        val to = (Address.check(recipient) as? AddressCheck.Valid)?.checksummed
        if (to == null) {
            error = app.getString(R.string.send_bad_address)
            return
        }
        val units: BigInteger? = if (amount.isBlank()) null else when (val read = Amounts.read(amount, decimals)) {
            is AmountInput.Units -> read.units
            else -> {
                error = app.getString(R.string.send_bad_amount)
                return
            }
        }
        launch { screen = Screen.SendReview(shield.quoteOut(symbol, units, to), to) }
    }

    fun send(quote: Quote) {
        shield.sendOut(quote)
        screen = Screen.Working
    }

    fun openExit(symbol: String, decimals: Int, deposit: Deposit) = launch {
        recipient = ""
        screen = Screen.Exit(symbol, decimals, deposit, shield.exitPlan())
    }

    fun refreshExit(exit: Screen.Exit) = launch { screen = exit.copy(plan = shield.exitPlan()) }

    fun exit(exit: Screen.Exit) {
        val to = (Address.check(recipient) as? AddressCheck.Valid)?.checksummed
        if (to == null) {
            error = app.getString(R.string.send_bad_address)
            return
        }
        shield.exit(exit.symbol, exit.deposit.commitment, to)
        screen = Screen.Working
    }

    fun finish() {
        shield.dismissWork()
        screen = Screen.Home
    }

    fun qr(text: String): ImageBitmap {
        qrImage?.takeIf { qrFor == text }?.let { return it }
        val size = 512
        val matrix = QRCodeWriter().encode(text, BarcodeFormat.QR_CODE, size, size, mapOf(EncodeHintType.MARGIN to 1))
        val pixels = IntArray(size * size) { if (matrix.get(it % size, it / size)) 0xFF000000.toInt() else 0xFFFFFFFF.toInt() }
        return Bitmap.createBitmap(pixels, size, size, Bitmap.Config.RGB_565).asImageBitmap().also {
            qrFor = text
            qrImage = it
        }
    }

    fun show(units: BigInteger, decimals: Int, symbol: String) = "${Shield.format(units, decimals)} $symbol"

    private fun launch(block: suspend () -> Unit) {
        viewModelScope.launch {
            busy = true
            error = null
            try {
                block()
            } catch (failure: EngineException) {
                error = Errors.describe(app, failure.code, failure.message)
            } catch (failure: Exception) {
                error = failure.message ?: failure.javaClass.simpleName
            } finally {
                busy = false
            }
        }
    }
}
