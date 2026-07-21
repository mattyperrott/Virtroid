package io.virtroid.client.security

import android.content.Context
import io.virtroid.client.api.RuntimeSummary
import java.io.IOException

class SnapshotRollbackGuard(context: Context) {
    private val appContext = context.applicationContext
    private val vault = SecureLocalVault.get(appContext)
    private val prefs = appContext.getSharedPreferences("virtroid-snapshot-highwater", Context.MODE_PRIVATE)

    @Synchronized
    fun verifyAndRecord(accountId: String, runtime: RuntimeSummary) {
        val generation = runtime.blobSnapshotGeneration
        val snapshotId = runtime.blobSnapshotId.orEmpty()
        if (generation < 0L || generation > 0L && snapshotId.isBlank()) {
            throw SnapshotRollbackException("Snapshot metadata is invalid for runtime ${runtime.id}.")
        }
        val key = recordKey(accountId, runtime.id)
        val previous = readRecord(key)
        val resetKey = resetRecordKey(key)
        if (prefs.getBoolean(resetKey, false) && isEmptySnapshotState(generation, snapshotId)) {
            removeRecord(key)
            prefs.edit().remove(resetKey).apply()
            return
        }
        verifySnapshotAdvance(previous?.generation, previous?.snapshotId, generation, snapshotId, runtime.id)
        if (generation > 0L && (previous == null || generation > previous.generation)) {
            writeRecord(key, SnapshotRecord(generation, snapshotId))
        }
    }

    @Synchronized
    fun expectRuntimeReset(accountId: String, runtimeId: String) {
        prefs.edit().putBoolean(resetRecordKey(recordKey(accountId, runtimeId)), true).apply()
    }

    @Synchronized
    fun clearRuntime(accountId: String, runtimeId: String) {
        val key = recordKey(accountId, runtimeId)
        removeRecord(key)
        prefs.edit().remove(resetRecordKey(key)).apply()
    }

    private fun removeRecord(key: String) {
        if (vault.isUnlocked) {
            vault.remove(NAMESPACE, key)
        }
        prefs.edit().remove(key).apply()
    }

    private fun readRecord(key: String): SnapshotRecord? {
        val raw = if (vault.isUnlocked) {
            vault.getString(NAMESPACE, key, null) ?: prefs.getString(key, null)?.also {
                vault.putString(NAMESPACE, key, it)
                prefs.edit().remove(key).apply()
            }
        } else {
            prefs.getString(key, null)
        }.orEmpty()
        val separator = raw.indexOf('|')
        if (separator <= 0) {
            return null
        }
        val generation = raw.substring(0, separator).toLongOrNull() ?: return null
        return SnapshotRecord(generation, raw.substring(separator + 1))
    }

    private fun writeRecord(key: String, record: SnapshotRecord) {
        val raw = "${record.generation}|${record.snapshotId}"
        if (vault.isUnlocked) {
            vault.putString(NAMESPACE, key, raw)
            prefs.edit().remove(key).apply()
        } else {
            prefs.edit().putString(key, raw).apply()
        }
    }

    private fun recordKey(accountId: String, runtimeId: String): String = "$accountId:$runtimeId"

    private fun resetRecordKey(recordKey: String): String = "reset:$recordKey"

    private data class SnapshotRecord(val generation: Long, val snapshotId: String)

    private companion object {
        const val NAMESPACE = "snapshot_highwater"
    }
}

class SnapshotRollbackException(message: String) : IOException(message)

internal fun verifySnapshotAdvance(
    previousGeneration: Long?,
    previousSnapshotId: String?,
    generation: Long,
    snapshotId: String,
    runtimeId: String,
) {
    if (generation < 0L || generation > 0L && snapshotId.isBlank()) {
        throw SnapshotRollbackException("Snapshot metadata is invalid for runtime $runtimeId.")
    }
    if (previousGeneration == null) {
        return
    }
    if (generation < previousGeneration) {
        throw SnapshotRollbackException("An older encrypted snapshot generation was returned for runtime $runtimeId.")
    }
    if (generation == previousGeneration && generation > 0L && snapshotId != previousSnapshotId) {
        throw SnapshotRollbackException("A conflicting encrypted snapshot was returned for runtime $runtimeId.")
    }
}

internal fun isEmptySnapshotState(generation: Long, snapshotId: String): Boolean {
    return generation == 0L && snapshotId.isBlank()
}
