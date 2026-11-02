// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.ui.card

import android.content.ClipData
import android.content.Intent
import android.provider.Settings
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.SizeTransform
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.paneTitle
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.core.net.toUri
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.virtroid.shield.AccountState
import io.virtroid.shield.Address
import io.virtroid.shield.Amounts
import io.virtroid.shield.Operations
import io.virtroid.shield.R
import io.virtroid.shield.engine.EngineException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.math.BigInteger

/** What the card's panel shows before the account is ready. */
private enum class Gate { NOT_CONFIGURED, CONNECT, LOADING, LOAD_FAILED, SETUP }

/** One panel's identity, for the animation between panels. */
private data class PanelKey(val gate: Gate?, val tab: Tab?, val depth: Int, val step: Step?, val outcome: Int)

/**
 * The transaction card: a full-width rounded container over the dimmed home
 * screen, the three tabs, and the inset panel whose content changes with
 * each step (design-brief.md).
 */
@Composable
fun CardScreen(model: CardModel, onClose: () -> Unit, openLink: (String) -> Boolean) {
    val account by model.account.collectAsStateWithLifecycle()
    val operation by model.operations.current.collectAsStateWithLifecycle()
    val none = remember { MutableInteractionSource() }
    Box(
        Modifier.fillMaxSize().background(Palette.Scrim).clickable(none, indication = null, onClick = onClose)
            .windowInsetsPadding(WindowInsets.safeDrawing),
        contentAlignment = Alignment.BottomCenter,
    ) {
        Column(
            Modifier.padding(16.dp).widthIn(max = 560.dp).fillMaxWidth()
                .clip(RoundedCornerShape(32.dp)).background(Palette.Outer).border(1.dp, Palette.Border, RoundedCornerShape(32.dp))
                .clickable(none, indication = null) {}
                .padding(10.dp),
        ) {
            Tabs(model.tab, model::select)
            Spacer(Modifier.height(10.dp))
            Panel(model, account, operation, onClose, openLink)
        }
    }
}

@Composable
private fun Tabs(selected: Tab, onSelect: (Tab) -> Unit) {
    Row(
        Modifier.fillMaxWidth().clip(CircleShape).background(Palette.Control).padding(4.dp),
        horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        for (tab in Tab.entries) {
            val on = tab == selected
            val label = stringResource(
                when (tab) {
                    Tab.SHIELD -> R.string.shield
                    Tab.WITHDRAW -> R.string.withdraw
                    Tab.SEND -> R.string.send
                },
            )
            Box(
                Modifier.weight(1f).heightIn(min = 48.dp).clip(CircleShape)
                    .then(if (on) Modifier.border(1.5.dp, Palette.Lime, CircleShape) else Modifier)
                    .clickable { onSelect(tab) }
                    .semantics {
                        role = Role.Tab
                        this.selected = on
                    },
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    label,
                    style = MaterialTheme.typography.titleSmall,
                    fontWeight = if (on) FontWeight.SemiBold else FontWeight.Medium,
                    color = if (on) Palette.Text else Palette.Muted,
                )
            }
        }
    }
}

@Composable
private fun Panel(
    model: CardModel,
    account: AccountState,
    operation: Operations.Operation?,
    onClose: () -> Unit,
    openLink: (String) -> Boolean,
) {
    val gate = when {
        !model.configured -> Gate.NOT_CONFIGURED
        !model.walletConnected -> Gate.CONNECT
        account is AccountState.NeedsSetup -> Gate.SETUP
        account is AccountState.Ready -> null
        model.loadError != null -> Gate.LOAD_FAILED
        else -> Gate.LOADING
    }
    val d = model.draft
    val outcome = when (operation?.outcome) {
        null -> 0
        is Operations.Outcome.Done -> 1
        is Operations.Outcome.Failed -> 2
    }
    val key = if (gate != null) PanelKey(gate, null, 0, null, 0)
    else PanelKey(null, d.tab, d.stack.size, d.step, if (d.step == Step.Processing) outcome else 0)
    val reduced = reducedMotion()
    Box(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(24.dp)).background(Palette.Panel)
            .border(1.dp, Palette.Border, RoundedCornerShape(24.dp)),
    ) {
        AnimatedContent(
            targetState = key,
            transitionSpec = {
                val spec = if (reduced) {
                    fadeIn(tween(150)) togetherWith fadeOut(tween(150))
                } else {
                    val sameTab = initialState.tab == targetState.tab
                    val ahead = if (sameTab) targetState.depth >= initialState.depth else model.forward
                    val dir = if (ahead) 1 else -1
                    (slideInHorizontally(tween(260)) { it / 5 * dir } + fadeIn(tween(260))) togetherWith
                        (slideOutHorizontally(tween(220)) { -it / 5 * dir } + fadeOut(tween(180)))
                }
                spec using SizeTransform(clip = true) { _, _ -> tween(if (reduced) 0 else 260) }
            },
            label = "panel",
        ) { k ->
            Column(Modifier.fillMaxWidth().padding(20.dp)) {
                when (k.gate) {
                    Gate.NOT_CONFIGURED -> StepFrame("") { Note(stringResource(R.string.not_configured)) }
                    Gate.CONNECT -> ConnectPanel(model, openLink)
                    Gate.LOADING -> StepFrame("") { Busy(stringResource(R.string.card_updating)) }
                    Gate.LOAD_FAILED -> StepFrame("", action = { LimeButton(stringResource(R.string.retry), onClick = model::load) }) {
                        ErrorLine(model.loadError)
                    }
                    Gate.SETUP -> SetupPanel(model)
                    null -> {
                        val draft = model.drafts.getValue(checkNotNull(k.tab))
                        StepPanel(model, draft, checkNotNull(k.step), operation, onClose, openLink)
                    }
                }
            }
        }
    }
}

