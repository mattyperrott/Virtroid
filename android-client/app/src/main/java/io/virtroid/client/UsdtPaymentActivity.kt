package io.virtroid.client

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.os.CountDownTimer
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.isVisible
import androidx.core.view.updatePadding
import io.virtroid.client.databinding.ScreenSendUsdtBinding
import io.virtroid.client.security.copySensitiveToClipboard
import io.virtroid.client.security.enableSecureWindow
import java.text.NumberFormat
import java.util.Locale

class UsdtPaymentActivity : AppCompatActivity() {
    private lateinit var binding: ScreenSendUsdtBinding
    private var timer: CountDownTimer? = null
    private val scFormatter = NumberFormat.getIntegerInstance(Locale.US)

    private val amountUsdt: Double by lazy { intent.getDoubleExtra(EXTRA_AMOUNT_USDT, 10.0) }
    private val network: String by lazy { intent.getStringExtra(EXTRA_NETWORK).orEmpty().ifBlank { "USDT TRC20" } }
    private val depositAddress: String by lazy { intent.getStringExtra(EXTRA_DEPOSIT_ADDRESS).orEmpty() }
    private val siaAddress: String by lazy { intent.getStringExtra(EXTRA_SIA_ADDRESS).orEmpty() }
    private val estimatedSc: Int by lazy { intent.getIntExtra(EXTRA_ESTIMATED_SC, 1180) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenSendUsdtBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        ViewCompat.setOnApplyWindowInsetsListener(binding.topNav) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 20 + bars.top,
                right = 24 + bars.right,
                bottom = 12,
            )
            insets
        }
        ViewCompat.setOnApplyWindowInsetsListener(binding.bottomActions) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 20,
                right = 24 + bars.right,
                bottom = 24 + bars.bottom,
            )
            insets
        }

        binding.buttonBack.setOnClickListener { finish() }
        binding.networkValue.text = network
        binding.amountValue.text = String.format(Locale.US, "%.6f USDT", amountUsdt)
        binding.depositAddressValue.text = depositAddress.ifBlank { getString(R.string.payment_quote_unavailable) }
        binding.destinationWalletValue.text = siaAddress.ifBlank { "--" }
        binding.estimatedReceiptValue.text = "~${scFormatter.format(estimatedSc)} SC"
        val hasLiveQuote = depositAddress.isNotBlank()
        binding.paymentStatusTitle.text = if (hasLiveQuote) {
            getString(R.string.payment_waiting)
        } else {
            getString(R.string.payment_quote_unavailable)
        }
        binding.paymentWaitingDot.isVisible = hasLiveQuote
        binding.buttonPaymentSent.isEnabled = hasLiveQuote
        binding.buttonCopyDepositAddress.isEnabled = hasLiveQuote
        binding.buttonShowQr.isEnabled = false
        binding.buttonShowQr.alpha = 0.45f

        binding.buttonCopyAmount.setOnClickListener {
            copyToClipboard("USDT amount", String.format(Locale.US, "%.6f", amountUsdt))
        }
        binding.buttonCopyDepositAddress.setOnClickListener {
            copyToClipboard("USDT deposit address", depositAddress)
        }
        binding.buttonPaymentSent.setOnClickListener {
            toast(getString(R.string.payment_listening))
        }
        binding.buttonCancelQuote.setOnClickListener {
            toast(getString(R.string.payment_cancelled))
            finish()
        }

        if (hasLiveQuote) {
            startTimer()
        } else {
            binding.paymentTimer.text = "--:--"
        }
    }

    override fun onDestroy() {
        timer?.cancel()
        super.onDestroy()
    }

    private fun startTimer() {
        timer?.cancel()
        timer = object : CountDownTimer(QUOTE_DURATION_MS, 1_000L) {
            override fun onTick(millisUntilFinished: Long) {
                val totalSeconds = (millisUntilFinished / 1_000L).coerceAtLeast(0)
                val minutes = totalSeconds / 60
                val seconds = totalSeconds % 60
                binding.paymentTimer.text = String.format(Locale.US, "%02d:%02d", minutes, seconds)
            }

            override fun onFinish() {
                binding.paymentTimer.text = "00:00"
                binding.buttonPaymentSent.isEnabled = false
                toast(getString(R.string.payment_expired))
            }
        }.start()
    }

    private fun copyToClipboard(label: String, value: String) {
        if (value.isBlank()) {
            toast(getString(R.string.payment_quote_unavailable))
            return
        }
        copySensitiveToClipboard(label, value)
        toast(getString(R.string.fund_storage_copied))
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        private const val QUOTE_DURATION_MS = 15 * 60 * 1_000L
        private const val EXTRA_AMOUNT_USDT = "amount_usdt"
        private const val EXTRA_NETWORK = "network"
        private const val EXTRA_DEPOSIT_ADDRESS = "deposit_address"
        private const val EXTRA_SIA_ADDRESS = "sia_address"
        private const val EXTRA_ESTIMATED_SC = "estimated_sc"

        fun createIntent(
            context: Context,
            amountUsdt: Double,
            network: String,
            depositAddress: String,
            siaAddress: String,
            estimatedSc: Int,
        ): Intent {
            return Intent(context, UsdtPaymentActivity::class.java)
                .putExtra(EXTRA_AMOUNT_USDT, amountUsdt)
                .putExtra(EXTRA_NETWORK, network)
                .putExtra(EXTRA_DEPOSIT_ADDRESS, depositAddress)
                .putExtra(EXTRA_SIA_ADDRESS, siaAddress)
                .putExtra(EXTRA_ESTIMATED_SC, estimatedSc)
        }
    }
}
