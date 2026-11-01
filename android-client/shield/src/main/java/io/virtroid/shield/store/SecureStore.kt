// SPDX-License-Identifier: GPL-3.0-or-later
package io.virtroid.shield.store

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.io.File
import java.security.KeyStore
import java.security.MessageDigest
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * The app's values at rest: the account's protocol keys, its notes, the
 * WalletConnect session. Each value is sealed with AES-256-GCM under a key
 * the Android Keystore holds and never hands out, with the value's name as
 * associated data so a sealed value can't be moved under another name. A
 * file per value, named by the hash of its name, in the app's private files.
 */
class SecureStore(context: Context) {
    private val dir = File(context.filesDir, "store").apply { mkdirs() }

    @Synchronized
    fun get(name: String): String? {
        val file = fileFor(name)
        if (!file.isFile) return null
        val sealed = Base64.decode(file.readText(), Base64.NO_WRAP)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, sealed, 0, IV_BYTES))
        cipher.updateAAD(name.toByteArray())
        return String(cipher.doFinal(sealed, IV_BYTES, sealed.size - IV_BYTES))
    }

    @Synchronized
    fun set(name: String, value: String) {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        cipher.updateAAD(name.toByteArray())
        val sealed = cipher.iv + cipher.doFinal(value.toByteArray())
        val file = fileFor(name)
        val part = File(dir, "${file.name}.part")
        part.writeText(Base64.encodeToString(sealed, Base64.NO_WRAP))
        if (!part.renameTo(file)) throw IllegalStateException("Couldn't save $name")
        names().let { if (name !in it) saveNames(it + name) }
    }

    @Synchronized
    fun delete(name: String) {
        fileFor(name).delete()
        names().let { if (name in it) saveNames(it - name) }
    }

    /** Deletes every value whose name starts with [prefix]. */
    @Synchronized
    fun clear(prefix: String) {
        val all = names()
        all.filter { it.startsWith(prefix) }.forEach { fileFor(it).delete() }
        saveNames(all.filterNot { it.startsWith(prefix) }.toSet())
    }

    // The names themselves are sealed too, in one value of their own, so a
    // clear by prefix needs no plaintext index on disk.
    private fun names(): Set<String> {
        val file = fileFor(INDEX)
        if (!file.isFile) return emptySet()
        val sealed = Base64.decode(file.readText(), Base64.NO_WRAP)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, sealed, 0, IV_BYTES))
        cipher.updateAAD(INDEX.toByteArray())
        return String(cipher.doFinal(sealed, IV_BYTES, sealed.size - IV_BYTES)).split('\n').filter { it.isNotEmpty() }.toSet()
    }

    private fun saveNames(names: Set<String>) {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        cipher.updateAAD(INDEX.toByteArray())
        val sealed = cipher.iv + cipher.doFinal(names.joinToString("\n").toByteArray())
        val file = fileFor(INDEX)
        val part = File(dir, "${file.name}.part")
        part.writeText(Base64.encodeToString(sealed, Base64.NO_WRAP))
        if (!part.renameTo(file)) throw IllegalStateException("Couldn't save the store's index")
    }

    private fun fileFor(name: String): File {
        val digest = MessageDigest.getInstance("SHA-256").digest(name.toByteArray())
        return File(dir, digest.joinToString("") { "%02x".format(it) })
    }

    private fun key(): SecretKey {
        val keyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        generator.init(
            KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey()
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "shield-store"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val IV_BYTES = 12
        const val TAG_BITS = 128
        const val INDEX = "\u0000index"
    }
}