@Composable
private fun ColumnScope.StepPanel(
    model: CardModel,
    d: Draft,
    step: Step,
    operation: Operations.Operation?,
    onClose: () -> Unit,
    openLink: (String) -> Boolean,
) {
    val back: (() -> Unit)? = if (model.canGoBack(d) && d.stack.lastOrNull() == step) ({ model.back() }) else null
    CompositionLocalProvider(LocalBack provides back) {
        when (step) {
            Step.Amount -> AmountPanel(model, d)
            Step.Token -> TokenPanel(model, d)
            Step.Destination -> DestinationPanel(model, d)
            Step.Recipient -> RecipientPanel(model, d)
            Step.Preparing -> StepFrame(stringResource(R.string.card_making_proof)) {
                Busy(stringResource(R.string.card_making_proof), stringResource(R.string.card_takes_moment))
            }
            Step.Review -> if (d.tab == Tab.SHIELD) ShieldReview(model, d) else SpendReview(model, d)
            Step.Processing -> ProcessingPanel(model, operation, onClose, openLink)
            is Step.Exit -> ExitPanel(model, step)
        }
    }
}

// --- the first step ------------------------------------------------------------

@Composable
private fun AmountPanel(model: CardModel, d: Draft) {
    val label = stringResource(
        when (d.tab) {
            Tab.SHIELD -> R.string.card_amount_shield
            Tab.WITHDRAW -> R.string.card_amount_withdraw
            Tab.SEND -> R.string.card_amount_send
        },
    )
    val state = model.amountState(d)
    StepFrame(label) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(label, style = MaterialTheme.typography.titleMedium, color = Palette.Text, modifier = Modifier.weight(1f))
            TokenChip(d.symbol, model.chain?.chainId ?: 0, model.chain?.name.orEmpty(), onClick = model::openTokens)
        }
        Spacer(Modifier.height(18.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            AmountField(
                value = d.amount,
                label = label,
                onChange = model::setAmount,
                onDone = model::next,
                autoFocus = state is AmountState.Hint && d.amount.isEmpty(),
                modifier = Modifier.weight(1f),
            )
            Spacer(Modifier.width(12.dp))
            ContinueButton(enabled = state is AmountState.Ok, onClick = model::next)
        }
        Spacer(Modifier.height(10.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            val (line, color) = when (state) {
                AmountState.Waiting -> stringResource(R.string.card_updating) to Palette.Muted
                is AmountState.Hint -> text(state.message) to Palette.Muted
                is AmountState.Ok -> text(state.hint) to Palette.Muted
                is AmountState.Invalid -> text(state.message) to Palette.Error
            }
            Text(line, style = MaterialTheme.typography.bodyMedium, color = color, modifier = Modifier.weight(1f))
            val canMax = d.tab != Tab.SHIELD && (model.balance(d.symbol)?.spendable?.signum() ?: 0) > 0
            if (canMax && !d.max) {
                TextButton(onClick = model::useMax, modifier = Modifier.heightIn(min = 48.dp)) {
                    Text(stringResource(R.string.action_max), color = Palette.Lime)
                }
            }
        }
        if (state is AmountState.Invalid && state.offerShield) {
            TextButton(onClick = { model.select(Tab.SHIELD) }, modifier = Modifier.heightIn(min = 48.dp)) {
                Text(stringResource(R.string.card_shield_first), color = Palette.Lime)
            }
        }
        if (model.loading && state !is AmountState.Waiting) {
            Text(stringResource(R.string.card_updating), style = MaterialTheme.typography.bodySmall, color = Palette.Muted)
        }
        Declined(model)
    }
}

/** Deposits screening turned down, on the first step of every tab: their way back out. */
@Composable
private fun Declined(model: CardModel) {
    val ready = model.account.collectAsStateWithLifecycle().value as? AccountState.Ready ?: return
    for (b in ready.balances) {
        for (deposit in b.declined) {
            Spacer(Modifier.height(12.dp))
            Row(
                Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(Palette.Control).padding(start = 14.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Icon(CardIcons.Warning, contentDescription = null, tint = Palette.Warning, modifier = Modifier.size(18.dp))
                Spacer(Modifier.width(10.dp))
                Text(
                    stringResource(R.string.card_declined, Amounts.show(deposit.value, b.decimals, b.symbol)),
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Text,
                    modifier = Modifier.weight(1f).padding(vertical = 12.dp),
                )
                TextButton(onClick = { model.exit(b.symbol, deposit) }, modifier = Modifier.heightIn(min = 48.dp)) {
                    Text(stringResource(R.string.card_take_back), color = Palette.Lime)
                }
            }
        }
    }
}

@Composable
private fun AmountField(
    value: String,
    label: String,
    onChange: (String) -> Unit,
    onDone: () -> Unit,
    autoFocus: Boolean,
    modifier: Modifier = Modifier,
    readOnly: Boolean = false,
) {
    val focus = remember { FocusRequester() }
    val measurer = rememberTextMeasurer()
    BoxWithConstraints(modifier) {
        // The largest size the whole amount fits at, so it shrinks rather than scrolls.
        val room = constraints.maxWidth
        val size = remember(value, room) {
            AMOUNT_SIZES.firstOrNull { sp ->
                measurer.measure(value.ifEmpty { "0" }, TextStyle(fontSize = sp.sp, fontWeight = FontWeight.Medium)).size.width < room
            } ?: AMOUNT_SIZES.last()
        }.sp
        val style = TextStyle(fontSize = size, lineHeight = size * 1.15f, fontWeight = FontWeight.Medium, color = Palette.Text)
        BasicTextField(
            value = value,
            onValueChange = onChange,
            readOnly = readOnly,
            singleLine = true,
            textStyle = style,
            cursorBrush = SolidColor(Palette.Lime),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Decimal, imeAction = ImeAction.Done),
            keyboardActions = KeyboardActions(onDone = { onDone() }),
            modifier = Modifier.fillMaxWidth().focusRequester(focus).heightIn(min = 56.dp).semantics { contentDescription = label },
            decorationBox = { inner ->
                Box(contentAlignment = Alignment.CenterStart) {
                    if (value.isEmpty()) Text("0", style = style.copy(color = Palette.Muted))
                    inner()
                }
            },
        )
    }
    if (autoFocus && !readOnly) LaunchedEffect(Unit) { runCatching { focus.requestFocus() } }
}

