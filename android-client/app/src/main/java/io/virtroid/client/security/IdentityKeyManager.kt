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
            val unlocked = withContext(Dispatchers.Default) {
                AccountMasterKeyCrypto.unlock(credential, password).blobAccessKey
            }
            upgradeIfOlder(baseUrl, accountId, deviceId, credential, password)
            unlocked
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

    /**
     * Changes the identity password: the stored envelope is rewrapped under
     * [newPassword] after [currentPassword] opens it. Throws when the current
     * password is wrong; nothing changes then.
     */
    suspend fun changePassword(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        currentPassword: String,
        newPassword: String,
    ): String {
        val credential = api.getAccountRecoveryCredential(baseUrl, accountId, deviceId)
        val rewrapped = withContext(Dispatchers.Default) {
            AccountMasterKeyCrypto.rewrap(credential, deviceId, currentPassword, newPassword)
        }
        api.changeIdentityPassword(baseUrl, accountId, deviceId, rewrapped.credential, rewrapped.proof)
        passwordStore.saveConfigured(accountId, deviceId)
        return passwordStore.cacheBlobAccessKey(accountId, deviceId, rewrapped.blobAccessKey)
    }

    /**
     * Rewraps an older (PBKDF2) envelope under the same password as the
     * current version, whose envelope the server hands out only for the
     * password's login key. Best effort: the next unlock tries again.
     */
    private suspend fun upgradeIfOlder(
        baseUrl: String,
        accountId: String,
        deviceId: String,
        credential: AccountRecoveryCredential,
        password: String,
    ) {
        if (credential.version >= AccountMasterKeyCrypto.VERSION) {
            return
        }
        runCatching {
            val upgraded = withContext(Dispatchers.Default) {
                AccountMasterKeyCrypto.rewrap(credential, deviceId, password, password)
            }
            api.changeIdentityPassword(baseUrl, accountId, deviceId, upgraded.credential, upgraded.proof)
        }
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
