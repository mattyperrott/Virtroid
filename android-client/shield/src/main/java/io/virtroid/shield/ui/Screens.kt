// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CheckboxDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.FilterChip
import androidx.compose.material3.FilterChipDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.virtroid.shield.AccountState
import io.virtroid.shield.Balance
import io.virtroid.shield.Incoming
import io.virtroid.shield.R
import io.virtroid.shield.Shield
import io.virtroid.shield.widget.ShieldWidget

@Composable
fun Screens(model: ShieldModel, copy: (String) -> Unit, paste: () -> String?, openAbout: () -> Unit) {
    val state by model.shield.state.collectAsState()
    Box(Modifier.fillMaxSize().background(Palette.Outer).safeDrawingPadding()) {
        when (val s = model.screen) {
            Screen.Home -> when (val account = state) {
                AccountState.Loading -> Frame { Busy(null) }
                AccountState.NoWallet -> Welcome(model)
                is AccountState.Ready -> Home(model, account, copy, openAbout)
            }
            Screen.Restore -> Restore(model)
            is Screen.Backup -> Backup(model, s)
            Screen.SendForm -> (state as? AccountState.Ready)?.let { SendForm(model, it, paste) }
            is Screen.SendReview -> SendReview(model, s)
            Screen.Working -> Working(model, copy)
            is Screen.Exit -> Exit(model, s, copy, paste)
        }
    }
}

// --- set-up ----------------------------------------------------------------------

@Composable
private fun Welcome(model: ShieldModel) {
    Frame(
        action = {
            LimeButton(stringResource(R.string.setup_create), onClick = model::create, enabled = !model.busy)
            Spacer(Modifier.height(10.dp))
            QuietButton(stringResource(R.string.setup_restore), onClick = model::openRestore, enabled = !model.busy)
        },
    ) {
        Title(stringResource(R.string.app_name))
        Spacer(Modifier.height(8.dp))
        Body(stringResource(R.string.setup_explain))
        ErrorLine(model.error)
    }
}

@Composable
private fun Restore(model: ShieldModel) {
    Frame(
        action = { LimeButton(stringResource(R.string.restore_action), onClick = model::restore, enabled = !model.busy && model.restoreWords.isNotBlank()) },
    ) {
        BackRow(model)
        Title(stringResource(R.string.restore_title))
        Spacer(Modifier.height(8.dp))
        Body(stringResource(R.string.restore_explain))
        Spacer(Modifier.height(14.dp))
        Field(model.restoreWords, { model.restoreWords = it }, stringResource(R.string.restore_hint), singleLine = false)
        if (model.busy) Busy(stringResource(R.string.progress_syncing))
        ErrorLine(model.error)
    }
}

@Composable
private fun Backup(model: ShieldModel, backup: Screen.Backup) {
    var saved by remember { mutableStateOf(false) }
    Frame(
        action = {
            if (backup.first) {
                Row(
                    Modifier.fillMaxWidth().clickable { saved = !saved }.padding(vertical = 4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Checkbox(saved, { saved = it }, colors = CheckboxDefaults.colors(checkedColor = Palette.Lime, checkmarkColor = Palette.OnLime))
                    Text(stringResource(R.string.backup_saved), style = MaterialTheme.typography.bodyLarge, color = Palette.Text)
                }
                Spacer(Modifier.height(8.dp))
                LimeButton(stringResource(R.string.backup_continue), onClick = model::backedUp, enabled = saved)
            } else {
                LimeButton(stringResource(R.string.action_done), onClick = model::backedUp)
            }
        },
    ) {
        Title(stringResource(R.string.backup_title))
        Spacer(Modifier.height(8.dp))
        Body(stringResource(R.string.backup_explain))
        Spacer(Modifier.height(14.dp))
        val words = backup.words.split(" ")
        for (row in words.indices.chunked(2)) {
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                for (i in row) {
                    Row(
                        Modifier.weight(1f).padding(vertical = 4.dp).clip(RoundedCornerShape(12.dp)).background(Palette.Panel).padding(horizontal = 12.dp, vertical = 10.dp),
                    ) {
                        Text("${i + 1}", style = MaterialTheme.typography.bodyMedium, color = Palette.Muted, modifier = Modifier.width(24.dp))
                        Text(words[i], style = MaterialTheme.typography.bodyLarge, color = Palette.Text, fontFamily = FontFamily.Monospace)
                    }
                }
            }
        }
    }
}

// --- the wallet ------------------------------------------------------------------