@Composable
private fun ContinueButton(enabled: Boolean, onClick: () -> Unit) {
    val description = stringResource(R.string.card_continue)
    Box(
        Modifier.size(56.dp).clip(CircleShape).background(if (enabled) Palette.Lime else Palette.Control)
            .clickable(enabled = enabled, onClick = onClick)
            .semantics {
                contentDescription = description
                role = Role.Button
            },
        contentAlignment = Alignment.Center,
    ) {
        Icon(CardIcons.Forward, contentDescription = null, tint = if (enabled) Palette.OnLime else Palette.Muted)
    }
}

@Composable
private fun TokenChip(symbol: String, chainId: Int, chainName: String, onClick: (() -> Unit)?) {
    val description = stringResource(R.string.card_token_on, symbol, chainName)
    Row(
        Modifier.clip(CircleShape).background(Palette.Control)
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
            .heightIn(min = 48.dp).padding(start = 8.dp, end = if (onClick != null) 8.dp else 14.dp)
            .semantics(mergeDescendants = true) { contentDescription = description },
        verticalAlignment = Alignment.CenterVertically,
    ) {
        TokenMark(symbol, chainId, 26.dp)
        Spacer(Modifier.width(8.dp))
        Text(symbol, style = MaterialTheme.typography.titleMedium, color = Palette.Text, fontWeight = FontWeight.SemiBold)
        if (onClick != null) {
            Spacer(Modifier.width(2.dp))
            Icon(CardIcons.Expand, contentDescription = null, tint = Palette.Secondary)
        }
    }
}

@Composable
private fun TokenPanel(model: CardModel, d: Draft) {
    val title = stringResource(R.string.card_choose_token)
    StepFrame(title) {
        Text(title, style = MaterialTheme.typography.titleMedium, color = Palette.Text)
        val busy = model.operations.running
        for (chain in model.chains) {
            val current = chain.chainId == model.chain?.chainId
            Spacer(Modifier.height(14.dp))
            Text(
                if (current || !busy) chain.name else stringResource(R.string.card_chain_busy, chain.name),
                style = MaterialTheme.typography.labelLarge,
                color = Palette.Secondary,
            )
            for (asset in chain.assets) {
                val enabled = current || !busy
                val chosen = current && asset.symbol == d.symbol
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 56.dp).clip(RoundedCornerShape(14.dp))
                        .clickable(enabled = enabled) { model.pickToken(chain.chainId, asset.symbol) }
                        .padding(horizontal = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    TokenMark(asset.symbol, chain.chainId, 26.dp)
                    Spacer(Modifier.width(12.dp))
                    Text(asset.symbol, style = MaterialTheme.typography.titleMedium, color = if (enabled) Palette.Text else Palette.Muted, modifier = Modifier.weight(1f))
                    if (current) {
                        val amount = if (d.tab == Tab.SHIELD) {
                            ((model.funds[asset.symbol] as? Loadable.Ready)?.value?.balance)
                        } else {
                            model.balance(asset.symbol)?.spendable
                        }
                        if (amount != null) Text(Amounts.show(amount, asset.decimals), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
                    }
                    if (chosen) {
                        Spacer(Modifier.width(8.dp))
                        Icon(CardIcons.Check, contentDescription = null, tint = Palette.Lime)
                    }
                }
            }
        }
    }
}

// --- Shield ----------------------------------------------------------------------

