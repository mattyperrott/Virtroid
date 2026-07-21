package io.virtroid.client.api

import android.content.Context
import io.virtroid.client.BuildConfig
import io.virtroid.client.device.DeviceRuntimeProfile
import io.virtroid.client.security.BlobKeyEnvelopeCrypto
import io.virtroid.client.security.BlobKeyLease
import io.virtroid.client.security.DeviceIdentityStore
import io.virtroid.client.security.IdentityCrypto
import io.virtroid.client.security.RuntimeCapabilityStore
import io.virtroid.client.security.TlsPins
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.ResponseBody
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.util.concurrent.TimeUnit

data class RuntimeSummary(
    val id: String,
    val name: String,
    val status: String,
    val desiredState: String,
    val connectionStatus: String,
    val hostId: String?,
    val personaVersion: Int,
    val androidImage: String,
    val androidVersion: String,
    val widthPx: Int,
    val heightPx: Int,
    val densityDpi: Int,
    val audioEnabled: Boolean,
    val cameraMode: String,
    val fileMode: String,
    val blobAutoSnapshot: Boolean,
    val blobRetainDays: Int,
    val blobLastSnapshotAt: String?,
    val blobSnapshotId: String?,
    val blobSnapshotGeneration: Long,
    val startedAt: String?,
    val loadAverage: Double?,
    val adbPort: Int?,
    val viewerPort: Int?,
    val lastError: String?,
    val personaBrand: String?,
    val personaModel: String?,
    val personaManufacturer: String?,
    val personaRelease: String?,
    val personaFingerprint: String?,
)

data class SessionLaunch(
    val sessionId: String,
    val relayHost: String,
    val relayPort: Int,
    val relayTls: Boolean,
    val relayPath: String,
    val relayToken: String,
    val viewerPublicKey: String,
    val viewerAddress: String,
)

data class SessionState(
    val sessionId: String,
    val runtimeId: String,
    val status: String,
    val effectiveStatus: String,
    val canResume: Boolean,
    val runtimeReady: Boolean,
    val isExpired: Boolean,
    val endedAt: String?,
    val endReason: String?,
    val runtime: RuntimeSummary?,
) {
    fun canResumeRuntime(expectedRuntimeId: String): Boolean {
        val currentRuntime = runtime ?: return false
        return canResume &&
            runtimeId == expectedRuntimeId &&
            currentRuntime.status.equals("running", ignoreCase = true) &&
            currentRuntime.desiredState.equals("running", ignoreCase = true) &&
            currentRuntime.connectionStatus.equals("online", ignoreCase = true) &&
            !currentRuntime.hostId.isNullOrBlank()
    }
}

data class RuntimeState(
    val runtime: RuntimeSummary,
    val effectiveState: String,
    val runtimeReady: Boolean,
    val hasActiveSession: Boolean,
    val hasCurrentDeviceSession: Boolean,
    val currentDeviceSessionId: String?,
    val canConnect: Boolean,
    val canStart: Boolean,
    val canStop: Boolean,
    val canWipe: Boolean,
    val canDelete: Boolean,
    val isBusy: Boolean,
    val blockedReason: String?,
) {
    fun canConnectRuntime(expectedRuntimeId: String): Boolean {
        return canConnect && runtimeReady && runtime.id == expectedRuntimeId
    }
}

data class RuntimeUpdate(
    val name: String,
    val androidImage: String,
    val androidVersion: String,
    val widthPx: Int,
    val heightPx: Int,
    val densityDpi: Int,
    val audioEnabled: Boolean,
    val cameraMode: String,
    val fileMode: String,
    val blobAutoSnapshot: Boolean,
    val blobRetainDays: Int,
)

data class RuntimeLogEntry(
    val level: String,
    val source: String,
    val message: String,
    val createdAt: String,
)

data class BootstrapResult(
    val accountId: String,
    val deviceId: String,
    val runtimeId: String,
)

data class AccountStorage(
    val provider: String,
    val fundingModel: String,
    val walletAddress: String?,
    val fundingAddress: String?,
    val status: String,
    val encryptedSeedBackedUp: Boolean,
    val lastPreflightStatus: String?,
    val lastPreflightJson: String?,
    val lastPreflightAt: String?,
)

data class TrustedDeviceSummary(
    val id: String,
    val name: String,
    val publicKey: String,
    val createdAt: String,
    val lastSeenAt: String?,
    val revokedAt: String?,
)

