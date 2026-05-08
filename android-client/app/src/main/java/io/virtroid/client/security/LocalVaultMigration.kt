package io.virtroid.client.security

import android.content.Context
import io.virtroid.client.data.ActiveSessionStore
import io.virtroid.client.data.AppLogStore
import io.virtroid.client.data.AppSettingsStore
import io.virtroid.client.data.SessionStore

object LocalVaultMigration {
    fun migrateUnlocked(context: Context) {
        val appContext = context.applicationContext
        SessionStore(appContext).migrateToVaultIfUnlocked()
        ActiveSessionStore(appContext).migrateToVaultIfUnlocked()
        AppSettingsStore(appContext).migrateToVaultIfUnlocked()
        IdentityPasswordStore(appContext).migrateToVaultIfUnlocked()
        AppLogStore.get(appContext).migrateToVaultIfUnlocked()
    }

    fun exportUnlockedToLegacy(context: Context) {
        val appContext = context.applicationContext
        SessionStore(appContext).exportVaultToLegacyIfUnlocked()
        ActiveSessionStore(appContext).exportVaultToLegacyIfUnlocked()
        AppSettingsStore(appContext).exportVaultToLegacyIfUnlocked()
        IdentityPasswordStore(appContext).exportVaultToLegacyIfUnlocked()
        AppLogStore.get(appContext).exportVaultToLegacyIfUnlocked()
    }
}