@Composable
private fun DestinationPanel(model: CardModel, d: Draft) {
    val asset = model.asset(d.symbol) ?: return
    val chain = model.chain ?: return
    val units = (model.amountState(d) as? AmountState.Ok)?.units ?: BigInteger.ZERO
    val title = stringResource(R.string.card_you_shield)
    StepFrame(title, action = {
        LimeButton(stringResource(R.string.card_review_shield), onClick = model::review, enabled = d.plan is Loadable.Ready)
    }) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(title, style = MaterialTheme.typography.titleMedium, color = Palette.Text, modifier = Modifier.weight(1f))
            TokenChip(d.symbol, chain.chainId, chain.name, onClick = null)
        }
        BigAmount(Amounts.show(units, asset.decimals))
        Text(stringResource(R.string.card_into_pool, d.symbol, chain.name), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
        Spacer(Modifier.height(16.dp))
        Text(stringResource(R.string.card_shielding_to_own), style = MaterialTheme.typography.bodyLarge, color = Palette.Text)
        Text(Address.short(model.owner.orEmpty()), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary, fontFamily = FontFamily.Monospace)
        Spacer(Modifier.height(12.dp))
        HorizontalDivider(color = Palette.Border)
        when (val plan = d.plan) {
            is Loadable.Ready -> {
                Line(stringResource(R.string.card_you_hold), holdText(plan.value.hold, asset.decimals, d.symbol, asset.yield))
                Line(stringResource(R.string.card_screening_fee), Amounts.show(plan.value.fee, asset.decimals, d.symbol))
            }
            is Loadable.Failed -> {
                Spacer(Modifier.height(10.dp))
                Text(text(plan.message), style = MaterialTheme.typography.bodyMedium, color = Palette.Error)
                TextButton(onClick = model::retryPlan, modifier = Modifier.heightIn(min = 48.dp)) { Text(stringResource(R.string.retry), color = Palette.Lime) }
            }
            else -> Line(stringResource(R.string.card_you_hold), stringResource(R.string.card_working_out))
        }
        (model.funds[d.symbol] as? Loadable.Ready)?.value?.let {
            Line(stringResource(R.string.card_in_wallet_label), Amounts.show(it.balance, asset.decimals, d.symbol))
        }
        Line(stringResource(R.string.card_network_fee), stringResource(R.string.card_shown_by_wallet))
        Spacer(Modifier.height(12.dp))
        if (asset.yield) Note(stringResource(R.string.card_earns))
        Note(stringResource(R.string.card_screening_note))
    }
}

@Composable
private fun ShieldReview(model: CardModel, d: Draft) {
    val asset = model.asset(d.symbol) ?: return
    val chain = model.chain ?: return
    val plan = (d.plan as? Loadable.Ready)?.value ?: return
    val units = (model.amountState(d) as? AmountState.Ok)?.units ?: return
    val title = stringResource(R.string.card_review_shield)
    StepFrame(title, action = {
        LimeButton(stringResource(R.string.card_confirm_shield), onClick = model::confirm, enabled = !model.operations.running)
    }) {
        Text(title, style = MaterialTheme.typography.titleLarge, color = Palette.Text)
        Spacer(Modifier.height(8.dp))
        Line(stringResource(R.string.card_you_shield), Amounts.show(units, asset.decimals, d.symbol), strong = true)
        Line(stringResource(R.string.card_from), stringResource(R.string.card_your_wallet, Address.short(model.owner.orEmpty())))
        Line(stringResource(R.string.card_network), chain.name)
        Line(stringResource(R.string.card_into), stringResource(R.string.card_pool, d.symbol))
        Line(stringResource(R.string.card_you_hold), holdText(plan.hold, asset.decimals, d.symbol, asset.yield))
        Line(stringResource(R.string.card_screening_fee), Amounts.show(plan.fee, asset.decimals, d.symbol))
        Line(
            stringResource(R.string.card_leaves_wallet),
            stringResource(
                if (plan.exact) R.string.card_plus_network_fee else R.string.card_at_most_plus_network_fee,
                Amounts.show(plan.cost, asset.decimals, d.symbol),
            ),
        )
        Spacer(Modifier.height(12.dp))
        Note(
            when (plan.approvals) {
                0 -> stringResource(R.string.card_wallet_asks_once)
                1 -> stringResource(R.string.card_wallet_asks_twice, Amounts.show(units, asset.decimals, d.symbol))
                else -> pluralStringResource(R.plurals.card_wallet_asks_approvals, plan.approvals, plan.approvals)
            },
        )
        ErrorLine(d.error)
    }
}

/** What the account will hold: about that much for a yield asset (its shares' value moves), held as its pp token. */
private fun holdText(units: BigInteger, decimals: Int, symbol: String, pp: Boolean): String = buildString {
    if (pp) append("≈ ")
    append(Amounts.show(units, decimals, symbol))
    if (pp) append(" · pp").append(symbol)
}

// --- Withdraw and Send -----------------------------------------------------------

