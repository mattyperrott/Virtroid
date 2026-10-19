package io.virtroid.client.security

import android.content.Context
import io.virtroid.client.api.RuntimeSummary
import java.io.IOException

/**
 * Remembers, per runtime, the newest snapshot this phone has seen. The phone
 * sends that generation sealed with the runtime's keys, so the VM itself
 * refuses to restore anything older and numbers new snapshots above it; the
 * checks here catch what the server reports before then.
 */
class SnapshotRollbackGuard(context: Context) {
    private val appContext = context.applicationContext
    private val vault = SecureLocalVault.get(appContext)
    private val prefs = appContext.getSharedPreferences("virtroid-snapshot-highwater", Context.MODE_PRIVATE)

    @Synchronized
    fun verifyAndRecord(accountId: String, runtime: RuntimeSummary) {
        val snapshotId = runtime.blobSnapshotId.orEmpty()
        val generation = runtime.blobSnapshotGeneration
        checkSnapshotMetadata(generation, snapshotId, runtime.id)
        val key = recordKey(accountId, runtime.id)
        val previous = readRecord(key, runtime.blobGenerationFloor)
        if (isEmptySnapshotState(generation, snapshotId)) {
            val resetKey = resetRecordKey(key)
            if (prefs.getBoolean(resetKey, false)) {
                // This phone wiped the runtime. The record stays, so the VM
                // numbers its next snapshot above everything seen before.
                prefs.edit().remove(resetKey).apply()
                return
            }
            if (isLostSnapshotState(previous?.generation, runtime.blobGenerationFloor)) {
                // The snapshot was lost (with its VM, or wiped from another
                // phone), and the server says so. Losing data is not a
                // rollback: the record stays, and no older snapshot passes.
                return
            }
        }
        verifySnapshotAdvance(previous?.generation, previous?.snapshotId, generation, snapshotId, runtime.id)
        if (generation > 0L && (previous == null || generation > previous.generation)) {
            writeRecord(key, SnapshotRecord(generation, snapshotId))
        }
    }

    /**
     * The newest snapshot generation this phone has seen for the runtime, which
     * it seals with the runtime's keys for the VM to enforce; 0 for none.
     */
    @Synchronized
    fun snapshotFloor(accountId: String, runtimeId: String): Long =
        readRecord(recordKey(accountId, runtimeId), floor = null)?.generation ?: 0L

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

    /**
     * Reads a record. Older versions of the app stored the server's floor plus
     * the snapshot's generation; with [floor] known, such a record converts to
     * the snapshot's own generation, and without it counts as none yet.
     */
    private fun readRecord(key: String, floor: Long?): SnapshotRecord? {
        val raw = if (vault.isUnlocked) {
            vault.getString(NAMESPACE, key, null) ?: prefs.getString(key, null)?.also {
                vault.putString(NAMESPACE, key, it)
                prefs.edit().remove(key).apply()
            }
        } else {
            prefs.getString(key, null)
        }.orEmpty()
        val record = parseSnapshotRecord(raw) ?: return null
        if (raw.startsWith(RECORD_PREFIX)) {
            return record
        }
        floor ?: return null
        return SnapshotRecord((record.generation - floor).coerceAtLeast(0L), record.snapshotId).also {
            writeRecord(key, it)
        }
    }

    private fun writeRecord(key: String, record: SnapshotRecord) {
        val raw = "$RECORD_PREFIX${record.generation}|${record.snapshotId}"
        if (vault.isUnlocked) {
            vault.putString(NAMESPACE, key, raw)
            prefs.edit().remove(key).apply()
        } else {
            prefs.edit().putString(key, raw).apply()
        }
    }

    private fun recordKey(accountId: String, runtimeId: String): String = "$accountId:$runtimeId"

    private fun resetRecordKey(recordKey: String): String = "reset:$recordKey"

    private companion object {
        const val NAMESPACE = "snapshot_highwater"
    }
}

internal data class SnapshotRecord(val generation: Long, val snapshotId: String)

/** Records written by this version hold the snapshot's own generation. */
internal const val RECORD_PREFIX = "g:"

internal fun parseSnapshotRecord(raw: String): SnapshotRecord? {
    val body = raw.removePrefix(RECORD_PREFIX)
    val separator = body.indexOf('|')
    if (separator <= 0) {
        return null
    }
    val generation = body.substring(0, separator).toLongOrNull() ?: return null
    return SnapshotRecord(generation, body.substring(separator + 1))
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

internal fun checkSnapshotMetadata(generation: Long, snapshotId: String, runtimeId: String) {
    if (generation < 0L || generation > 0L && snapshotId.isBlank()) {
        throw SnapshotRollbackException("Snapshot metadata is invalid for runtime $runtimeId.")
    }
}

/**
 * True when the server reports no snapshot and says the one this phone last
 * saw was lost: its floor, the generations the runtime lost, covers it.
 */
internal fun isLostSnapshotState(previousGeneration: Long?, floor: Long): Boolean {
    return previousGeneration != null && floor > 0L && floor >= previousGeneration
}
