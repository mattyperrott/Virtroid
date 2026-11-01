// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import android.content.Intent
import android.os.Bundle
import android.view.LayoutInflater
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import androidx.appcompat.widget.PopupMenu
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import io.virtroid.shield.AccountState
import io.virtroid.shield.Balance
import io.virtroid.shield.R
import io.virtroid.shield.Shield
import io.virtroid.shield.ShieldApp
import io.virtroid.shield.databinding.ActivityMainBinding
import io.virtroid.shield.databinding.RowBalanceBinding
import io.virtroid.shield.engine.EngineException
import io.virtroid.shield.widget.ShieldWidget
import kotlinx.coroutines.launch

/** The account: which wallet, which chain, what's shielded, and the three actions. */
class MainActivity : AppCompatActivity() {
    private lateinit var binding: ActivityMainBinding
    private val app get() = application as ShieldApp
    private val shield get() = app.shield

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        binding.connectButton.setOnClickListener { startActivity(Intent(this, ConnectActivity::class.java)) }
        binding.setupButton.setOnClickListener { run(getString(R.string.progress_setting_up)) { shield.setUp((shield.state.value as AccountState.NeedsSetup).owner) } }
        binding.refreshButton.setOnClickListener { run(getString(R.string.progress_syncing)) { shield.refresh() } }
        binding.chainButton.setOnClickListener { pickChain() }
        binding.shieldButton.setOnClickListener { startActivity(ActionActivity.intent(this, ActionActivity.Mode.SHIELD)) }
        binding.sendButton.setOnClickListener { startActivity(ActionActivity.intent(this, ActionActivity.Mode.SEND)) }
        binding.withdrawButton.setOnClickListener { startActivity(ActionActivity.intent(this, ActionActivity.Mode.WITHDRAW)) }
        binding.disconnectButton.setOnClickListener { lifecycleScope.launch { app.walletConnect.disconnect(); shield.load() } }
        binding.widgetBalances.isChecked = ShieldWidget.showBalances(this)
        binding.widgetBalances.setOnCheckedChangeListener { _, checked -> ShieldWidget.setShowBalances(this, checked) }
        binding.aboutButton.setOnClickListener { startActivity(Intent(this, AboutActivity::class.java)) }

        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                launch { shield.state.collect(::render) }
                launch { app.walletConnect.session.collect { render(shield.state.value) } }
                launch { Progress.follow(this@MainActivity, app, binding.progressText) }
            }
        }
    }

    override fun onStart() {
        super.onStart()
        if (!app.walletConnect.configured && app.localTest == null) {
            binding.statusText.setText(R.string.not_configured)
            return
        }
        run(getString(R.string.progress_syncing)) { shield.load() }
    }

    private fun render(state: AccountState) {
        val session = app.walletConnect.session.value
        val test = app.localTest
        binding.walletText.text = when {
            test?.owner != null -> getString(R.string.wallet_connected, getString(R.string.wallet_test), short(test.owner))
            session != null -> getString(R.string.wallet_connected, session.walletName.ifEmpty { getString(R.string.wallet_unnamed) }, short(session.addresses.firstOrNull()))
            else -> getString(R.string.wallet_none)
        }
        binding.connectButton.visibility = if (session == null && test?.owner == null) View.VISIBLE else View.GONE
        binding.disconnectButton.visibility = if (session != null) View.VISIBLE else View.GONE
        binding.setupGroup.visibility = if (state is AccountState.NeedsSetup) View.VISIBLE else View.GONE
        binding.readyGroup.visibility = if (state is AccountState.Ready) View.VISIBLE else View.GONE
        lifecycleScope.launch { binding.chainButton.text = shield.chain().name }
        if (state is AccountState.Ready) renderBalances(state.balances)
    }

    private fun renderBalances(balances: List<Balance>) {
        binding.balances.removeAllViews()
        for (b in balances) {
            val row = RowBalanceBinding.inflate(LayoutInflater.from(this), binding.balances, true)
            row.symbol.text = b.symbol
            row.spendable.text = Shield.format(b.spendable, b.decimals)
            val notes = buildList {
                if (b.pending.signum() > 0) add(getString(R.string.balance_waiting, Shield.format(b.pending, b.decimals)))
                if (b.maxSingle < b.spendable) add(getString(R.string.balance_max_single, Shield.format(b.maxSingle, b.decimals)))
            }
            row.notes.text = notes.joinToString("\n")
            row.notes.visibility = if (notes.isEmpty()) View.GONE else View.VISIBLE
            for (declined in b.declined) {
                row.declined.visibility = View.VISIBLE
                row.declined.text = getString(R.string.balance_declined, Shield.format(declined.value, b.decimals), b.symbol)
                row.declined.setOnClickListener { confirmExit(declined.commitment) }
            }
        }
    }

    private fun confirmExit(commitment: String) {
        MaterialAlertDialogBuilder(this)
            .setTitle(R.string.exit_title)
            .setMessage(R.string.exit_message)
            .setPositiveButton(R.string.exit_confirm) { _, _ -> run(getString(R.string.progress_exiting)) { shield.exit(commitment) } }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun pickChain() {
        lifecycleScope.launch {
            val chains = shield.chains()
            PopupMenu(this@MainActivity, binding.chainButton).apply {
                chains.forEachIndexed { i, c -> menu.add(0, i, i, c.name) }
                setOnMenuItemClickListener { item ->
                    shield.chainId = chains[item.itemId].chainId
                    run(getString(R.string.progress_syncing)) { shield.load() }
                    true
                }
            }.show()
        }
    }

    private fun run(label: String, block: suspend () -> Any?) {
        lifecycleScope.launch {
            binding.progressText.text = label
            binding.progressText.visibility = View.VISIBLE
            try {
                block()
                binding.progressText.visibility = View.GONE
            } catch (error: EngineException) {
                binding.progressText.text = Errors.describe(this@MainActivity, error)
            } catch (error: Exception) {
                binding.progressText.text = error.message ?: error.javaClass.simpleName
            }
        }
    }

    private fun short(address: String?) = address?.let { "${it.take(6)}…${it.takeLast(4)}" } ?: ""
}
