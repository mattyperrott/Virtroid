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
        binding.buttonCreateUsdtPayment.setOnClickListener { copyToClipboard("Sia deposit address", walletAddress) }

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
        walletAddress = storage.fundingAddress?.takeIf { it.isNotBlank() }
            ?: storage.walletAddress?.takeIf { it.isNotBlank() }
            ?: ""
        binding.siaAddressValue.text = walletAddress.takeIf { it.isNotBlank() }?.let(::shortenAddress)
            ?: getString(R.string.fund_storage_wallet_not_configured)
        binding.walletStatusChip.text = when (storage.status) {
            "ready" -> "READY"
            "funding_required", "not_configured" -> getString(R.string.fund_storage_required)
            else -> storage.status.replace('_', ' ').uppercase(Locale.US)
        }
        binding.networkFeeValue.text = storage.lastPreflightStatus
            ?.replace('_', ' ')
            ?.uppercase(Locale.US)
            ?: "--"
        binding.contractMinimumValue.text = when (storage.status) {
            "ready" -> "Contracts active"
            "contracts_required" -> "Contracts needed"
            "funding_required" -> "SC funding needed"
            "syncing" -> "Consensus syncing"
            else -> "--"
        }
        bindDeploymentStatus(storage)
        setQuoteActionEnabled(walletAddress.isNotBlank())
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
            getString(R.string.fund_storage_copy_deposit_address)
        } else {
            getString(R.string.fund_storage_wallet_not_configured)
        }
    }

    private fun showFundingUnavailableDialog() {
        AlertDialog.Builder(this)
            .setTitle(getString(R.string.fund_storage_direct_sia_title))
            .setMessage(getString(R.string.fund_storage_direct_sia_body))
            .setPositiveButton(android.R.string.ok, null)
            .show()
    }

    private fun bindDeploymentStatus(storage: AccountStorage) {
        val status = storage.lastPreflightStatus?.ifBlank { null } ?: storage.status
        val walletReady = walletAddress.isNotBlank()
        val funded = status == "ready" || status == "contracts_required"
        val contractsReady = status == "ready"

        setStep(
            dot = binding.stepCreateQuoteDot,
            label = binding.stepCreateQuoteLabel,
            text = if (walletReady) getString(R.string.fund_storage_step_wallet_address_ready) else getString(R.string.fund_storage_step_wallet_address_missing),
            complete = walletReady,
            active = !walletReady,
        )
        setStep(
            dot = binding.stepSendUsdtDot,
            label = binding.stepSendUsdtLabel,
            text = if (funded) getString(R.string.fund_storage_step_funding_detected) else getString(R.string.fund_storage_step_waiting_siacoin),
            complete = funded,
            active = walletReady && !funded,
        )
        setStep(
            dot = binding.stepReceiveSiacoinDot,
            label = binding.stepReceiveSiacoinLabel,
            text = when (status) {
                "syncing" -> getString(R.string.fund_storage_step_consensus_syncing)
                "ready", "contracts_required" -> getString(R.string.fund_storage_step_renterd_reachable)
                else -> getString(R.string.fund_storage_step_renterd_waiting)
            },
            complete = status == "ready" || status == "contracts_required",
            active = status == "syncing",
        )
        setStep(
            dot = binding.stepFormContractsDot,
            label = binding.stepFormContractsLabel,
            text = if (contractsReady) getString(R.string.fund_storage_step_contracts_ready) else getString(R.string.fund_storage_step_contracts_required),
            complete = contractsReady,
            active = status == "contracts_required",
        )
        setStep(
            dot = binding.stepStorageReadyDot,
            label = binding.stepStorageReadyLabel,
            text = if (contractsReady) getString(R.string.fund_storage_step_ready) else getString(R.string.fund_storage_step_waiting_ready),
            complete = contractsReady,
            active = false,
        )
    }

    private fun setStep(
        dot: android.view.View,
        label: TextView,
        text: String,
        complete: Boolean,
        active: Boolean,
    ) {
        dot.setBackgroundResource(
            when {
                complete -> R.drawable.bg_dot_accent
                active -> R.drawable.bg_dot_amber_outline
                else -> R.drawable.bg_dot_muted_outline
            },
        )
        label.text = text
        label.alpha = if (complete || active) 1f else 0.45f
        label.setTextColor(getColor(if (active) R.color.v_amber else R.color.v_text_primary))
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