data class EntitlementSummary(
    val accountId: String,
    val source: String,
    val status: String,
    val runtimeLimit: Int,
    val runtimeCount: Int,
    val runtimeRemaining: Int,
    val activeRuntimeLimit: Int,
    val activeRuntimeCount: Int,
    val activeRuntimeRemaining: Int,
    val runtimeStartsPerDay: Int,
    val runtimeStartsUsedToday: Int,
    val runtimeStartsRemainingToday: Int,
    val storageBytesLimit: Long,
    val storageBytesUsed: Long,
    val storageBytesRemaining: Long,
    val trialRuntimeSeconds: Int,
    val trialRuntimeSecondsUsed: Long,
    val trialRuntimeSecondsRemaining: Long,
    val expiresAt: String?,
    val canCreateRuntime: Boolean,
    val canStartRuntime: Boolean,
    val createRuntimeBlockedCode: String?,
    val createRuntimeBlockedReason: String?,
    val startRuntimeBlockedCode: String?,
    val startRuntimeBlockedReason: String?,
)

data class AppCatalogEntry(
    val packageName: String,
    val source: String,
    val displayName: String,
    val summary: String,
    val iconUrl: String?,
    val versionName: String,
    val versionCode: Long,
    val apkSizeBytes: Long,
    val minSdk: Int,
    val nativeCode: String?,
    val recommended: Boolean,
    val selected: Boolean,
    val catalogUpdatedAt: String?,
)

class VirtroidApiException(
    val statusCode: Int,
    val code: String?,
    val errorMessage: String,
) : IOException(errorMessage)

private val DEFAULT_HTTP_CLIENT: OkHttpClient = OkHttpClient.Builder()
    .certificatePinner(TlsPins.certificatePinner())
    .connectTimeout(15, TimeUnit.SECONDS)
    .readTimeout(90, TimeUnit.SECONDS)
    .writeTimeout(30, TimeUnit.SECONDS)
    .callTimeout(120, TimeUnit.SECONDS)
    .build()