@Composable
private fun Home(model: ShieldModel, account: AccountState.Ready, copy: (String) -> Unit, openAbout: () -> Unit) {
    val hasBalance = account.balances.any { it.spendable.signum() > 0 }
    Frame(
        action = if (hasBalance) ({ LimeButton(stringResource(R.string.send_out), onClick = model::openSendOut) }) else null,
    ) {
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            Title(stringResource(R.string.app_name), Modifier.weight(1f))
            Menu(model, openAbout)
        }
        Spacer(Modifier.height(14.dp))

        for (item in account.incoming) IncomingLine(model, item)
        for (b in account.balances) for (d in b.waiting) StatusLine(
            stringResource(R.string.status_screening, model.show(d.value, b.decimals, b.symbol)),
            stringResource(R.string.status_screening_detail),
            spinner = true,
        )
        for (b in account.balances) for (d in b.declined) DeclinedLine(model, b, d)

        Panel {
            Label(stringResource(R.string.home_balance))
            val held = account.balances.filter { it.spendable.signum() > 0 }
            if (held.isEmpty()) {
                Spacer(Modifier.height(6.dp))
                Body(stringResource(R.string.home_balance_empty))
            } else {
                for (b in held) {
                    Row(Modifier.fillMaxWidth().padding(top = 10.dp), verticalAlignment = Alignment.CenterVertically) {
                        TokenMark(b.symbol)
                        Spacer(Modifier.width(10.dp))
                        Text(
                            model.show(b.spendable, b.decimals, b.symbol),
                            style = MaterialTheme.typography.titleLarge,
                            color = Palette.Text,
                            fontWeight = FontWeight.Medium,
                        )
                    }
                }
            }
        }
        Spacer(Modifier.height(12.dp))
        Panel {
            Label(stringResource(R.string.home_address))
            Spacer(Modifier.height(8.dp))
            Text(account.address, style = MaterialTheme.typography.bodyLarge, color = Palette.Text, fontFamily = FontFamily.Monospace)
            Spacer(Modifier.height(12.dp))
            QuietButton(stringResource(R.string.home_copy), onClick = { copy(account.address) })
            Spacer(Modifier.height(12.dp))
            Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
                Image(
                    model.qr(account.address),
                    contentDescription = stringResource(R.string.home_qr),
                    modifier = Modifier.size(180.dp).clip(RoundedCornerShape(16.dp)).background(Color.White).padding(8.dp),
                )
            }
            Spacer(Modifier.height(12.dp))
            Note(stringResource(R.string.home_address_note))
        }
        ErrorLine(model.error)
    }
}

@Composable
private fun IncomingLine(model: ShieldModel, item: Incoming) {
    val amount = model.show(item.amount, item.decimals, item.symbol)
    when (item.state) {
        Incoming.State.SHIELDING -> StatusLine(stringResource(R.string.status_shielding, amount), stringResource(R.string.status_shielding_detail), spinner = true)
        Incoming.State.BELOW_MINIMUM -> StatusLine(
            stringResource(R.string.status_received, amount),
            stringResource(R.string.status_below_minimum, model.show(item.minimum ?: item.amount, item.decimals, item.symbol)),
        )
        Incoming.State.GAS_UNAVAILABLE -> StatusLine(stringResource(R.string.status_received, amount), stringResource(R.string.status_gas_unavailable))
        Incoming.State.FEE_HIGH -> {
            StatusLine(
                stringResource(R.string.status_received, amount),
                stringResource(R.string.status_fee_high, model.show(item.fee ?: 0.toBigInteger(), item.decimals, item.symbol)),
            )
            QuietButton(stringResource(R.string.status_shield_now), onClick = { model.check(listOf(item.symbol)) }, enabled = !model.busy)
            Spacer(Modifier.height(12.dp))
        }
    }
}

