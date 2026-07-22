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
        "runtime_quota_exceeded" -> context.getString(R.string.entitlement_runtime_quota_reached)
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
        else -> startRuntimeBlockedReason ?: context.getString(R.string.entitlement_unavailable)
    }
}

internal fun Throwable.virtroidDisplayMessage(context: Context): String {
    if (isIdentityAuthenticationFailure()) {
        IdentityPasswordStore(context).clearUnlocked()
    }
    val code = (this as? VirtroidApiException)?.code
    val rawMessage = message.orEmpty()
    return when (code) {
        "runtime_entitlement_required" -> context.getString(R.string.entitlement_required)
        "runtime_quota_exceeded" -> context.getString(R.string.entitlement_runtime_quota_reached)
        "active_runtime_quota_exceeded" -> context.getString(R.string.entitlement_active_runtime_limit)
        "runtime_start_quota_exceeded" -> context.getString(R.string.entitlement_start_quota_reached)
        "runtime_trial_time_exceeded" -> context.getString(R.string.entitlement_trial_time_reached)
        "runtime_storage_quota_exceeded" -> context.getString(R.string.entitlement_storage_quota_reached)
        "runtime_profile_not_allowed" -> context.getString(R.string.entitlement_profile_not_allowed)
        "no_ready_host" -> context.getString(R.string.entitlement_no_ready_host)
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
    return apiError?.errorMessage?.equals("identity authentication failed", ignoreCase = true) == true ||
        message.orEmpty().equals("identity authentication failed", ignoreCase = true)
}

internal fun Throwable.isGoneSessionResponse(): Boolean {
    val apiError = this as? VirtroidApiException
    return apiError?.code == "session_not_found" ||
        (apiError?.statusCode == 401 && apiError.errorMessage.contains("runtime capability", ignoreCase = true)) ||
        message.orEmpty().contains("session not found", ignoreCase = true)
}