@Composable
private fun RecipientPanel(model: CardModel, d: Draft) {
    val asset = model.asset(d.symbol) ?: return
    val chain = model.chain ?: return
    val ok = model.amountState(d) as? AmountState.Ok
    val state = model.recipientState(d)
    val title = stringResource(R.string.card_recipient_receives)
    val clipboard = LocalClipboard.current
    val scope = rememberCoroutineScope()
    StepFrame(title, action = {
        AnimatedVisibility(visible = state is RecipientState.Fine) {
            LimeButton(
                stringResource(if (d.tab == Tab.SEND) R.string.card_review_send else R.string.card_review_withdrawal),
                onClick = model::review,
            )
        }
    }) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(title, style = MaterialTheme.typography.titleMedium, color = Palette.Text, modifier = Modifier.weight(1f))
            TokenChip(d.symbol, chain.chainId, chain.name, onClick = null)
        }
        BigAmount(ok?.let { Amounts.show(it.units, asset.decimals) } ?: "0")
        if (d.max) Note(stringResource(R.string.card_fee_from_max))
        Text(
            if (d.tab == Tab.SEND) stringResource(R.string.card_sent_privately, chain.name) else stringResource(R.string.card_received_publicly, chain.name),
            style = MaterialTheme.typography.bodyMedium,
            color = Palette.Secondary,
        )
        if (d.tab == Tab.WITHDRAW && asset.yield) {
            Text(stringResource(R.string.card_redeemed, d.symbol), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
        }
        Spacer(Modifier.height(14.dp))
        val (support, supportColor) = when (state) {
            RecipientState.Empty -> null to Palette.Muted
            is RecipientState.Typing -> text(state.message) to Palette.Muted
            is RecipientState.Problem -> text(state.message) to Palette.Error
            RecipientState.Checking -> stringResource(R.string.card_address_checking) to Palette.Muted
            is RecipientState.CantReceive -> stringResource(R.string.card_recipient_unregistered, chain.name) to Palette.Error
            is RecipientState.Fine -> text(state.message) to Palette.Lime
        }
        OutlinedTextField(
            value = d.recipient,
            onValueChange = model::setRecipient,
            label = { Text(stringResource(R.string.card_recipient_label)) },
            singleLine = true,
            isError = state is RecipientState.Problem || state is RecipientState.CantReceive,
            textStyle = MaterialTheme.typography.bodyLarge.copy(fontFamily = FontFamily.Monospace, color = Palette.Text),
            keyboardOptions = KeyboardOptions(
                keyboardType = KeyboardType.Ascii,
                capitalization = KeyboardCapitalization.None,
                autoCorrectEnabled = false,
                imeAction = ImeAction.Done,
            ),
            trailingIcon = {
                IconButton(onClick = {
                    scope.launch {
                        clipboard.getClipEntry()?.clipData?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.text?.let { model.setRecipient(it.toString()) }
                    }
                }) { Icon(CardIcons.Paste, contentDescription = stringResource(R.string.card_paste), tint = Palette.Secondary) }
            },
            supportingText = support?.let { s ->
                {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        if (state is RecipientState.Fine) {
                            Icon(CardIcons.Check, contentDescription = null, tint = Palette.Lime, modifier = Modifier.size(16.dp))
                            Spacer(Modifier.width(4.dp))
                        }
                        Text(s, color = supportColor)
                    }
                }
            },
            colors = OutlinedTextFieldDefaults.colors(
                focusedBorderColor = Palette.Lime,
                unfocusedBorderColor = Palette.Border,
                focusedLabelColor = Palette.Lime,
                unfocusedLabelColor = Palette.Muted,
                cursorColor = Palette.Lime,
                errorBorderColor = Palette.Error,
                errorLabelColor = Palette.Error,
            ),
            modifier = Modifier.fillMaxWidth(),
        )
        (state as? RecipientState.Fine)?.warning?.let { WarningLine(text(it)) }
        if (state is RecipientState.Problem && d.tab == Tab.SEND && d.receives is Loadable.Failed) {
            TextButton(onClick = model::recheckReceives, modifier = Modifier.heightIn(min = 48.dp)) { Text(stringResource(R.string.retry), color = Palette.Lime) }
        }
        if (state is RecipientState.CantReceive) {
            TextButton(onClick = model::withdrawInstead, modifier = Modifier.heightIn(min = 48.dp)) {
                Text(stringResource(R.string.card_withdraw_instead), color = Palette.Lime)
            }
        }
        Spacer(Modifier.height(8.dp))
        if (d.tab == Tab.WITHDRAW) Note(stringResource(R.string.card_public_note))
        if (!d.max) Note(stringResource(R.string.card_fee_on_top))
        ErrorLine(d.error)
    }
}

