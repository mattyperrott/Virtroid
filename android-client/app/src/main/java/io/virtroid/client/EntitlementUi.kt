package io.virtroid.client

import android.content.Context
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.VirtroidApiException
import io.virtroid.client.security.IdentityPasswordStore

internal fun EntitlementSummary.createRuntimeBlockedMessage(context: Context): String? {
    if (canCreateRuntime) {
        return null
    }
    return when (createRuntimeBlockedCode) {
        "runtime_entitlement_required" -> context.getString(R.string.entitlement_required)
        "runtime_quota_exceeded" -> context.getString(
            if (isTrial()) R.string.entitlement_trial_runtime_limit else R.string.entitlement_runtime_quota_reached,
        )
        else -> createRuntimeBlockedReason ?: context.getString(R.string.entitlement_unavailable)
    }
}

internal fun EntitlementSummary.startRuntimeBlockedMessage(context: Context): String? {
    if (canStartRuntime) {
        return null
    }
    return when (startRuntimeBlockedCode) {
        "runtime_entitlement_required" -> context.getString(R.string.entitlement_required)
        "active_runtime_quota_exceeded" -> context.getString(R.string.entitlement_active_runtime_limit)
        "runtime_start_quota_exceeded" -> context.getString(R.string.entitlement_start_quota_reached)
        "runtime_trial_time_exceeded" -> context.getString(R.string.entitlement_trial_time_reached)
        RUNTIME_BALANCE_INSUFFICIENT_CODE -> context.getString(R.string.entitlement_balance_insufficient)
        else -> startRuntimeBlockedReason ?: context.getString(R.string.entitlement_unavailable)
    }
}

internal const val RUNTIME_BALANCE_INSUFFICIENT_CODE = "runtime_balance_insufficient"

/** Whether adding USDT in the wallet lets the account start a runtime again. */
internal fun EntitlementSummary.startNeedsDeposit(): Boolean =
    !canStartRuntime && startRuntimeBlockedCode in setOf("runtime_trial_time_exceeded", RUNTIME_BALANCE_INSUFFICIENT_CODE)

/** Whether adding USDT in the wallet lets the account create another runtime. */
internal fun EntitlementSummary.createNeedsDeposit(): Boolean =
    !canCreateRuntime && isTrial() && createRuntimeBlockedCode == "runtime_quota_exceeded"

internal fun EntitlementSummary.isTrial(): Boolean = source.equals("trial", ignoreCase = true)

/** The home screen's trial banner, or null when the account is not on trial. */
internal fun EntitlementSummary.trialBanner(context: Context): String? {
    if (!isTrial()) {
        return null
    }
    if (trialRuntimeSecondsRemaining <= 0) {
        return context.getString(R.string.trial_banner_used_up)
    }
    return context.getString(R.string.trial_banner_time_left, trialMinutes(context, trialRuntimeSecondsRemaining))
}

/** The account screen's Wallet line: the trial's time left or the balance. */
internal fun EntitlementSummary.walletSubtitle(context: Context): String {
    val balance = balanceMicro
    return when {
        isTrial() && trialRuntimeSecondsRemaining > 0 ->
            context.getString(R.string.account_wallet_trial, trialMinutes(context, trialRuntimeSecondsRemaining))
        isTrial() -> context.getString(R.string.account_wallet_trial_used_up)
        balance != null -> context.getString(R.string.account_wallet_balance, WalletActivity.formatUsdt(balance))
        else -> context.getString(R.string.account_wallet_subtitle)
    }
}

/** Running time left, in whole minutes rounded up so a started minute still shows. */
internal fun trialMinutes(context: Context, seconds: Long): String {
    val minutes = ((seconds + 59) / 60).toInt()
    return context.resources.getQuantityString(R.plurals.trial_minutes, minutes, minutes)
}

internal fun Throwable.virtroidDisplayMessage(context: Context): String {
    if (isIdentityAuthenticationFailure()) {
        IdentityPasswordStore(context).clearUnlocked()
        return context.getString(R.string.identity_password_incorrect)
    }
    val code = (this as? VirtroidApiException)?.code
    val rawMessage = message.orEmpty()
    return when (code) {
        "runtime_entitlement_required" -> context.getString(R.string.entitlement_required)
        "runtime_quota_exceeded" -> context.getString(R.string.entitlement_runtime_quota_reached)
        "active_runtime_quota_exceeded" -> context.getString(R.string.entitlement_active_runtime_limit)
        "runtime_start_quota_exceeded" -> context.getString(R.string.entitlement_start_quota_reached)
        "runtime_trial_time_exceeded" -> context.getString(R.string.entitlement_trial_time_reached)
        RUNTIME_BALANCE_INSUFFICIENT_CODE -> context.getString(R.string.entitlement_balance_insufficient)
        "runtime_storage_quota_exceeded" -> context.getString(R.string.entitlement_storage_quota_reached)
        "runtime_profile_not_allowed" -> context.getString(R.string.entitlement_profile_not_allowed)
        "runtime_egress_active" -> context.getString(R.string.controls_tor_stop_first)
        "no_ready_host" -> context.getString(R.string.entitlement_no_ready_host)
        RUNTIME_HOST_PROVISIONING_CODE -> context.getString(R.string.runtime_host_provisioning)
        "runtime_host_capacity" -> context.getString(R.string.runtime_host_capacity)
        "last_active_device" -> context.getString(R.string.account_revoke_last_device_blocked)
        "bootstrap_invite_required" -> context.getString(R.string.onboarding_invite_required)
        "bootstrap_invite_invalid" -> context.getString(R.string.onboarding_invite_invalid)
        "account_recovery_unavailable" -> context.getString(R.string.identity_recovery_unavailable)
        else -> when {
            rawMessage.contains("viewer prepare", ignoreCase = true) ||
                rawMessage.contains("viewer service", ignoreCase = true) ||
                rawMessage.contains("runtime stream timed out", ignoreCase = true) -> context.getString(R.string.session_prepare_retry_message)
            rawMessage.contains("timed out", ignoreCase = true) ||
                rawMessage.contains("timeout", ignoreCase = true) -> context.getString(R.string.runtime_start_timeout)
            else -> rawMessage.ifBlank { context.getString(R.string.status_error) }
        }
    }
}

internal fun Throwable.isIdentityAuthenticationFailure(): Boolean {
    val apiError = this as? VirtroidApiException
    return apiError?.code in setOf("account_recovery_key_mismatch", "account_recovery_proof_invalid") ||
        apiError?.errorMessage?.equals("identity authentication failed", ignoreCase = true) == true ||
        message.orEmpty().equals("identity authentication failed", ignoreCase = true) ||
        message.orEmpty().equals("blob encryption password is incorrect", ignoreCase = true)
}

internal fun Throwable.isGoneSessionResponse(): Boolean {
    val apiError = this as? VirtroidApiException
    return apiError?.code == "session_not_found" ||
        (apiError?.statusCode == 401 && apiError.errorMessage.contains("runtime capability", ignoreCase = true)) ||
        message.orEmpty().contains("session not found", ignoreCase = true)
}
