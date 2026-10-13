package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.os.Bundle
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.api.VirtroidApiException
import io.virtroid.client.api.WalletEntry
import io.virtroid.client.api.WalletSummary
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenWalletBinding
import io.virtroid.client.security.copySensitiveToClipboard
import io.virtroid.client.security.enableSecureWindow
import io.virtroid.client.ui.Notice
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.math.BigDecimal
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/** The account's USDT balance, its TRC20 deposit address and recent activity. */
class WalletActivity : AppCompatActivity() {
    private lateinit var binding: ScreenWalletBinding
    private lateinit var sessionStore: SessionStore
    private val api = VirtroidApi()
    private var shownAddress: String? = null
    private val timeFormatter = DateTimeFormatter.ofPattern("d MMM, HH:mm").withZone(ZoneId.systemDefault())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenWalletBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        sessionStore = SessionStore(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.walletRoot) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(top = bars.top, bottom = bars.bottom)
            insets
        }
        binding.walletBackButton.setOnClickListener { finish() }
        binding.walletRefreshButton.setOnClickListener { lifecycleScope.launch { refresh() } }
        binding.walletCopyButton.setOnClickListener { copyAddress() }

        // While the wallet is open a deposit shows up without a manual refresh.
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                while (true) {
                    refresh()
                    delay(REFRESH_INTERVAL_MS)
                }
            }
        }
    }

    private suspend fun refresh() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            finish()
            return
        }
        runCatching { api.getWallet(sessionStore.baseUrl, accountId, deviceId) }
            .onSuccess { render(it) }
            .onFailure { error ->
                val message = if (error is VirtroidApiException && error.code == "wallet_unavailable") {
                    getString(R.string.wallet_unavailable)
                } else {
                    getString(R.string.wallet_load_failed, error.virtroidDisplayMessage(this))
                }
                binding.walletStatusText.text = message
                binding.walletStatusText.visibility = View.VISIBLE
            }
    }

    private suspend fun render(wallet: WalletSummary) {
        binding.walletStatusText.visibility = View.GONE
        binding.walletBalanceText.text = getString(R.string.wallet_amount, formatUsdt(wallet.balanceMicro))
        val pricing = buildList {
            if (wallet.runtimePricePerHourMicro > 0) {
                add(
                    getString(
                        R.string.wallet_pricing,
                        formatUsdt(wallet.runtimePricePerHourMicro),
                        formatUsdt(wallet.storagePricePerGibMonthMicro),
                    ),
                )
            }
            if (wallet.deposits.isEmpty()) {
                add(getString(R.string.wallet_trial_note))
            }
        }
        binding.walletPricingText.text = pricing.joinToString("\n\n")
        binding.walletPricingText.visibility = if (pricing.isEmpty()) View.GONE else View.VISIBLE
        binding.walletDepositCard.visibility = View.VISIBLE
        if (wallet.depositAddress != shownAddress) {
            shownAddress = wallet.depositAddress
            binding.walletAddressText.text = wallet.depositAddress
            binding.walletQrImage.setImageBitmap(withContext(Dispatchers.Default) { qrCode(wallet.depositAddress) })
        }
        renderHistory(wallet.entries)
    }

    private fun renderHistory(entries: List<WalletEntry>) {
        val container = binding.walletHistoryContainer
        container.removeAllViews()
        if (entries.isEmpty()) {
            container.addView(historyText(getString(R.string.wallet_history_empty), R.color.v_text_muted))
            return
        }
        entries.forEach { entry ->
            val row = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                setPadding(0, dp(10), 0, dp(10))
            }
            val label = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
            label.addView(historyText(kindLabel(entry.kind), R.color.v_text_primary))
            label.addView(historyText(formatTime(entry.createdAt), R.color.v_text_muted).apply { textSize = 12f })
            row.addView(label, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
            val amount = (if (entry.amountMicro > 0) "+" else "") + formatUsdt(entry.amountMicro)
            row.addView(historyText(amount, if (entry.amountMicro > 0) R.color.v_accent else R.color.v_text_primary))
            container.addView(row)
        }
    }

    private fun historyText(text: String, colorRes: Int): TextView =
        TextView(this).apply {
            this.text = text
            textSize = 14f
            setTextColor(getColor(colorRes))
        }

    private fun kindLabel(kind: String): String =
        getString(
            when (kind) {
                "deposit" -> R.string.wallet_kind_deposit
                "runtime" -> R.string.wallet_kind_runtime
                "storage" -> R.string.wallet_kind_storage
                else -> R.string.wallet_kind_adjustment
            },
        )

    private fun formatTime(raw: String): String =
        runCatching { timeFormatter.format(Instant.parse(raw)) }.getOrDefault("")

    private fun copyAddress() {
        val address = shownAddress ?: return
        val systemConfirms = copySensitiveToClipboard("USDT deposit address", address, clearAfterMs = ADDRESS_CLIPBOARD_MS)
        if (!systemConfirms) {
            Notice.show(this, getString(R.string.wallet_copy_address))
        }
    }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

    companion object {
        private const val REFRESH_INTERVAL_MS = 15_000L
        // Long enough to switch to another app and paste.
        private const val ADDRESS_CLIPBOARD_MS = 120_000L
        private const val QR_SIZE_PX = 512

        fun createIntent(context: Context): Intent = Intent(context, WalletActivity::class.java)

        /** USDT from millionths: at least two decimals, up to six. */
        internal fun formatUsdt(micro: Long): String {
            val value = BigDecimal.valueOf(micro, 6).stripTrailingZeros()
            return if (value.scale() < 2) value.setScale(2).toPlainString() else value.toPlainString()
        }

        private fun qrCode(text: String): Bitmap {
            val matrix = QRCodeWriter().encode(
                text,
                BarcodeFormat.QR_CODE,
                QR_SIZE_PX,
                QR_SIZE_PX,
                mapOf(EncodeHintType.MARGIN to 1),
            )
            val pixels = IntArray(matrix.width * matrix.height) { index ->
                if (matrix.get(index % matrix.width, index / matrix.width)) Color.BLACK else Color.WHITE
            }
            return Bitmap.createBitmap(pixels, matrix.width, matrix.height, Bitmap.Config.RGB_565)
        }
    }
}