@Composable
private fun SpendReview(model: CardModel, d: Draft) {
    val asset = model.asset(d.symbol) ?: return
    val chain = model.chain ?: return
    val q = d.quote ?: return
    val address = (model.recipientState(d) as? RecipientState.Fine)?.address ?: return
    val send = d.tab == Tab.SEND
    val now = rememberNow()
    val expired = model.expired(q, now)
    val title = stringResource(if (send) R.string.card_review_send else R.string.card_review_withdrawal)
    StepFrame(title, action = {
        if (expired) {
            LimeButton(stringResource(R.string.card_refresh), onClick = { model.prepare() })
        } else {
            LimeButton(
                stringResource(if (send) R.string.card_confirm_send else R.string.card_confirm_withdrawal),
                onClick = model::confirm,
                enabled = !model.operations.running,
            )
        }
    }) {
        Text(title, style = MaterialTheme.typography.titleLarge, color = Palette.Text)
        Spacer(Modifier.height(8.dp))
        Line(stringResource(R.string.card_recipient_receives), Amounts.show(q.receive, asset.decimals, d.symbol), strong = true)
        Line(stringResource(R.string.card_relayer_fee), Amounts.show(q.fee, asset.decimals, d.symbol))
        Line(stringResource(R.string.card_taken_from), Amounts.show(q.receive + q.fee, asset.decimals, d.symbol))
        Line(stringResource(R.string.card_network), chain.name)
        Spacer(Modifier.height(10.dp))
        Text(stringResource(R.string.card_to), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
        Text(address, style = MaterialTheme.typography.bodyLarge, color = Palette.Text, fontFamily = FontFamily.Monospace)
        Spacer(Modifier.height(12.dp))
        if (send) Note(stringResource(R.string.card_private)) else WarningLine(stringResource(R.string.card_public))
        q.expiresAt?.let { at ->
            if (expired) {
                Text(stringResource(R.string.card_fee_expired), style = MaterialTheme.typography.bodyMedium, color = Palette.Warning)
            } else {
                val left = ((at - now) / 1000).coerceAtLeast(0)
                Note(stringResource(R.string.card_fee_held, "%d:%02d".format(left / 60, left % 60)))
            }
        }
        ErrorLine(d.error)
        if (!expired) {
            Spacer(Modifier.height(8.dp))
            Note(stringResource(R.string.card_cant_undo))
        }
    }
}

// --- the operation ---------------------------------------------------------------

@Composable
private fun ProcessingPanel(model: CardModel, op: Operations.Operation?, onClose: () -> Unit, openLink: (String) -> Boolean) {
    if (op == null) {
        StepFrame("") { Busy(stringResource(R.string.card_updating)) }
        return
    }
    when (val outcome = op.outcome) {
        null -> Running(model, op, onClose, openLink)
        is Operations.Outcome.Done -> Finished(model, op, outcome, onClose)
        is Operations.Outcome.Failed -> {
            val title = stringResource(R.string.card_failed)
            StepFrame(title, action = {
                LimeButton(stringResource(R.string.retry), onClick = model::retry)
                Spacer(Modifier.height(8.dp))
                QuietButton(stringResource(R.string.close), onClick = {
                    model.retry()
                    onClose()
                })
            }) {
                Text(title, style = MaterialTheme.typography.titleLarge, color = Palette.Error)
                Spacer(Modifier.height(8.dp))
                Text(text(model.describe(EngineException(outcome.code, outcome.message))), style = MaterialTheme.typography.bodyLarge, color = Palette.Text)
                Spacer(Modifier.height(6.dp))
                Note(stringResource(R.string.card_draft_kept))
            }
        }
    }
}

@Composable
private fun Running(model: CardModel, op: Operations.Operation, onClose: () -> Unit, openLink: (String) -> Boolean) {
    val s = op.summary
    val now = rememberNow()
    val amount = Amounts.show(s.amount, s.decimals, s.symbol)
    val status = when (val stage = op.stage) {
        Operations.Stage.Starting -> when (s.kind) {
            Operations.Kind.SHIELD, Operations.Kind.EXIT -> stringResource(R.string.card_status_wallet)
            else -> stringResource(R.string.card_status_relaying)
        }
        is Operations.Stage.Wallet -> when {
            stage.approval -> stringResource(R.string.card_status_approve, amount)
            s.kind == Operations.Kind.SHIELD -> stringResource(R.string.card_status_confirm_shield)
            s.kind == Operations.Kind.EXIT -> stringResource(R.string.card_status_confirm_exit)
            else -> stringResource(R.string.progress_wallet)
        }
        Operations.Stage.Proving -> stringResource(R.string.card_making_proof)
        Operations.Stage.Relaying -> stringResource(R.string.card_status_relaying)
        is Operations.Stage.Confirming -> stringResource(R.string.card_status_confirming, s.chainName)
        Operations.Stage.Syncing -> stringResource(R.string.card_status_syncing)
    }
    StepFrame(status) {
        Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
            if (op.approved) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Icon(CardIcons.Check, contentDescription = null, tint = Palette.Lime, modifier = Modifier.size(18.dp))
                    Spacer(Modifier.width(6.dp))
                    Text(stringResource(R.string.card_approved, s.symbol), style = MaterialTheme.typography.bodyMedium, color = Palette.Lime)
                }
                Spacer(Modifier.height(14.dp))
            }
            CircularProgressIndicator(color = Palette.Lime, trackColor = Palette.Control, strokeWidth = 3.dp, modifier = Modifier.size(36.dp))
            Spacer(Modifier.height(16.dp))
            Text(status, style = MaterialTheme.typography.titleMedium, color = Palette.Text, textAlign = TextAlign.Center)
            if (op.stage is Operations.Stage.Wallet) {
                val link = model.walletLink()
                Spacer(Modifier.height(12.dp))
                if (link != null) {
                    QuietButton(stringResource(R.string.card_open_wallet), onClick = { openLink(link) })
                } else {
                    Note(stringResource(R.string.card_approve_elsewhere))
                }
            }
            if (op.stage is Operations.Stage.Confirming && now - op.stageSince > STILL_CONFIRMING_MS) {
                Spacer(Modifier.height(10.dp))
                Note(stringResource(R.string.card_still_confirming))
            }
            Spacer(Modifier.height(12.dp))
            TextButton(onClick = onClose, modifier = Modifier.heightIn(min = 48.dp)) {
                Text(stringResource(R.string.card_close_keeps_going), color = Palette.Secondary)
            }
        }
    }
}