class VirtroidApi(
    private val okHttpClient: OkHttpClient = DEFAULT_HTTP_CLIENT,
    private val deviceIdentityStore: DeviceIdentityStore = DeviceIdentityStore(),
    private val runtimeCapabilityStore: RuntimeCapabilityStore = RuntimeCapabilityStore(),
) {
    fun clearLocalRuntimeCapabilities(context: Context) {
        RuntimeCapabilityStore.clearAllRegistered(context)
        synchronized(registeredRuntimeCapabilities) {
            registeredRuntimeCapabilities.clear()
        }
    }

    suspend fun registerIdentity(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        blobKeyVerifier: String,
    ) = withContext(Dispatchers.IO) {
        val requestBody = JSONObject()
            .put("account_id", accountId)
            .put("device_id", deviceId)
            .put("blob_key_verifier", blobKeyVerifier)
            .toString()

        executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/identity/register",
                method = "POST",
                accountId = accountId,
                deviceId = deviceId,
                body = requestBody,
            ),
        )
    }

    suspend fun bootstrap(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        deviceName: String,
        publicKey: String,
        runtimeProfile: DeviceRuntimeProfile,
    ): BootstrapResult =
        withContext(Dispatchers.IO) {
            val requestJson = JSONObject()
                .put("account_id", accountId)
                .put("device_id", deviceId)
                .put("device_name", deviceName)
                .put("public_key", publicKey)
                .put("runtime_name", runtimeProfile.runtimeName)
                .put("width_px", runtimeProfile.widthPx)
                .put("height_px", runtimeProfile.heightPx)
                .put("density_dpi", runtimeProfile.densityDpi)
                .toString()
            val requestBytes = requestJson.toByteArray(Charsets.UTF_8)
            val requestBody = requestJson.toRequestBody(JSON_MEDIA_TYPE)

            val requestBuilder = Request.Builder()
                .url(normalizeBaseUrl(baseUrl) + "/api/v1/bootstrap")
                .post(requestBody)

            deviceIdentityStore.signedHeaders(
                method = "POST",
                requestUri = "/api/v1/bootstrap",
                accountId = accountId,
                deviceId = deviceId,
                body = requestBytes,
            ).forEach { (name, value) -> requestBuilder.header(name, value) }

            val payload = executeJson(requestBuilder.build())

            BootstrapResult(
                accountId = payload.getJSONObject("account").getString("id"),
                deviceId = payload.getJSONObject("device").getString("id"),
                runtimeId = payload.optJSONObject("runtime")?.optString("id").orEmpty(),
            )
        }

    suspend fun listRuntimes(baseUrl: String, accountId: String, deviceId: String): List<RuntimeSummary> =
        withContext(Dispatchers.IO) {
            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/runtimes?account_id=$accountId&device_id=$deviceId",
                    method = "GET",
                    accountId = accountId,
                    deviceId = deviceId,
                ),
            )

            val items = payload.optJSONArray("items") ?: return@withContext emptyList()
            List(items.length()) { index -> items.getJSONObject(index).toRuntimeSummary() }
        }

    suspend fun listRuntimeStates(baseUrl: String, accountId: String, deviceId: String): List<RuntimeState> =
        withContext(Dispatchers.IO) {
            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/runtimes/state?account_id=$accountId&device_id=$deviceId",
                    method = "GET",
                    accountId = accountId,
                    deviceId = deviceId,
                ),
            )

            val items = payload.optJSONArray("items") ?: return@withContext emptyList()
            List(items.length()) { index -> items.getJSONObject(index).toRuntimeState() }
        }

    suspend fun getEntitlement(baseUrl: String, accountId: String, deviceId: String): EntitlementSummary =
        withContext(Dispatchers.IO) {
            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/entitlement?account_id=$accountId&device_id=$deviceId",
                    method = "GET",
                    accountId = accountId,
                    deviceId = deviceId,
                ),
            )

            payload.toEntitlementSummary()
        }

    suspend fun listAppCatalog(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        search: String = "",
    ): List<AppCatalogEntry> =
        withContext(Dispatchers.IO) {
            val query = buildString {
                append("account_id=").append(accountId)
                append("&device_id=").append(deviceId)
                if (search.isNotBlank()) {
                    append("&search=").append(java.net.URLEncoder.encode(search, "UTF-8"))
                }
            }
            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/apps/catalog?$query",
                    method = "GET",
                    accountId = accountId,
                    deviceId = deviceId,
                ),
            )

            payload.toAppCatalogEntries()
        }

    suspend fun listPublicAppCatalog(
        baseUrl: String,
        search: String = "",
    ): List<AppCatalogEntry> =
        withContext(Dispatchers.IO) {
            val path = if (search.isBlank()) {
                "/api/v1/apps/catalog"
            } else {
                "/api/v1/apps/catalog?search=${java.net.URLEncoder.encode(search, "UTF-8")}"
            }
            val payload = executeJson(
                Request.Builder()
                    .url(normalizeBaseUrl(baseUrl) + path)
                    .get()
                    .build(),
            )

            payload.toAppCatalogEntries()
        }

    suspend fun updateAppSelections(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        packageNames: Set<String>,
    ): List<AppCatalogEntry> =
        withContext(Dispatchers.IO) {
            val packages = JSONArray()
            packageNames.sorted().forEach { packages.put(it) }
            val requestBody = JSONObject()
                .put("account_id", accountId)
                .put("device_id", deviceId)
                .put("package_names", packages)
                .toString()

            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/apps/selections",
                    method = "PUT",
                    accountId = accountId,
                    deviceId = deviceId,
                    body = requestBody,
                ),
            )

            payload.toAppCatalogEntries()
        }

    suspend fun getStorage(baseUrl: String, accountId: String, deviceId: String): AccountStorage =
        withContext(Dispatchers.IO) {
            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/storage?account_id=$accountId&device_id=$deviceId",
                    method = "GET",
                    accountId = accountId,
                    deviceId = deviceId,
                ),
            )

            payload.toAccountStorage()
        }

    suspend fun createRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        name: String,
        runtimeProfile: DeviceRuntimeProfile,
        audioEnabled: Boolean,
        cameraMode: String,
        fileMode: String,
        blobAutoSnapshot: Boolean,
        blobRetainDays: Int,
    ): RuntimeSummary =
        withContext(Dispatchers.IO) {
            val requestBody = JSONObject()
                .put("account_id", accountId)
                .put("device_id", deviceId)
                .put("name", name.ifBlank { runtimeProfile.runtimeName })
                .put("width_px", runtimeProfile.widthPx)
                .put("height_px", runtimeProfile.heightPx)
                .put("density_dpi", runtimeProfile.densityDpi)
                .put("audio_enabled", audioEnabled)
                .put("camera_mode", cameraMode)
                .put("file_mode", fileMode)
                .put("blob_auto_snapshot", blobAutoSnapshot)
                .put("blob_retain_days", blobRetainDays)
                .toString()

            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/runtimes",
                    method = "POST",
                    accountId = accountId,
                    deviceId = deviceId,
                    body = requestBody,
                ),
            )

            payload.toRuntimeSummary()
        }

    suspend fun updateRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        update: RuntimeUpdate,
    ): RuntimeSummary = withContext(Dispatchers.IO) {
        val requestBody = JSONObject()
            .put("account_id", accountId)
            .put("device_id", deviceId)
            .put("name", update.name)
            .put("android_image", update.androidImage)
            .put("android_version", update.androidVersion)
            .put("width_px", update.widthPx)
            .put("height_px", update.heightPx)
            .put("density_dpi", update.densityDpi)
            .put("audio_enabled", update.audioEnabled)
            .put("camera_mode", update.cameraMode)
            .put("file_mode", update.fileMode)
            .put("blob_auto_snapshot", update.blobAutoSnapshot)
            .put("blob_retain_days", update.blobRetainDays)
            .toString()

        val payload = executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId",
                method = "PATCH",
                accountId = accountId,
                deviceId = deviceId,
                body = requestBody,
            ),
        )

        payload.toRuntimeSummary()
    }

    suspend fun startRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        blobAccessKey: String,
    ): RuntimeSummary =
        mutateRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey, "start")

    suspend fun stopRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        blobAccessKey: String,
    ): RuntimeSummary =
        mutateRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey, "stop")

    suspend fun wipeRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        blobAccessKey: String,
    ): RuntimeSummary =
        mutateRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey, "wipe")

    suspend fun deleteRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        blobAccessKey: String,
    ): RuntimeSummary =
        mutateRuntime(baseUrl, accountId, deviceId, runtimeId, blobAccessKey, "delete")

    suspend fun getRuntimeState(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
    ): RuntimeState = withContext(Dispatchers.IO) {
        val payload = executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/state?account_id=$accountId&device_id=$deviceId",
                method = "GET",
                accountId = accountId,
                deviceId = deviceId,
            ),
        )

        payload.toRuntimeState()
    }

    suspend fun listRuntimeLogs(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        limit: Int = 8,
    ): List<RuntimeLogEntry> = withContext(Dispatchers.IO) {
        val payload = executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/logs?account_id=$accountId&device_id=$deviceId&limit=$limit",
                method = "GET",
                accountId = accountId,
                deviceId = deviceId,
            ),
        )

        val items = payload.optJSONArray("items") ?: return@withContext emptyList()
        List(items.length()) { index ->
            items.getJSONObject(index).let { item ->
                RuntimeLogEntry(
                    level = item.optString("level"),
                    source = item.optString("source"),
                    message = item.optString("message"),
                    createdAt = item.optString("created_at"),
                )
            }
        }
    }

    suspend fun createSession(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        maxSize: Int,
        bitRate: Int,
        blobAccessKey: String,
    ): SessionLaunch = withContext(Dispatchers.IO) {
        val blobKeyVerifier = IdentityCrypto.blobKeyVerifier(blobAccessKey)
        val lease = requestBlobKeyLease(baseUrl, accountId, deviceId, runtimeId, "session", blobKeyVerifier)
        val envelope = BlobKeyEnvelopeCrypto.encryptBlobAccessKey(blobAccessKey, lease)
        val requestBody = JSONObject()
            .put("max_size", maxSize)
            .put("bit_rate", bitRate)
            .put("blob_key_verifier", blobKeyVerifier)
            .put("blob_key_envelope", envelope)
            .toString()

        val payload = executeJson(
            capabilityJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/session",
                method = "POST",
                runtimeId = runtimeId,
                body = requestBody,
            ),
        )

        val relayTls = payload.optBoolean(
            "relay_tls",
            payload.optString("relay_scheme").equals("tls", ignoreCase = true),
        )
        if (!relayTls && !BuildConfig.DEBUG) {
            throw IOException("insecure relay transport rejected")
        }
        val viewerPublicKey = payload.optString("viewer_public_key").ifBlank {
            throw IOException("viewer identity key is required")
        }

        SessionLaunch(
            sessionId = payload.getJSONObject("session").getString("id"),
            relayHost = payload.optString("relay_host").ifBlank { payload.getString("viewer_host") },
            relayPort = payload.optInt("relay_port").takeIf { it > 0 } ?: payload.getInt("viewer_port"),
            relayTls = relayTls,
            relayPath = payload.optString("relay_path").ifBlank {
                "/api/v1/relay/${payload.getJSONObject("session").getString("id")}"
            },
            relayToken = payload.getJSONObject("session").getString("relay_token"),
            viewerPublicKey = viewerPublicKey,
            viewerAddress = payload.getString("viewer_address"),
        )
    }

    suspend fun deleteAccount(
        baseUrl: String,
        accountId: String,
        deviceId: String,
    ) = withContext(Dispatchers.IO) {
        executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me?account_id=$accountId&device_id=$deviceId",
                method = "DELETE",
                accountId = accountId,
                deviceId = deviceId,
            ),
        )
    }

    suspend fun listDevices(baseUrl: String, accountId: String, deviceId: String): List<TrustedDeviceSummary> =
        withContext(Dispatchers.IO) {
            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/devices?account_id=$accountId&device_id=$deviceId",
                    method = "GET",
                    accountId = accountId,
                    deviceId = deviceId,
                ),
            )
            val items = payload.optJSONArray("items") ?: return@withContext emptyList()
            List(items.length()) { index ->
                val item = items.getJSONObject(index)
                TrustedDeviceSummary(
                    id = item.getString("id"),
                    name = item.optString("name").ifBlank { "Linked device" },
                    publicKey = item.optString("public_key"),
                    createdAt = item.optString("created_at"),
                    lastSeenAt = item.nullableString("last_seen_at"),
                    revokedAt = item.nullableString("revoked_at"),
                )
            }
        }

    suspend fun revokeDevice(
        baseUrl: String,
        accountId: String,
        signingDeviceId: String,
        targetDeviceId: String,
    ) = withContext(Dispatchers.IO) {
        executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/devices/$targetDeviceId?account_id=$accountId&device_id=$signingDeviceId",
                method = "DELETE",
                accountId = accountId,
                deviceId = signingDeviceId,
            ),
        )
    }

    suspend fun closeSession(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        sessionId: String,
    ) = withContext(Dispatchers.IO) {
        executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/sessions/$sessionId/close",
                method = "POST",
                accountId = accountId,
                deviceId = deviceId,
            ),
        )
    }

    suspend fun getSessionState(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        sessionId: String,
    ): SessionState = withContext(Dispatchers.IO) {
        val payload = executeJson(
            capabilityJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/sessions/$sessionId?runtime_id=$runtimeId",
                method = "GET",
                runtimeId = runtimeId,
            ),
        )
        payload.toSessionState()
    }

    suspend fun issueSessionRelayToken(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        sessionId: String,
    ): String = withContext(Dispatchers.IO) {
        val payload = executeJson(
            capabilityJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/sessions/$sessionId/relay-token?runtime_id=$runtimeId",
                method = "POST",
                runtimeId = runtimeId,
            ),
        )
        payload.getJSONObject("session").getString("relay_token")
    }

    suspend fun endSession(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        sessionId: String,
        blobAccessKey: String,
    ): RuntimeSummary = withContext(Dispatchers.IO) {
        val blobKeyVerifier = IdentityCrypto.blobKeyVerifier(blobAccessKey)
        val lease = requestBlobKeyLease(baseUrl, accountId, deviceId, runtimeId, "stop", blobKeyVerifier)
        val envelope = BlobKeyEnvelopeCrypto.encryptBlobAccessKey(blobAccessKey, lease)
        val requestBody = JSONObject()
            .put("blob_key_verifier", blobKeyVerifier)
            .put("blob_key_envelope", envelope)
            .toString()

        executeJson(
            capabilityJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/sessions/$sessionId/end?runtime_id=$runtimeId",
                method = "POST",
                runtimeId = runtimeId,
                body = requestBody,
            ),
        ).toRuntimeSummary()
    }

    suspend fun heartbeatSession(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        sessionId: String,
    ) = withContext(Dispatchers.IO) {
        executeJson(
            capabilityJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/sessions/$sessionId/heartbeat?runtime_id=$runtimeId",
                method = "POST",
                runtimeId = runtimeId,
            ),
        )
    }

    private fun requestBlobKeyLease(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        operation: String,
        blobKeyVerifier: String,
    ): BlobKeyLease {
        return try {
            requestBlobKeyLeaseOnce(baseUrl, accountId, deviceId, runtimeId, operation, blobKeyVerifier)
        } catch (error: VirtroidApiException) {
            if (!error.isRuntimeCapabilityInvalid()) {
                throw error
            }
            requestBlobKeyLeaseOnce(
                baseUrl = baseUrl,
                accountId = accountId,
                deviceId = deviceId,
                runtimeId = runtimeId,
                operation = operation,
                blobKeyVerifier = blobKeyVerifier,
                rotateCapability = true,
            )
        }
    }

    private fun requestBlobKeyLeaseOnce(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        operation: String,
        blobKeyVerifier: String,
        rotateCapability: Boolean = false,
    ): BlobKeyLease {
        ensureRuntimeCapability(baseUrl, accountId, deviceId, runtimeId, rotateCapability = rotateCapability)
        val requestBody = JSONObject()
            .put("operation", operation)
            .put("blob_key_verifier", blobKeyVerifier)
            .toString()

        val payload = executeJson(
            capabilityJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/blob-key-lease",
                method = "POST",
                runtimeId = runtimeId,
                body = requestBody,
            ),
        )

        return BlobKeyLease(
            leaseId = payload.getString("lease_id"),
            runtimeId = payload.getString("runtime_id"),
            hostId = payload.getString("host_id"),
            operation = payload.getString("operation"),
            algorithm = payload.getString("algorithm"),
            nodePublicKey = payload.getString("node_public_key"),
        )
    }

    private fun ensureRuntimeCapability(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        rotateCapability: Boolean = false,
    ) {
        if (rotateCapability) {
            forgetRuntimeCapability(baseUrl, accountId, runtimeId)
        }
        val publicKey = if (rotateCapability) {
            runtimeCapabilityStore.rotate(runtimeId)
        } else {
            runtimeCapabilityStore.publicKeyMaterial(runtimeId)
        }
        val capabilityId = runtimeCapabilityStore.capabilityId(runtimeId, publicKey)
        val cacheKey = runtimeCapabilityCacheKey(baseUrl, accountId, runtimeId, capabilityId)
        synchronized(registeredRuntimeCapabilities) {
            if (registeredRuntimeCapabilities.contains(cacheKey)) {
                return
            }
        }

        val requestBody = JSONObject()
            .put("account_id", accountId)
            .put("device_id", deviceId)
            .put("capability_id", capabilityId)
            .put("public_key", publicKey)
            .toString()
        executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/capability",
                method = "POST",
                accountId = accountId,
                deviceId = deviceId,
                body = requestBody,
            ),
        )

        synchronized(registeredRuntimeCapabilities) {
            registeredRuntimeCapabilities.add(cacheKey)
        }
    }

    private fun forgetRuntimeCapability(baseUrl: String, accountId: String, runtimeId: String) {
        val prefix = "${normalizeBaseUrl(baseUrl)}|$accountId|$runtimeId|"
        synchronized(registeredRuntimeCapabilities) {
            registeredRuntimeCapabilities.removeAll { it.startsWith(prefix) }
        }
    }

    private fun runtimeCapabilityCacheKey(
        baseUrl: String,
        accountId: String,
        runtimeId: String,
        capabilityId: String,
    ): String = "${normalizeBaseUrl(baseUrl)}|$accountId|$runtimeId|$capabilityId"

    private suspend fun mutateRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        blobAccessKey: String,
        action: String,
    ): RuntimeSummary = withContext(Dispatchers.IO) {
        val blobKeyVerifier = IdentityCrypto.blobKeyVerifier(blobAccessKey)
        val lease = requestBlobKeyLease(baseUrl, accountId, deviceId, runtimeId, action, blobKeyVerifier)
        val envelope = BlobKeyEnvelopeCrypto.encryptBlobAccessKey(blobAccessKey, lease)
        val requestBody = JSONObject()
            .put("blob_key_verifier", blobKeyVerifier)
            .put("blob_key_envelope", envelope)
            .toString()

        val payload = executeJson(
            capabilityJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/$action",
                method = "POST",
                runtimeId = runtimeId,
                body = requestBody,
            ),
        )

        payload.toRuntimeSummary()
    }

    private fun signedJsonRequest(
        baseUrl: String,
        pathAndQuery: String,
        method: String,
        accountId: String,
        deviceId: String,
        body: String = "",
    ): Request {
        val normalizedMethod = method.uppercase()
        val signedBody = when {
            body.isNotBlank() -> body
            normalizedMethod in METHODS_REQUIRING_REQUEST_BODY -> "{}"
            else -> ""
        }
        val bodyBytes = signedBody.toByteArray(Charsets.UTF_8)
        val requestBody = if (signedBody.isBlank()) null else signedBody.toRequestBody(JSON_MEDIA_TYPE)
        val builder = Request.Builder()
            .url(normalizeBaseUrl(baseUrl) + pathAndQuery)
            .method(normalizedMethod, requestBody)

        deviceIdentityStore.signedHeaders(
            method = method,
            requestUri = pathAndQuery,
            accountId = accountId,
            deviceId = deviceId,
            body = bodyBytes,
        ).forEach { (name, value) -> builder.header(name, value) }

        return builder.build()
    }

    private fun capabilityJsonRequest(
        baseUrl: String,
        pathAndQuery: String,
        method: String,
        runtimeId: String,
        body: String = "",
    ): Request {
        val normalizedMethod = method.uppercase()
        val signedBody = when {
            body.isNotBlank() -> body
            normalizedMethod in METHODS_REQUIRING_REQUEST_BODY -> "{}"
            else -> ""
        }
        val bodyBytes = signedBody.toByteArray(Charsets.UTF_8)
        val requestBody = if (signedBody.isBlank()) null else signedBody.toRequestBody(JSON_MEDIA_TYPE)
        val builder = Request.Builder()
            .url(normalizeBaseUrl(baseUrl) + pathAndQuery)
            .method(normalizedMethod, requestBody)

        runtimeCapabilityStore.signedHeaders(
            method = method,
            requestUri = pathAndQuery,
            runtimeId = runtimeId,
            body = bodyBytes,
        ).forEach { (name, value) -> builder.header(name, value) }

        return builder.build()
    }

    private fun executeJson(request: Request): JSONObject {
        okHttpClient.newCall(request).execute().use { response ->
            val responseBody = response.body
            val contentType = responseBody?.contentType()
            val body = readBoundedResponseBody(responseBody)
            if (!response.isSuccessful) {
                val errorPayload = parseError(body)
                throw VirtroidApiException(
                    statusCode = response.code,
                    code = errorPayload.first,
                    errorMessage = errorPayload.second,
                )
            }
            val isJson = contentType?.let {
                it.type.equals("application", ignoreCase = true) &&
                    (it.subtype.equals("json", ignoreCase = true) || it.subtype.endsWith("+json", ignoreCase = true))
            } == true
            if (!isJson) {
                throw IOException("API response Content-Type must be application/json")
            }
            return JSONObject(body)
        }
    }

    private fun readBoundedResponseBody(responseBody: ResponseBody?): String {
        val body = responseBody ?: throw IOException("API response body is missing")
        val declaredLength = body.contentLength()
        if (declaredLength > MAX_JSON_RESPONSE_BYTES) {
            throw IOException("API response exceeded the JSON size limit")
        }
        val initialSize = declaredLength
            .takeIf { it in 1L..MAX_JSON_RESPONSE_BYTES }
            ?.toInt()
            ?: 8_192
        val output = ByteArrayOutputStream(initialSize)
        body.byteStream().use { input ->
            val buffer = ByteArray(8_192)
            var total = 0L
            while (true) {
                val read = input.read(buffer)
                if (read < 0) {
                    break
                }
                total += read
                if (total > MAX_JSON_RESPONSE_BYTES) {
                    throw IOException("API response exceeded the JSON size limit")
                }
                output.write(buffer, 0, read)
            }
        }
        return output.toString(Charsets.UTF_8.name())
    }

    private fun parseError(body: String): Pair<String?, String> {
        return runCatching {
            val payload = JSONObject(body)
            payload.nullableString("code") to
                payload.optString("error").ifBlank { body }.take(MAX_ERROR_MESSAGE_CHARS)
        }.getOrDefault(null to body.take(MAX_ERROR_MESSAGE_CHARS))
    }

    private fun VirtroidApiException.isRuntimeCapabilityInvalid(): Boolean {
        return statusCode == 401 && errorMessage.contains("runtime capability", ignoreCase = true)
    }

    private fun normalizeBaseUrl(baseUrl: String): String = baseUrl.trim().trimEnd('/')

    private fun JSONObject.toRuntimeSummary(): RuntimeSummary {
        val persona = parsePersona()
        val blobManifest = optJSONObject("blob_manifest_json") ?: optString("blob_manifest_json")
            .takeIf { it.isNotBlank() }
            ?.let { raw -> runCatching { JSONObject(raw) }.getOrNull() }
        return RuntimeSummary(
            id = getString("id"),
            name = optString("name").ifBlank { "Runtime" },
            status = optString("status", ""),
            desiredState = optString("desired_state", ""),
            connectionStatus = optString("connection_status", ""),
            hostId = nullableString("host_id"),
            personaVersion = optInt("persona_version", 0),
            androidImage = optString("android_image"),
            androidVersion = optString("android_version"),
            widthPx = optInt("width_px", 720),
            heightPx = optInt("height_px", 1600),
            densityDpi = optInt("density_dpi", 320),
            audioEnabled = optBoolean("audio_enabled", true),
            cameraMode = optString("camera_mode", "disabled"),
            fileMode = optString("file_mode", "upload-only"),
            blobAutoSnapshot = optBoolean("blob_auto_snapshot", true),
            blobRetainDays = optInt("blob_retain_days", 7),
            blobLastSnapshotAt = nullableString("blob_last_snapshot_at"),
            blobSnapshotId = blobManifest?.nullableString("snapshot_id"),
            blobSnapshotGeneration = blobManifest?.optLong("generation", 0L) ?: 0L,
            startedAt = nullableString("started_at"),
            loadAverage = if (has("load_average") && !isNull("load_average")) optDouble("load_average") else null,
            adbPort = optInt("adb_port").takeIf { has("adb_port") && !isNull("adb_port") },
            viewerPort = optInt("viewer_port").takeIf { has("viewer_port") && !isNull("viewer_port") },
            lastError = nullableString("last_error"),
            personaBrand = persona?.nullableString("brand"),
            personaModel = persona?.nullableString("model"),
            personaManufacturer = persona?.nullableString("manufacturer"),
            personaRelease = persona?.nullableString("release"),
            personaFingerprint = persona?.nullableString("fingerprint"),
        )
    }

    private fun JSONObject.toSessionState(): SessionState {
        val session = getJSONObject("session")
        val runtime = optJSONObject("runtime")?.toRuntimeSummary()
        return SessionState(
            sessionId = session.getString("id"),
            runtimeId = session.getString("runtime_id"),
            status = session.optString("status", ""),
            effectiveStatus = optString("effective_status").ifBlank { session.optString("status", "") },
            canResume = optBoolean("can_resume", false),
            runtimeReady = optBoolean("runtime_ready", false),
            isExpired = optBoolean("is_expired", false),
            endedAt = session.nullableString("ended_at"),
            endReason = session.nullableString("end_reason"),
            runtime = runtime,
        )
    }

    private fun JSONObject.toRuntimeState(): RuntimeState {
        return RuntimeState(
            runtime = getJSONObject("runtime").toRuntimeSummary(),
            effectiveState = optString("effective_state", "unknown"),
            runtimeReady = optBoolean("runtime_ready", false),
            hasActiveSession = optBoolean("has_active_session", false),
            hasCurrentDeviceSession = optBoolean("has_current_device_session", false),
            currentDeviceSessionId = nullableString("current_device_session_id"),
            canConnect = optBoolean("can_connect", false),
            canStart = optBoolean("can_start", false),
            canStop = optBoolean("can_stop", false),
            canWipe = optBoolean("can_wipe", false),
            canDelete = optBoolean("can_delete", false),
            isBusy = optBoolean("is_busy", false),
            blockedReason = nullableString("blocked_reason"),
        )
    }

    private fun JSONObject.toAccountStorage(): AccountStorage {
        return AccountStorage(
            provider = optString("provider", "local-disk"),
            fundingModel = optString("funding_model", "operator"),
            walletAddress = nullableString("wallet_address"),
            fundingAddress = nullableString("funding_address"),
            status = optString("status", "not_configured"),
            encryptedSeedBackedUp = optBoolean("encrypted_seed_backed_up", false),
            lastPreflightStatus = nullableString("last_preflight_status"),
            lastPreflightJson = nullableString("last_preflight_json"),
            lastPreflightAt = nullableString("last_preflight_at"),
        )
    }

    private fun JSONObject.toEntitlementSummary(): EntitlementSummary {
        return EntitlementSummary(
            accountId = optString("account_id"),
            source = optString("source", "none"),
            status = optString("status", "missing"),
            runtimeLimit = optInt("runtime_limit", 0),
            runtimeCount = optInt("runtime_count", 0),
            runtimeRemaining = optInt("runtime_remaining", 0),
            activeRuntimeLimit = optInt("active_runtime_limit", 0),
            activeRuntimeCount = optInt("active_runtime_count", 0),
            activeRuntimeRemaining = optInt("active_runtime_remaining", 0),
            runtimeStartsPerDay = optInt("runtime_starts_per_day", 0),
            runtimeStartsUsedToday = optInt("runtime_starts_used_today", 0),
            runtimeStartsRemainingToday = optInt("runtime_starts_remaining_today", 0),
            storageBytesLimit = optLong("storage_bytes_limit", 0L),
            storageBytesUsed = optLong("storage_bytes_used", 0L),
            storageBytesRemaining = optLong("storage_bytes_remaining", 0L),
            trialRuntimeSeconds = optInt("trial_runtime_seconds", 0),
            trialRuntimeSecondsUsed = optLong("trial_runtime_seconds_used", 0L),
            trialRuntimeSecondsRemaining = optLong("trial_runtime_seconds_remaining", 0L),
            expiresAt = nullableString("expires_at"),
            canCreateRuntime = optBoolean("can_create_runtime", false),
            canStartRuntime = optBoolean("can_start_runtime", false),
            createRuntimeBlockedCode = nullableString("create_runtime_blocked_code"),
            createRuntimeBlockedReason = nullableString("create_runtime_blocked_reason"),
            startRuntimeBlockedCode = nullableString("start_runtime_blocked_code"),
            startRuntimeBlockedReason = nullableString("start_runtime_blocked_reason"),
        )
    }

    private fun JSONObject.toAppCatalogEntry(): AppCatalogEntry {
        return AppCatalogEntry(
            packageName = optString("package_name"),
            source = optString("source", "fdroid"),
            displayName = optString("display_name").ifBlank { optString("package_name") },
            summary = optString("summary"),
            iconUrl = nullableString("icon_url"),
            versionName = optString("version_name"),
            versionCode = optLong("version_code", 0L),
            apkSizeBytes = optLong("apk_size_bytes", 0L),
            minSdk = optInt("min_sdk", 0),
            nativeCode = nullableString("native_code"),
            recommended = optBoolean("recommended", false),
            selected = optBoolean("selected", false),
            catalogUpdatedAt = nullableString("catalog_updated_at"),
        )
    }

    private fun JSONObject.toAppCatalogEntries(): List<AppCatalogEntry> {
        val items = optJSONArray("items") ?: return emptyList()
        if (items.length() > MAX_CATALOG_ITEMS) {
            throw IOException("App catalog exceeded the item limit")
        }
        return List(items.length()) { index -> items.getJSONObject(index).toAppCatalogEntry() }
    }

    private fun JSONObject.parsePersona(): JSONObject? {
        val raw = optString("active_persona_json").ifBlank { return null }
        return runCatching { JSONObject(raw) }.getOrNull()
    }

    private companion object {
        val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()
        const val MAX_JSON_RESPONSE_BYTES = 2L * 1024L * 1024L
        const val MAX_ERROR_MESSAGE_CHARS = 2_048
        const val MAX_CATALOG_ITEMS = 250
        val METHODS_REQUIRING_REQUEST_BODY = setOf("POST", "PUT", "PATCH")
        val registeredRuntimeCapabilities = mutableSetOf<String>()
    }
}

private fun JSONObject.nullableString(name: String): String? {
    if (!has(name) || isNull(name)) {
        return null
    }
    return optString(name).takeIf { it.isNotBlank() }
}