@Composable
private fun DeclinedLine(model: ShieldModel, balance: Balance, deposit: io.virtroid.shield.Deposit) {
    Column(
        Modifier.fillMaxWidth().padding(bottom = 12.dp).clip(RoundedCornerShape(16.dp)).clickable { model.openExit(balance.symbol, balance.decimals, deposit) }
            .background(Palette.Panel).padding(14.dp),
    ) {
        Text(stringResource(R.string.status_declined, model.show(deposit.value, balance.decimals, balance.symbol)), style = MaterialTheme.typography.titleMedium, color = Palette.Error)
        Spacer(Modifier.height(2.dp))
        Text(stringResource(R.string.status_declined_detail), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
    }
}

@Composable
private fun StatusLine(title: String, detail: String, spinner: Boolean = false) {
    Row(
        Modifier.fillMaxWidth().padding(bottom = 12.dp).clip(RoundedCornerShape(16.dp)).background(Palette.Panel).padding(14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.titleMedium, color = Palette.Text)
            Spacer(Modifier.height(2.dp))
            Text(detail, style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
        }
        if (spinner) {
            Spacer(Modifier.width(12.dp))
            CircularProgressIndicator(color = Palette.Lime, trackColor = Palette.Control, strokeWidth = 2.5.dp, modifier = Modifier.size(22.dp))
        }
    }
}

@Composable
private fun Menu(model: ShieldModel, openAbout: () -> Unit) {
    var open by remember { mutableStateOf(false) }
    val context = LocalContext.current
    var show by remember { mutableStateOf(ShieldWidget.showBalances(context)) }
    Box {
        Box(Modifier.size(48.dp).clip(CircleShape).clickable { open = true }, contentAlignment = Alignment.Center) {
            Icon(Icons.More, contentDescription = stringResource(R.string.menu), tint = Palette.Secondary)
        }
        DropdownMenu(open, { open = false }, containerColor = Palette.Control) {
            DropdownMenuItem(text = { Text(stringResource(R.string.menu_backup)) }, onClick = {
                open = false
                model.showBackup()
            })
            DropdownMenuItem(
                text = { Text(stringResource(R.string.menu_widget_balances)) },
                trailingIcon = { Checkbox(show, null, colors = CheckboxDefaults.colors(checkedColor = Palette.Lime, checkmarkColor = Palette.OnLime)) },
                onClick = {
                    show = !show
                    ShieldWidget.setShowBalances(context, show)
                },
            )
            DropdownMenuItem(text = { Text(stringResource(R.string.about)) }, onClick = {
                open = false
                openAbout()
            })
        }
    }
}

// --- sending out -----------------------------------------------------------------

@Composable
private fun SendForm(model: ShieldModel, account: AccountState.Ready, paste: () -> String?) {
    val held = account.balances.filter { it.spendable.signum() > 0 }
    val selected = held.firstOrNull { it.symbol == model.symbol } ?: held.firstOrNull()
    Frame(
        action = {
            LimeButton(
                stringResource(R.string.send_review),
                onClick = { selected?.let { model.review(it.symbol, it.decimals) } },
                enabled = selected != null && !model.busy && model.recipient.isNotBlank(),
            )
        },
    ) {
        BackRow(model)
        Title(stringResource(R.string.send_out))
        Spacer(Modifier.height(14.dp))
        if (held.size > 1) {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                for (b in held) {
                    FilterChip(
                        selected = b == selected,
                        onClick = { model.symbol = b.symbol },
                        label = { Text(b.symbol) },
                        leadingIcon = { TokenMark(b.symbol, size = 18.dp) },
                        colors = FilterChipDefaults.filterChipColors(
                            containerColor = Palette.Control,
                            labelColor = Palette.Text,
                            selectedContainerColor = Palette.Panel,
                            selectedLabelColor = Palette.Lime,
                        ),
                    )
                }
            }
            Spacer(Modifier.height(12.dp))
        }
        if (selected != null) {
            Label(stringResource(R.string.send_available, model.show(selected.spendable, selected.decimals, selected.symbol)))
            Spacer(Modifier.height(10.dp))
        }
        Field(model.recipient, { model.recipient = it.trim() }, stringResource(R.string.send_to), mono = true, singleLine = false)
        TextButton(onClick = { paste()?.let { model.recipient = it.trim() } }) { Text(stringResource(R.string.send_paste), color = Palette.Lime) }
        Spacer(Modifier.height(6.dp))
        Field(model.amount, { model.amount = it }, stringResource(R.string.send_amount), number = true)
        Spacer(Modifier.height(6.dp))
        Note(stringResource(R.string.send_amount_note))
        if (model.busy) Busy(stringResource(R.string.progress_quote))
        ErrorLine(model.error)
    }
}

@Composable
private fun SendReview(model: ShieldModel, review: Screen.SendReview) {
    val quote = review.quote
    val balance = (model.shield.state.collectAsState().value as? AccountState.Ready)?.balances?.firstOrNull { it.symbol == quote.symbol }
    val decimals = balance?.decimals ?: 18
    Frame(action = { LimeButton(stringResource(R.string.send_confirm), onClick = { model.send(quote) }) }) {
        BackRow(model)
        Title(stringResource(R.string.send_review_title))
        Spacer(Modifier.height(14.dp))
        Panel {
            Line(stringResource(R.string.send_receive), model.show(quote.receive, decimals, quote.symbol), strong = true)
            Line(stringResource(R.string.send_fee), model.show(quote.fee, decimals, quote.symbol))
            Line(stringResource(R.string.send_to), review.recipient, mono = true)
        }
        Spacer(Modifier.height(12.dp))
        if (quote.parts > 1) Note(stringResource(R.string.send_parts, quote.parts))
        if (quote.recent) WarningLine(stringResource(R.string.send_recent))
        Note(stringResource(R.string.send_check_address))
    }
}

