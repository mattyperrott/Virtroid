package io.virtroid.client.security

import android.content.Context
import io.virtroid.client.api.VirtroidApi
import io.virtroid.client.api.VirtroidApiException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

class IdentityKeyManager(
    context: Context,
    private val api: VirtroidApi,
) {
    private val passwordStore = IdentityPasswordStore(context.applicationContext)

    suspend fun createAndRegister(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        password: String,
    ): String {
        val bundle = withContext(Dispatchers.Default) {
            AccountMasterKeyCrypto.create(accountId, password)
        }
        val blobAccessKey = registerOrUnlockExisting(baseUrl, accountId, deviceId, password, bundle)
        passwordStore.saveConfigured(accountId, deviceId)
        return passwordStore.cacheBlobAccessKey(accountId, deviceId, blobAccessKey)
    }

    suspend fun unlockOrMigrate(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        password: String,
    ): String {
        val credential = try {
            api.getAccountRecoveryCredential(baseUrl, accountId, deviceId)
        } catch (error: VirtroidApiException) {
            if (error.statusCode != 404 || error.code != "account_recovery_not_configured") {
                throw error
            }
            null
        }
        val blobAccessKey = if (credential != null) {
            withContext(Dispatchers.Default) {
                AccountMasterKeyCrypto.unlock(credential, password).blobAccessKey
            }
        } else {
            val bundle = withContext(Dispatchers.Default) {
                val legacyBlobAccessKey = IdentityCrypto.deriveBlobAccessKey(accountId, deviceId, password)
                AccountMasterKeyCrypto.create(
                    accountId = accountId,
                    password = password,
                    existingBlobAccessKey = legacyBlobAccessKey,
                )
            }
            registerOrUnlockExisting(baseUrl, accountId, deviceId, password, bundle)
        }
        passwordStore.saveConfigured(accountId, deviceId)
        return passwordStore.cacheBlobAccessKey(accountId, deviceId, blobAccessKey)
    }

    private suspend fun registerOrUnlockExisting(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        password: String,
        bundle: AccountRecoveryBundle,
    ): String {
        return try {
            api.registerAccountRecoveryCredential(baseUrl, accountId, deviceId, bundle.credential)
            bundle.blobAccessKey
        } catch (error: VirtroidApiException) {
            if (error.statusCode != 409 || error.code != "account_recovery_already_configured") {
                throw error
            }
            val existing = api.getAccountRecoveryCredential(baseUrl, accountId, deviceId)
            withContext(Dispatchers.Default) {
                AccountMasterKeyCrypto.unlock(existing, password).blobAccessKey
            }
        }
    }
}
