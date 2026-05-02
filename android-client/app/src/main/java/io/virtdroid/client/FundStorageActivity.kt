package io.virtdroid.client

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtdroid.client.api.AccountStorage
import io.virtdroid.client.api.VirtdroidApi
import io.virtdroid.client.data.SessionStore
import io.virtdroid.client.databinding.ScreenFundStorageBinding
import io.virtdroid.client.security.enableSecureWindow
import kotlinx.coroutines.launch
import java.text.NumberFormat
import java.util.Locale
import kotlin.math.roundToInt

class FundStorageActivity : AppCompatActivity() {
    private lateinit var binding: ScreenFundStorageBinding
    private lateinit var sessionStore: SessionStore
    private val api = VirtdroidApi()
    private var selectedAmountUsd = 10
    private var walletAddress: String = FALLBACK_SIA_ADDRESS
    private val scFormatter = NumberFormat.getIntegerInstance(Locale.US)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenFundStorageBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)

        ViewCompat.setOnApplyWindowInsetsListener(binding.topNav) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 20 + bars.top,
                right = 24 + bars.right,
                bottom = 8,
            )
            insets
        }
        ViewCompat.setOnApplyWindowInsetsListener(binding.bottomAction) { view, insets ->
            val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars())
            view.updatePadding(
                left = 24 + bars.left,
                top = 20,
                right = 24 + bars.right,
                bottom = 32 + bars.bottom,
            )
            insets
        }

        binding.buttonBack.setOnClickListener { finish() }
        binding.buttonHelp.setOnClickListener {
            toast("USDT is swapped externally into SC and sent to your Sia wallet.")
        }
        binding.buttonCopySiaAddress.setOnClickListener { copyToClipboard("Sia address", walletAddress) }
        binding.buttonAmount5.setOnClickListener { selectAmount(5) }
        binding.buttonAmount10.setOnClickListener { selectAmount(10) }
        binding.buttonAmount25.setOnClickListener { selectAmount(25) }
        binding.buttonAmountCustom.setOnClickListener { toast(getString(R.string.fund_storage_custom)) }
        binding.buttonCreateUsdtPayment.setOnClickListener { openPayment() }

        selectAmount(selectedAmountUsd)
        loadStorage()
    }

    private fun loadStorage() {
        val accountId = sessionStore.accountId
        val deviceId = sessionStore.deviceId
        if (accountId.isNullOrBlank() || deviceId.isNullOrBlank()) {
            toast(getString(R.string.account_missing))
            finish()
            return
        }

        lifecycleScope.launch {
            runCatching {
                api.getStorage(sessionStore.baseUrl, accountId, deviceId)
            }.onSuccess { storage ->
                bindStorage(storage)
            }.onFailure {
                binding.walletStatusChip.text = getString(R.string.fund_storage_required)
                binding.siaAddressValue.text = shortenAddress(walletAddress)
            }
        }
    }

    private fun bindStorage(storage: AccountStorage) {
        walletAddress = storage.walletAddress?.takeIf { it.isNotBlank() } ?: FALLBACK_SIA_ADDRESS
        binding.siaAddressValue.text = shortenAddress(walletAddress)
        binding.walletStatusChip.text = when (storage.status) {
            "ready" -> "READY"
            "funding_required", "not_configured" -> getString(R.string.fund_storage_required)
            else -> storage.status.replace('_', ' ').uppercase(Locale.US)
        }
    }

    private fun selectAmount(amountUsd: Int) {
        selectedAmountUsd = amountUsd
        setAmountButton(binding.buttonAmount5, amountUsd == 5)
        setAmountButton(binding.buttonAmount10, amountUsd == 10)
        setAmountButton(binding.buttonAmount25, amountUsd == 25)
        binding.estimatedReceivedValue.text = "~${scFormatter.format(estimateSc(amountUsd))} SC"
        binding.networkFeeValue.text = "~$${"%.2f".format(Locale.US, estimateFee(amountUsd))}"
    }

    private fun setAmountButton(view: TextView, selected: Boolean) {
        view.setBackgroundResource(if (selected) R.drawable.bg_amount_selected else R.drawable.bg_amount_unselected)
        view.setTextColor(getColor(if (selected) R.color.virtdroid_on_primary else R.color.virtdroid_on_surface_variant))
    }

    private fun openPayment() {
        startActivity(
            UsdtPaymentActivity.createIntent(
                context = this,
                amountUsdt = selectedAmountUsd.toDouble(),
                network = "USDT TRC20",
                depositAddress = SAMPLE_USDT_TRC20_ADDRESS,
                siaAddress = walletAddress,
                estimatedSc = estimateSc(selectedAmountUsd),
            ),
        )
    }

    private fun estimateSc(amountUsd: Int): Int {
        return (amountUsd * 118.0).roundToInt()
    }

    private fun estimateFee(amountUsd: Int): Double {
        return maxOf(0.45, amountUsd * 0.045)
    }

    private fun shortenAddress(value: String): String {
        if (value.length <= 18) return value
        return value.take(5) + "..." + value.takeLast(14)
    }

    private fun copyToClipboard(label: String, value: String) {
        val clipboard = getSystemService(ClipboardManager::class.java)
        clipboard.setPrimaryClip(ClipData.newPlainText(label, value))
        toast(getString(R.string.fund_storage_copied))
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        private const val FALLBACK_SIA_ADDRESS = "c3b91d50cf58b0b5a312dd2d8f2a1b4c7d9e0f"
        private const val SAMPLE_USDT_TRC20_ADDRESS = "TNP12w3zKVm1M38f2vA8X4m1z6UaN6cQ22"

        fun createIntent(context: Context): Intent = Intent(context, FundStorageActivity::class.java)
    }
}