@Composable
private fun Working(model: ShieldModel, copy: (String) -> Unit) {
    val work by model.shield.work.collectAsState()
    when (val w = work) {
        is Shield.Work.Sending -> Frame(action = { QuietButton(stringResource(R.string.action_close_keep), onClick = model::finish) }) {
            Spacer(Modifier.height(40.dp))
            Busy(
                stringResource(
                    when (w.stage) {
                        "proving" -> R.string.progress_proving
                        "relaying" -> R.string.progress_relaying
                        "confirming" -> R.string.progress_confirming
                        else -> R.string.progress_starting
                    },
                ),
                if (w.parts > 1) stringResource(R.string.progress_part, w.part, w.parts) else null,
            )
        }
        is Shield.Work.Sent -> Frame(action = { LimeButton(stringResource(R.string.action_done), onClick = model::finish) }) {
            Spacer(Modifier.height(30.dp))
            Icon(Icons.Check, contentDescription = null, tint = Palette.Lime, modifier = Modifier.size(48.dp))
            Spacer(Modifier.height(12.dp))
            Title(stringResource(R.string.send_done))
            Spacer(Modifier.height(10.dp))
            for (hash in w.txHashes) {
                Row(Modifier.fillMaxWidth().clip(RoundedCornerShape(10.dp)).clickable { copy(hash) }.padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("${hash.take(12)}…${hash.takeLast(8)}", style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary, fontFamily = FontFamily.Monospace, modifier = Modifier.weight(1f))
                    Icon(Icons.Copy, contentDescription = stringResource(R.string.home_copy), tint = Palette.Secondary, modifier = Modifier.size(18.dp))
                }
            }
        }
        is Shield.Work.Failed -> Frame(action = { LimeButton(stringResource(R.string.action_done), onClick = model::finish) }) {
            Spacer(Modifier.height(30.dp))
            Title(stringResource(R.string.send_failed))
            ErrorLine(Errors.describe(LocalContext.current, w.code, w.message))
        }
        null -> Frame { Busy(null) }
    }
}

// --- taking a declined deposit back ----------------------------------------------

@Composable
private fun Exit(model: ShieldModel, exit: Screen.Exit, copy: (String) -> Unit, paste: () -> String?) {
    val plan = exit.plan
    val funded = plan != null && plan.balance >= plan.needed
    Frame(
        action = {
            if (funded) {
                LimeButton(stringResource(R.string.exit_confirm), onClick = { model.exit(exit) }, enabled = model.recipient.isNotBlank())
            } else {
                QuietButton(stringResource(R.string.exit_check), onClick = { model.refreshExit(exit) }, enabled = !model.busy)
            }
        },
    ) {
        BackRow(model)
        Title(stringResource(R.string.exit_title))
        Spacer(Modifier.height(8.dp))
        Body(stringResource(R.string.exit_explain, model.show(exit.deposit.value, exit.decimals, exit.symbol)))
        Spacer(Modifier.height(14.dp))
        if (plan != null && !funded) {
            Panel {
                Label(stringResource(R.string.exit_needs_eth, model.show(plan.needed, 18, "ETH")))
                Spacer(Modifier.height(8.dp))
                Text(plan.owner, style = MaterialTheme.typography.bodyLarge, color = Palette.Text, fontFamily = FontFamily.Monospace)
                Spacer(Modifier.height(10.dp))
                QuietButton(stringResource(R.string.home_copy), onClick = { copy(plan.owner) })
            }
        } else if (plan != null) {
            Field(model.recipient, { model.recipient = it.trim() }, stringResource(R.string.send_to), mono = true, singleLine = false)
            TextButton(onClick = { paste()?.let { model.recipient = it.trim() } }) { Text(stringResource(R.string.send_paste), color = Palette.Lime) }
            WarningLine(stringResource(R.string.exit_public))
        }
        ErrorLine(model.error)
    }
}

// --- pieces ----------------------------------------------------------------------

/** A screen: its content scrolling, its action pinned below (the cloud phone's screen is short). */
@Composable
private fun Frame(action: (@Composable ColumnScope.() -> Unit)? = null, body: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp, vertical = 12.dp)) {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()), content = body)
        if (action != null) Column(Modifier.padding(top = 12.dp), content = action)
    }
}

