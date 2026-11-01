// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import androidx.core.net.toUri
import androidx.core.widget.doAfterTextChanged
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.google.android.material.chip.Chip
import io.virtroid.shield.AccountState
import io.virtroid.shield.AssetInfo
import io.virtroid.shield.Quote
import io.virtroid.shield.R
import io.virtroid.shield.Shield
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.databinding.ActivityActionBinding
import io.virtroid.shield.engine.EngineException
import kotlinx.coroutines.launch
import java.math.BigInteger

/**
 * Shield, send privately, or withdraw. A send or withdrawal is quoted first
 * (the relayer's fee, what arrives), and only goes out when the user
 * confirms the quote; a shield goes straight to the wallet, which shows the
 * user exactly what it's about to sign.
 */
class ActionActivity : AppCompatActivity() {
    enum class Mode { SHIELD, SEND, WITHDRAW }

    private lateinit var binding: ActivityActionBinding
    private val app get() = application as ShieldApp
    private val shield get() = app.shield
    private lateinit var mode: Mode
    private var assets: List<AssetInfo> = emptyList()
    private var selected: AssetInfo? = null
    private var quote: Quote? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityActionBinding.inflate(layoutInflater)
        setContentView(binding.root)
        mode = Mode.valueOf(intent.getStringExtra(EXTRA_MODE) ?: Mode.SHIELD.name)

        binding.title.setText(
            when (mode) {
                Mode.SHIELD -> R.string.action_shield_title
                Mode.SEND -> R.string.action_send_title
                Mode.WITHDRAW -> R.string.action_withdraw_title
            },
        )
        binding.explain.setText(
            when (mode) {
                Mode.SHIELD -> R.string.action_shield_explain
                Mode.SEND -> R.string.action_send_explain
                Mode.WITHDRAW -> R.string.action_withdraw_explain
            },
        )
        binding.recipientLayout.visibility = if (mode == Mode.SHIELD) View.GONE else View.VISIBLE
        binding.maxButton.visibility = if (mode == Mode.SHIELD) View.GONE else View.VISIBLE
        binding.maxButton.setOnClickListener { binding.amount.setText(getString(R.string.action_max_value)) }
        binding.amount.doAfterTextChanged { clearQuote() }
        binding.recipient.doAfterTextChanged { clearQuote() }
        binding.closeButton.setOnClickListener { finish() }
        binding.primaryButton.setOnClickListener { primary() }

        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                Progress.follow(this@ActionActivity, app, binding.progressText)
            }
        }
        lifecycleScope.launch { loadAssets() }
    }

    private suspend fun loadAssets() {
        val state = shield.state.value as? AccountState.Ready ?: run {
            binding.progressText.setText(R.string.error_not_ready)
            binding.progressText.visibility = View.VISIBLE
            binding.primaryButton.isEnabled = false
            return
        }
        val chain = shield.chain()
        binding.chainText.text = chain.name
        assets = if (mode == Mode.SHIELD) chain.assets
        else chain.assets.filter { a -> state.balances.any { it.symbol == a.symbol && it.spendable.signum() > 0 } }
        binding.assets.removeAllViews()
        for (asset in assets) {
            val chip = Chip(this).apply {
                text = asset.symbol
                isCheckable = true
                setOnClickListener {
                    selected = asset
                    clearQuote()
                    showAvailable(state)
                }
            }
            binding.assets.addView(chip)
        }
        (binding.assets.getChildAt(0) as? Chip)?.performClick()
        if (assets.isEmpty()) {
            binding.progressText.setText(R.string.error_nothing_spendable)
            binding.progressText.visibility = View.VISIBLE
            binding.primaryButton.isEnabled = false
        }
    }

    private fun showAvailable(state: AccountState.Ready) {
        val asset = selected ?: return
        val balance = state.balances.firstOrNull { it.symbol == asset.symbol }
        binding.available.text = if (mode == Mode.SHIELD || balance == null) ""
        else getString(R.string.action_available, Shield.format(balance.maxSingle, asset.decimals), asset.symbol)
    }

    private fun primary() {
        val asset = selected ?: return
        val text = binding.amount.text?.toString().orEmpty()
        val max = mode != Mode.SHIELD && text.trim().equals(getString(R.string.action_max_value), ignoreCase = true)
        val amount: BigInteger? = if (max) null else Shield.parse(text, asset.decimals) ?: run {
            binding.amountLayout.error = getString(R.string.error_amount)
            return
        }
        binding.amountLayout.error = null
        val recipient = binding.recipient.text?.toString()?.trim().orEmpty()
        if (mode != Mode.SHIELD && !ADDRESS.matches(recipient)) {
            binding.recipientLayout.error = getString(R.string.error_address)
            return
        }
        binding.recipientLayout.error = null

        lifecycleScope.launch {
            busy(true)
            try {
                when {
                    mode == Mode.SHIELD -> {
                        shield.shield(asset.symbol, amount!!)
                        done(getString(R.string.action_shield_done))
                    }
                    quote == null -> {
                        val q = if (mode == Mode.SEND) shield.quoteSend(asset.symbol, amount, recipient)
                        else shield.quoteWithdraw(asset.symbol, amount, recipient)
                        quote = q
                        binding.quoteText.text = getString(
                            R.string.action_quote,
                            Shield.format(q.receive, asset.decimals), asset.symbol,
                            Shield.format(q.fee, asset.decimals), asset.symbol,
                        )
                        binding.quoteText.visibility = View.VISIBLE
                        binding.primaryButton.setText(R.string.action_confirm)
                    }
                    else -> {
                        val tx = shield.confirm()
                        done(getString(if (mode == Mode.SEND) R.string.action_send_done else R.string.action_withdraw_done), tx)
                    }
                }
            } catch (error: EngineException) {
                binding.progressText.text = Errors.describe(this@ActionActivity, error)
                binding.progressText.visibility = View.VISIBLE
                clearQuote()
            } catch (error: Exception) {
                binding.progressText.text = error.message ?: error.javaClass.simpleName
                binding.progressText.visibility = View.VISIBLE
                clearQuote()
            } finally {
                busy(false)
            }
        }
    }

    private suspend fun done(message: String, txHash: String? = null) {
        binding.form.visibility = View.GONE
        binding.progressText.visibility = View.VISIBLE
        binding.progressText.text = message
        if (txHash != null) {
            val explorer = shield.chain().explorerUrl
            if (explorer.isNotEmpty()) {
                binding.explorerButton.visibility = View.VISIBLE
                binding.explorerButton.setOnClickListener {
                    startActivity(Intent(Intent.ACTION_VIEW, "$explorer/tx/$txHash".toUri()))
                }
            }
        }
        binding.primaryButton.setText(R.string.action_done)
        binding.primaryButton.setOnClickListener { finish() }
    }

    private fun clearQuote() {
        if (quote == null) return
        quote = null
        binding.quoteText.visibility = View.GONE
        binding.primaryButton.setText(R.string.action_review)
    }

    private fun busy(on: Boolean) {
        binding.primaryButton.isEnabled = !on
        binding.progress.visibility = if (on) View.VISIBLE else View.GONE
        if (on) binding.progressText.visibility = View.VISIBLE
    }

    companion object {
        private const val EXTRA_MODE = "mode"
        private val ADDRESS = Regex("^0x[0-9a-fA-F]{40}$")

        fun intent(context: Context, mode: Mode): Intent = Intent(context, ActionActivity::class.java).putExtra(EXTRA_MODE, mode.name)
    }
}
