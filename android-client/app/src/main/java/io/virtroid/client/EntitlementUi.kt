package io.virtroid.client

import android.content.Context
import io.virtroid.client.api.EntitlementSummary
import io.virtroid.client.api.VirtroidApiException

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
        else -> startRuntimeBlockedReason ?: context.getString(R.string.entitlement_unavailable)
    }
}

internal fun Throwable.virtroidDisplayMessage(context: Context): String {
    val code = (this as? VirtroidApiException)?.code
    return when (code) {
        "runtime_entitlement_required" -> context.getString(R.string.entitlement_required)
        "runtime_quota_exceeded" -> context.getString(R.string.entitlement_runtime_quota_reached)
        "active_runtime_quota_exceeded" -> context.getString(R.string.entitlement_active_runtime_limit)
        "runtime_start_quota_exceeded" -> context.getString(R.string.entitlement_start_quota_reached)
        "runtime_profile_not_allowed" -> context.getString(R.string.entitlement_profile_not_allowed)
        "no_ready_host" -> context.getString(R.string.entitlement_no_ready_host)
        else -> message ?: context.getString(R.string.status_error)
    }
}
