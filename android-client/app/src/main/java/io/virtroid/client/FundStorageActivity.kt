package io.virtroid.client

import android.app.AlertDialog
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.text.InputType
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.updatePadding
import androidx.lifecycle.lifecycleScope
import io.virtroid.client.api.AccountStorage
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.SessionStore
import io.virtroid.client.databinding.ScreenFundStorageBinding
import io.virtroid.client.security.copySensitiveToClipboard
import io.virtroid.client.security.enableSecureWindow
import kotlinx.coroutines.launch
import java.util.Locale

class FundStorageActivity : AppCompatActivity() {
    private lateinit var binding: ScreenFundStorageBinding
    private lateinit var sessionStore: SessionStore
    private lateinit var appLogs: AppLogStore
    private val api = VirtroidApi()
    private var selectedAmountUsd = 10
    private var walletAddress: String = ""

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableSecureWindow()
        binding = ScreenFundStorageBinding.inflate(layoutInflater)
        setContentView(binding.root)
        WindowCompat.setDecorFitsSystemWindows(window, false)

        sessionStore = SessionStore(this)
        appLogs = AppLogStore.get(this)

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
        binding.buttonHelp.setOnClickListener { showFundingUnavailableDialog() }
        binding.buttonCopySiaAddress.setOnClickListener { copyToClipboard("Sia address", walletAddress) }
        binding.buttonAmount5.setOnClickListener { selectAmount(5) }
        binding.buttonAmount10.setOnClickListener { selectAmount(10) }
        binding.buttonAmount25.setOnClickListener { selectAmount(25) }
        binding.buttonAmountCustom.setOnClickListener { chooseCustomAmount() }
        binding.buttonCreateUsdtPayment.setOnClickListener { openPayment() }

        selectAmount(selectedAmountUsd)
        setQuoteActionEnabled(false)
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
                walletAddress = ""
                binding.walletStatusChip.text = getString(R.string.account_storage_unavailable)
                binding.siaAddressValue.text = getString(R.string.account_storage_unavailable)
                setQuoteActionEnabled(false)
                appLogs.warn("Storage wallet could not be loaded: ${it.message}", "storage")
            }
        }
    }

    private fun bindStorage(storage: AccountStorage) {
        walletAddress = storage.walletAddress?.takeIf { it.isNotBlank() }.orEmpty()
        binding.siaAddressValue.text = walletAddress.takeIf { it.isNotBlank() }?.let(::shortenAddress)
            ?: getString(R.string.fund_storage_wallet_not_configured)
        binding.walletStatusChip.text = when (storage.status) {
            "ready" -> "READY"
            "funding_required", "not_configured" -> getString(R.string.fund_storage_required)
            else -> storage.status.replace('_', ' ').uppercase(Locale.US)
        }
        setQuoteActionEnabled(false)
    }

    private fun selectAmount(amountUsd: Int) {
        selectedAmountUsd = amountUsd
        setAmountButton(binding.buttonAmount5, amountUsd == 5)
        setAmountButton(binding.buttonAmount10, amountUsd == 10)
        setAmountButton(binding.buttonAmount25, amountUsd == 25)
        binding.estimatedReceivedValue.text = "--"
        binding.networkFeeValue.text = "--"
    }

    private fun setAmountButton(view: TextView, selected: Boolean) {
        view.setBackgroundResource(if (selected) R.drawable.bg_amount_selected else R.drawable.bg_amount_unselected)
        view.setTextColor(getColor(if (selected) R.color.virtroid_on_primary else R.color.virtroid_on_surface_variant))
    }

    private fun openPayment() {
        showFundingUnavailableDialog()
    }

    private fun chooseCustomAmount() {
        val input = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(selectedAmountUsd.toString())
            selectAll()
        }
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.fund_storage_custom_amount_title))
            .setView(input)
            .setNegativeButton(getString(R.string.controls_cancel), null)
            .setPositiveButton(getString(R.string.controls_confirm)) { _, _ ->
                val amount = input.text.toString().toIntOrNull()?.coerceIn(1, 10_000)
                if (amount == null) {
                    toast(getString(R.string.fund_storage_custom_amount_invalid))
                } else {
                    selectAmount(amount)
                }
            }
            .show()
    }

    private fun setQuoteActionEnabled(enabled: Boolean) {
        binding.buttonCreateUsdtPayment.isEnabled = enabled
        binding.buttonCreateUsdtPayment.alpha = if (enabled) 1f else 0.55f
        binding.buttonCreateUsdtPayment.text = if (enabled) {
            getString(R.string.fund_storage_create_payment)
        } else {
            getString(R.string.fund_storage_payment_unavailable)
        }
    }

    private fun showFundingUnavailableDialog() {
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.fund_storage_payment_unavailable_title))
            .setMessage(getString(R.string.fund_storage_payment_unavailable_body))
            .setPositiveButton(android.R.string.ok, null)
            .show()
        appLogs.warn("USDT payment flow blocked because live quote backend is unavailable", "payment")
    }

    private fun shortenAddress(value: String): String {
        if (value.length <= 18) return value
        return value.take(5) + "..." + value.takeLast(14)
    }

    private fun copyToClipboard(label: String, value: String) {
        if (value.isBlank()) {
            toast(getString(R.string.fund_storage_wallet_not_configured))
            return
        }
        copySensitiveToClipboard(label, value)
        toast(getString(R.string.fund_storage_copied))
    }

    private fun toast(message: String) {
        Toast.makeText(this, message, Toast.LENGTH_SHORT).show()
    }

    companion object {
        fun createIntent(context: Context): Intent = Intent(context, FundStorageActivity::class.java)
    }
}