@Composable
private fun Finished(model: CardModel, op: Operations.Operation, outcome: Operations.Outcome.Done, onClose: () -> Unit) {
    val s = op.summary
    fun show(units: BigInteger) = Amounts.show(units, s.decimals, s.symbol)
    val title = stringResource(
        when (s.kind) {
            Operations.Kind.SHIELD -> R.string.card_done_shield
            Operations.Kind.WITHDRAW -> R.string.card_done_withdraw
            Operations.Kind.SEND -> R.string.card_done_send
            Operations.Kind.EXIT -> R.string.card_done_exit
        },
    )
    val again = when (s.kind) {
        Operations.Kind.SHIELD -> R.string.card_again_shield
        Operations.Kind.WITHDRAW -> R.string.card_again_withdraw
        Operations.Kind.SEND -> R.string.card_again_send
        Operations.Kind.EXIT -> null
    }
    StepFrame(title, action = {
        LimeButton(stringResource(R.string.action_done), onClick = {
            model.finished()
            onClose()
        })
        if (again != null) {
            Spacer(Modifier.height(8.dp))
            QuietButton(stringResource(again), onClick = model::again)
        }
    }) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(32.dp).clip(CircleShape).background(Palette.Lime), contentAlignment = Alignment.Center) {
                Icon(CardIcons.Check, contentDescription = null, tint = Palette.OnLime, modifier = Modifier.size(20.dp))
            }
            Spacer(Modifier.width(12.dp))
            Text(title, style = MaterialTheme.typography.titleLarge, color = Palette.Text)
        }
        Spacer(Modifier.height(12.dp))
        when (s.kind) {
            Operations.Kind.SHIELD -> {
                Text(
                    stringResource(R.string.card_done_shield_body, show(s.hold ?: s.amount), s.symbol, s.chainName),
                    style = MaterialTheme.typography.bodyLarge,
                    color = Palette.Text,
                )
                Spacer(Modifier.height(8.dp))
                Line(stringResource(R.string.card_from_wallet), show(s.amount))
                s.fee?.let { Line(stringResource(R.string.card_screening_fee), show(it)) }
            }
            Operations.Kind.WITHDRAW, Operations.Kind.SEND -> {
                Line(stringResource(R.string.card_recipient_received), show(s.amount), strong = true)
                s.fee?.let { Line(stringResource(R.string.card_relayer_fee), show(it)) }
                Line(stringResource(R.string.card_network), s.chainName)
                s.recipient?.let { Line(stringResource(R.string.card_to), Address.short(it), mono = true) }
                if (s.kind == Operations.Kind.SEND) {
                    Spacer(Modifier.height(8.dp))
                    Note(stringResource(R.string.card_done_send_body))
                }
            }
            Operations.Kind.EXIT -> Text(
                stringResource(R.string.card_done_exit_body, show(s.amount), Address.short(s.recipient.orEmpty())),
                style = MaterialTheme.typography.bodyLarge,
                color = Palette.Text,
            )
        }
        outcome.txHash?.let { TxLine(it, model.chain?.explorerUrl) }
    }
}

@Composable
private fun TxLine(hash: String, explorer: String?) {
    val clipboard = LocalClipboard.current
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var copied by remember { mutableStateOf(false) }
    val label = stringResource(R.string.card_tx)
    Spacer(Modifier.height(10.dp))
    Row(verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(stringResource(R.string.card_tx), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
            Text(Address.short(hash), style = MaterialTheme.typography.bodyLarge, color = Palette.Text, fontFamily = FontFamily.Monospace)
        }
        IconButton(onClick = {
            scope.launch {
                clipboard.setClipEntry(ClipEntry(ClipData.newPlainText(label, hash)))
                copied = true
            }
        }) { Icon(CardIcons.Copy, contentDescription = stringResource(R.string.card_copy), tint = if (copied) Palette.Lime else Palette.Secondary) }
    }
    if (!explorer.isNullOrEmpty()) {
        TextButton(
            onClick = { runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, "$explorer/tx/$hash".toUri())) } },
            modifier = Modifier.heightIn(min = 48.dp),
        ) { Text(stringResource(R.string.card_explorer), color = Palette.Lime) }
    }
}

@Composable
private fun ExitPanel(model: CardModel, step: Step.Exit) {
    val asset = model.asset(step.symbol) ?: return
    val title = stringResource(R.string.exit_title)
    StepFrame(title, action = {
        LimeButton(stringResource(R.string.exit_confirm), onClick = { model.confirmExit(step) }, enabled = !model.operations.running)
    }) {
        Text(title, style = MaterialTheme.typography.titleLarge, color = Palette.Text)
        BigAmount(Amounts.show(step.deposit.value, asset.decimals, step.symbol))
        Text(stringResource(R.string.card_exit_why), style = MaterialTheme.typography.bodyLarge, color = Palette.Text)
        Spacer(Modifier.height(8.dp))
        WarningLine(stringResource(R.string.card_exit_body, Address.short(model.owner.orEmpty())))
        ErrorLine(model.draft.error)
    }
}

// --- before the account is ready -------------------------------------------------

@Composable
private fun ConnectPanel(model: CardModel, openLink: (String) -> Boolean) {
    LaunchedEffect(Unit) { model.pair() }
    val title = stringResource(R.string.connect_title)
    var noWalletHere by remember { mutableStateOf(false) }
    StepFrame(title) {
        Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
            Text(title, style = MaterialTheme.typography.titleLarge, color = Palette.Text, modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(6.dp))
            Text(stringResource(R.string.connect_explain), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
            Spacer(Modifier.height(16.dp))
            when (val c = model.connect) {
                is Connect.Waiting -> {
                    Image(
                        c.qr,
                        contentDescription = stringResource(R.string.card_qr),
                        modifier = Modifier.size(220.dp).clip(RoundedCornerShape(16.dp)).background(Color.White).padding(10.dp),
                    )
                    Spacer(Modifier.height(14.dp))
                    LimeButton(stringResource(R.string.connect_open_here), onClick = { noWalletHere = !openLink(c.uri) })
                    Spacer(Modifier.height(10.dp))
                    Note(stringResource(if (noWalletHere) R.string.connect_no_wallet_here else R.string.connect_waiting))
                }
                is Connect.Failed -> Failure(c.message, onRetry = model::pair)
                else -> Busy(null)
            }
        }
    }
}