@Composable
private fun Panel(content: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(20.dp)).background(Palette.Panel).padding(16.dp), content = content)
}

@Composable
private fun BackRow(model: ShieldModel) {
    Row(
        Modifier.padding(bottom = 8.dp).clip(CircleShape).clickable { model.back() }.heightIn(min = 48.dp).padding(end = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(Icons.Back, contentDescription = null, tint = Palette.Secondary, modifier = Modifier.size(20.dp))
        Spacer(Modifier.width(6.dp))
        Text(stringResource(R.string.action_back), style = MaterialTheme.typography.labelLarge, color = Palette.Secondary)
    }
}

@Composable
private fun Title(text: String, modifier: Modifier = Modifier) {
    Text(text, style = MaterialTheme.typography.headlineSmall, color = Palette.Text, fontWeight = FontWeight.Medium, modifier = modifier)
}

@Composable
private fun Label(text: String) {
    Text(text, style = MaterialTheme.typography.labelLarge, color = Palette.Secondary)
}

@Composable
private fun Body(text: String) {
    Text(text, style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
}

@Composable
private fun Note(text: String) {
    Text(text, style = MaterialTheme.typography.bodyMedium, color = Palette.Muted, modifier = Modifier.padding(vertical = 2.dp))
}

@Composable
private fun Line(label: String, value: String, strong: Boolean = false, mono: Boolean = false) {
    // Label and value share the row (two to three), so a long value wraps instead of crushing its label.
    Row(Modifier.fillMaxWidth().padding(vertical = 7.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(label, style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary, modifier = Modifier.weight(2f))
        Spacer(Modifier.width(12.dp))
        Text(
            value,
            style = if (strong) MaterialTheme.typography.titleMedium else MaterialTheme.typography.bodyMedium,
            color = Palette.Text,
            fontFamily = if (mono) FontFamily.Monospace else null,
            textAlign = TextAlign.End,
            modifier = Modifier.weight(3f),
        )
    }
}

@Composable
private fun WarningLine(value: String) {
    Row(Modifier.padding(vertical = 4.dp), verticalAlignment = Alignment.Top) {
        Icon(Icons.Warning, contentDescription = null, tint = Palette.Warning, modifier = Modifier.size(18.dp).padding(top = 2.dp))
        Spacer(Modifier.width(8.dp))
        Text(value, style = MaterialTheme.typography.bodyMedium, color = Palette.Warning)
    }
}

@Composable
private fun ErrorLine(message: String?) {
    if (message == null) return
    Spacer(Modifier.height(8.dp))
    Text(message, style = MaterialTheme.typography.bodyMedium, color = Palette.Error)
}

@Composable
private fun Busy(label: String?, detail: String? = null) {
    Column(Modifier.fillMaxWidth().padding(vertical = 12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        CircularProgressIndicator(color = Palette.Lime, trackColor = Palette.Control, strokeWidth = 3.dp, modifier = Modifier.size(32.dp))
        if (label != null) {
            Spacer(Modifier.height(14.dp))
            Text(label, style = MaterialTheme.typography.titleMedium, color = Palette.Text, textAlign = TextAlign.Center)
        }
        if (detail != null) {
            Spacer(Modifier.height(4.dp))
            Text(detail, style = MaterialTheme.typography.bodyMedium, color = Palette.Muted, textAlign = TextAlign.Center)
        }
    }
}

@Composable
private fun Field(value: String, onChange: (String) -> Unit, label: String, mono: Boolean = false, singleLine: Boolean = true, number: Boolean = false) {
    OutlinedTextField(
        value = value,
        onValueChange = onChange,
        label = { Text(label) },
        singleLine = singleLine,
        modifier = Modifier.fillMaxWidth(),
        textStyle = MaterialTheme.typography.bodyLarge.copy(
            fontFamily = if (mono) FontFamily.Monospace else null,
            fontSize = if (mono) 15.sp else 16.sp,
        ),
        keyboardOptions = if (number) KeyboardOptions(keyboardType = KeyboardType.Decimal) else KeyboardOptions.Default,
        colors = OutlinedTextFieldDefaults.colors(
            focusedBorderColor = Palette.Lime,
            unfocusedBorderColor = Palette.Border,
            cursorColor = Palette.Lime,
            focusedLabelColor = Palette.Lime,
            unfocusedLabelColor = Palette.Secondary,
            focusedTextColor = Palette.Text,
            unfocusedTextColor = Palette.Text,
        ),
    )
}
