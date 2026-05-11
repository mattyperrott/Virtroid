package io.virtroid.client.api

import io.virtroid.client.BuildConfig
import io.virtroid.client.device.DeviceRuntimeProfile
import io.virtroid.client.security.DeviceIdentityStore
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit

data class RuntimeSummary(
    val id: String,
    val name: String,
    val status: String,
    val desiredState: String,
    val connectionStatus: String,
    val hostId: String?,
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
    val status: String,
    val encryptedSeedBackedUp: Boolean,
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
    val trialRuntimeSeconds: Int,
    val expiresAt: String?,
    val canCreateRuntime: Boolean,
    val canStartRuntime: Boolean,
    val createRuntimeBlockedCode: String?,
    val createRuntimeBlockedReason: String?,
    val startRuntimeBlockedCode: String?,
    val startRuntimeBlockedReason: String?,
)

class VirtroidApiException(
    val statusCode: Int,
    val code: String?,
    val errorMessage: String,
) : IOException(errorMessage)

private val DEFAULT_HTTP_CLIENT: OkHttpClient = OkHttpClient.Builder()
    .connectTimeout(15, TimeUnit.SECONDS)
    .readTimeout(90, TimeUnit.SECONDS)
    .writeTimeout(30, TimeUnit.SECONDS)
    .callTimeout(120, TimeUnit.SECONDS)
    .build()