@Composable
private fun SetupPanel(model: CardModel) {
    val title = stringResource(R.string.card_setup_title)
    val chain = model.chain?.name.orEmpty()
    val busy = model.setup as? Setup.Busy
    StepFrame(title, action = { if (busy == null) LimeButton(stringResource(R.string.card_sign), onClick = model::setUp) }) {
        Text(title, style = MaterialTheme.typography.titleLarge, color = Palette.Text)
        Spacer(Modifier.height(6.dp))
        Text(stringResource(R.string.card_setup_on, chain), style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary)
        Spacer(Modifier.height(10.dp))
        Text(stringResource(R.string.setup_explain), style = MaterialTheme.typography.bodyMedium, color = Palette.Text)
        Spacer(Modifier.height(16.dp))
        if (busy != null) {
            Busy(
                if (busy.publishing) stringResource(R.string.card_publishing, chain)
                else stringResource(R.string.card_signing, busy.signatures.coerceIn(1, 2)),
            )
        }
        (model.setup as? Setup.Failed)?.let { ErrorLine(it.message) }
    }
}

// --- pieces ----------------------------------------------------------------------

/** How the step on screen goes back, when it has somewhere to go back to. */
private val LocalBack = staticCompositionLocalOf<(() -> Unit)?> { null }

/**
 * A step: Back, its content (which scrolls when the screen is short, as the
 * cloud phone's is) and its action pinned under it, so the next move is
 * always in view.
 */
@Composable
private fun StepFrame(title: String, action: (@Composable ColumnScope.() -> Unit)? = null, body: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxWidth().then(if (title.isNotEmpty()) Modifier.semantics { paneTitle = title } else Modifier)) {
        Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) {
            LocalBack.current?.let { BackRow(it) }
            body()
        }
        if (action != null) Column(Modifier.padding(top = 16.dp), content = action)
    }
}

@Composable
private fun BackRow(onBack: () -> Unit) {
    Row(
        Modifier.padding(bottom = 8.dp).clip(CircleShape).clickable(onClick = onBack).heightIn(min = 48.dp).padding(end = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(CardIcons.Back, contentDescription = null, tint = Palette.Secondary, modifier = Modifier.size(20.dp))
        Spacer(Modifier.width(6.dp))
        Text(stringResource(R.string.card_back), style = MaterialTheme.typography.labelLarge, color = Palette.Secondary)
    }
}

@Composable
private fun BigAmount(value: String) {
    val measurer = rememberTextMeasurer()
    BoxWithConstraints(Modifier.fillMaxWidth().padding(vertical = 10.dp)) {
        val room = constraints.maxWidth
        val size = remember(value, room) {
            AMOUNT_SIZES.firstOrNull { sp ->
                measurer.measure(value, TextStyle(fontSize = sp.sp, fontWeight = FontWeight.Medium)).size.width < room
            } ?: AMOUNT_SIZES.last()
        }.sp
        Text(value, style = TextStyle(fontSize = size, fontWeight = FontWeight.Medium, color = Palette.Text), maxLines = 1)
    }
}

@Composable
private fun Line(label: String, value: String, strong: Boolean = false, mono: Boolean = false) {
    // Label and value share the row (two to three), so a long value wraps instead of crushing its label.
    Row(Modifier.fillMaxWidth().padding(vertical = 7.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(label, style = MaterialTheme.typography.bodyMedium, color = Palette.Secondary, modifier = Modifier.weight(2f))
        Spacer(Modifier.width(12.dp))
        Text(
            value,
            style = if (strong) MaterialTheme.typography.titleMedium else MaterialTheme.typography.bodyLarge,
            color = Palette.Text,
            fontFamily = if (mono) FontFamily.Monospace else null,
            textAlign = TextAlign.End,
            modifier = Modifier.weight(3f),
        )
    }
}

@Composable
private fun Note(value: String) {
    Text(value, style = MaterialTheme.typography.bodyMedium, color = Palette.Muted, modifier = Modifier.padding(vertical = 2.dp))
}

@Composable
private fun WarningLine(value: String) {
    Row(Modifier.padding(vertical = 4.dp), verticalAlignment = Alignment.Top) {
        Icon(CardIcons.Warning, contentDescription = null, tint = Palette.Warning, modifier = Modifier.size(18.dp).padding(top = 2.dp))
        Spacer(Modifier.width(8.dp))
        Text(value, style = MaterialTheme.typography.bodyMedium, color = Palette.Warning)
    }
}

@Composable
private fun ErrorLine(message: Message?) {
    if (message == null) return
    Spacer(Modifier.height(8.dp))
    Text(text(message), style = MaterialTheme.typography.bodyMedium, color = Palette.Error)
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
private fun Failure(message: Message?, onRetry: () -> Unit) {
    Column(Modifier.fillMaxWidth()) {
        ErrorLine(message)
        Spacer(Modifier.height(12.dp))
        LimeButton(stringResource(R.string.retry), onClick = onRetry)
    }
}

@Composable
private fun text(message: Message): String = stringResource(message.id, *message.args.toTypedArray())

/** The time, ticking each second while it's shown: quote countdowns, "still confirming". */
@Composable
private fun rememberNow(): Long {
    val now by produceState(System.currentTimeMillis()) {
        while (true) {
            delay(1000)
            value = System.currentTimeMillis()
        }
    }
    return now
}

/** "Remove animations" (or animations off in developer options): crossfade only. */
@Composable
private fun reducedMotion(): Boolean {
    val context = LocalContext.current
    return remember { Settings.Global.getFloat(context.contentResolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) == 0f }
}

private const val STILL_CONFIRMING_MS = 90_000L

/** The amount field's type sizes, largest first. */
private val AMOUNT_SIZES = listOf(48, 42, 36, 32, 28, 24, 20, 18)