class VirtroidApi(
    private val okHttpClient: OkHttpClient = DEFAULT_HTTP_CLIENT,
    private val deviceIdentityStore: DeviceIdentityStore = DeviceIdentityStore(),
) {
    suspend fun registerIdentity(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        blobKeyVerifier: String,
        currentBlobAccessKey: String? = null,
    ) = withContext(Dispatchers.IO) {
        val requestJson = JSONObject()
            .put("account_id", accountId)
            .put("device_id", deviceId)
            .put("blob_key_verifier", blobKeyVerifier)
        if (!currentBlobAccessKey.isNullOrBlank()) {
            requestJson.put("current_blob_access_key", currentBlobAccessKey)
        }
        val requestBody = requestJson.toString()

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
            val requestBody = JSONObject()
                .put("account_id", accountId)
                .put("device_id", deviceId)
                .put("device_name", deviceName)
                .put("public_key", publicKey)
                .put("runtime_name", runtimeProfile.runtimeName)
                .put("width_px", runtimeProfile.widthPx)
                .put("height_px", runtimeProfile.heightPx)
                .put("density_dpi", runtimeProfile.densityDpi)
                .toString()
                .toRequestBody(JSON_MEDIA_TYPE)

            val requestBuilder = Request.Builder()
                .url(normalizeBaseUrl(baseUrl) + "/api/v1/bootstrap")
                .post(requestBody)

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

    suspend fun updateStorage(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        provider: String,
        fundingModel: String,
        walletAddress: String?,
        encryptedSeedBlob: String?,
        seedEncryptionHint: String?,
        status: String,
    ): AccountStorage = withContext(Dispatchers.IO) {
        val requestBody = JSONObject()
            .put("account_id", accountId)
            .put("device_id", deviceId)
            .put("provider", provider)
            .put("funding_model", fundingModel)
            .put("wallet_address", walletAddress)
            .put("encrypted_seed_blob", encryptedSeedBlob)
            .put("seed_encryption_hint", seedEncryptionHint)
            .put("status", status)
            .toString()

        val payload = executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/storage",
                method = "PUT",
                accountId = accountId,
                deviceId = deviceId,
                body = requestBody,
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

    suspend fun deleteRuntime(baseUrl: String, accountId: String, deviceId: String, runtimeId: String): RuntimeSummary =
        withContext(Dispatchers.IO) {
            val payload = executeJson(
                signedJsonRequest(
                    baseUrl = baseUrl,
                    pathAndQuery = "/api/v1/me/runtimes/$runtimeId?account_id=$accountId&device_id=$deviceId",
                    method = "DELETE",
                    accountId = accountId,
                    deviceId = deviceId,
                ),
            )

            payload.toRuntimeSummary()
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
        val requestBody = JSONObject()
            .put("account_id", accountId)
            .put("device_id", deviceId)
            .put("max_size", maxSize)
            .put("bit_rate", bitRate)
            .put("blob_access_key", blobAccessKey)
            .toString()

        val payload = executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/session",
                method = "POST",
                accountId = accountId,
                deviceId = deviceId,
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

    suspend fun heartbeatSession(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        sessionId: String,
    ) = withContext(Dispatchers.IO) {
        executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/sessions/$sessionId/heartbeat",
                method = "POST",
                accountId = accountId,
                deviceId = deviceId,
            ),
        )
    }

    private suspend fun mutateRuntime(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        runtimeId: String,
        blobAccessKey: String,
        action: String,
    ): RuntimeSummary = withContext(Dispatchers.IO) {
        val requestBody = JSONObject()
            .put("account_id", accountId)
            .put("device_id", deviceId)
            .put("blob_access_key", blobAccessKey)
            .toString()

        val payload = executeJson(
            signedJsonRequest(
                baseUrl = baseUrl,
                pathAndQuery = "/api/v1/me/runtimes/$runtimeId/$action",
                method = "POST",
                accountId = accountId,
                deviceId = deviceId,
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
        val bodyBytes = body.toByteArray(Charsets.UTF_8)
        val requestBody = if (body.isBlank()) null else body.toRequestBody(JSON_MEDIA_TYPE)
        val builder = Request.Builder()
            .url(normalizeBaseUrl(baseUrl) + pathAndQuery)
            .method(method.uppercase(), requestBody)

        deviceIdentityStore.signedHeaders(
            method = method,
            requestUri = pathAndQuery,
            accountId = accountId,
            deviceId = deviceId,
            body = bodyBytes,
        ).forEach { (name, value) -> builder.header(name, value) }

        return builder.build()
    }

    private fun executeJson(request: Request): JSONObject {
        okHttpClient.newCall(request).execute().use { response ->
            val body = response.body?.string().orEmpty()
            if (!response.isSuccessful) {
                val errorPayload = parseError(body)
                throw VirtroidApiException(
                    statusCode = response.code,
                    code = errorPayload.first,
                    errorMessage = errorPayload.second,
                )
            }
            return JSONObject(body)
        }
    }

    private fun parseError(body: String): Pair<String?, String> {
        return runCatching {
            val payload = JSONObject(body)
            payload.optString("code").ifBlank { null } to payload.optString("error").ifBlank { body }
        }.getOrDefault(null to body)
    }

    private fun normalizeBaseUrl(baseUrl: String): String = baseUrl.trim().trimEnd('/')

    private fun JSONObject.toRuntimeSummary(): RuntimeSummary {
        val persona = parsePersona()
        return RuntimeSummary(
            id = getString("id"),
            name = optString("name").ifBlank { "Runtime" },
            status = optString("status", ""),
            desiredState = optString("desired_state", ""),
            connectionStatus = optString("connection_status", ""),
            hostId = optString("host_id").ifBlank { null },
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
            blobLastSnapshotAt = optString("blob_last_snapshot_at").ifBlank { null },
            startedAt = optString("started_at").ifBlank { null },
            loadAverage = if (has("load_average") && !isNull("load_average")) optDouble("load_average") else null,
            adbPort = optInt("adb_port").takeIf { has("adb_port") && !isNull("adb_port") },
            viewerPort = optInt("viewer_port").takeIf { has("viewer_port") && !isNull("viewer_port") },
            lastError = optString("last_error").ifBlank { null },
            personaBrand = persona?.optString("brand")?.ifBlank { null },
            personaModel = persona?.optString("model")?.ifBlank { null },
            personaManufacturer = persona?.optString("manufacturer")?.ifBlank { null },
            personaRelease = persona?.optString("release")?.ifBlank { null },
            personaFingerprint = persona?.optString("fingerprint")?.ifBlank { null },
        )
    }

    private fun JSONObject.toAccountStorage(): AccountStorage {
        return AccountStorage(
            provider = optString("provider", "local-disk"),
            fundingModel = optString("funding_model", "operator"),
            walletAddress = optString("wallet_address").ifBlank { null },
            status = optString("status", "not_configured"),
            encryptedSeedBackedUp = optBoolean("encrypted_seed_backed_up", false),
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
            trialRuntimeSeconds = optInt("trial_runtime_seconds", 0),
            expiresAt = optString("expires_at").ifBlank { null },
            canCreateRuntime = optBoolean("can_create_runtime", false),
            canStartRuntime = optBoolean("can_start_runtime", false),
            createRuntimeBlockedCode = optString("create_runtime_blocked_code").ifBlank { null },
            createRuntimeBlockedReason = optString("create_runtime_blocked_reason").ifBlank { null },
            startRuntimeBlockedCode = optString("start_runtime_blocked_code").ifBlank { null },
            startRuntimeBlockedReason = optString("start_runtime_blocked_reason").ifBlank { null },
        )
    }

    private fun JSONObject.parsePersona(): JSONObject? {
        val raw = optString("active_persona_json").ifBlank { return null }
        return runCatching { JSONObject(raw) }.getOrNull()
    }

    private companion object {
        val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()
    }
}
