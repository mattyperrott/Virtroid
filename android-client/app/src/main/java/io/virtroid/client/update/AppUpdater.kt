package io.virtroid.client.update

import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInfo
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import io.virtroid.client.BuildConfig
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * Updates the app from the project's GitHub releases. A release is used only
 * when its APK matches the SHA-256 published beside it, names this package,
 * has a higher version code, and is signed with the same certificate as the
 * installed app; Android's installer then asks the user to confirm, and
 * itself refuses an APK signed with another key.
 */
class AppUpdater(private val context: Context) {

    data class Release(val version: String, val apkUrl: String, val sha256Url: String, val notes: String)

    private val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
    private val client = OkHttpClient.Builder()
        .connectTimeout(20, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .followRedirects(true)
        .build()

    /** The newest release, when it is newer than this app. */
    suspend fun newerRelease(): Release? = withContext(Dispatchers.IO) {
        prefs.edit().putLong(KEY_CHECKED_AT, System.currentTimeMillis()).apply()
        val request = Request.Builder()
            .url("https://api.github.com/repos/$REPOSITORY/releases/latest")
            .header("Accept", "application/vnd.github+json")
            .build()
        val body = client.newCall(request).execute().use { response ->
            if (response.code == 404) return@withContext null
            if (!response.isSuccessful) throw IOException("GitHub answered ${response.code}")
            response.body?.string() ?: throw IOException("GitHub sent no release")
        }
        val release = JSONObject(body)
        val version = release.optString("tag_name").removePrefix("v")
        if (compareVersions(version, BuildConfig.VERSION_NAME) <= 0) return@withContext null
        val assets = release.optJSONArray("assets") ?: return@withContext null
        var apkUrl = ""
        var shaUrl = ""
        for (i in 0 until assets.length()) {
            val asset = assets.getJSONObject(i)
            when (asset.optString("name")) {
                "Virtroid-$version-release.apk" -> apkUrl = asset.optString("browser_download_url")
                "Virtroid-$version-release.apk.sha256" -> shaUrl = asset.optString("browser_download_url")
            }
        }
        if (apkUrl.isBlank() || shaUrl.isBlank()) return@withContext null
        Release(version, apkUrl, shaUrl, release.optString("body"))
    }

    /** Whether a daily automatic check is due. */
    fun checkDue(): Boolean =
        System.currentTimeMillis() - prefs.getLong(KEY_CHECKED_AT, 0L) >= CHECK_EVERY_MS

    /** Downloads, verifies and hands the release to Android's installer. */
    suspend fun install(release: Release) = withContext(Dispatchers.IO) {
        val dir = File(context.cacheDir, "updates").apply { deleteRecursively(); mkdirs() }
        val apk = File(dir, "Virtroid-${release.version}.apk")
        val expected = download(release.sha256Url).toString(Charsets.US_ASCII).trim().take(64).lowercase()
        client.newCall(Request.Builder().url(release.apkUrl).build()).execute().use { response ->
            if (!response.isSuccessful) throw IOException("download answered ${response.code}")
            val source = response.body ?: throw IOException("download was empty")
            apk.outputStream().use { out -> source.byteStream().copyTo(out) }
        }
        val actual = sha256(apk)
        if (expected.length != 64 || actual != expected) {
            apk.delete()
            throw IOException("the download does not match its published SHA-256")
        }
        verifyPackage(apk)
        commit(apk)
    }

    private fun verifyPackage(apk: File) {
        val pm = context.packageManager
        val archive = pm.getPackageArchiveInfo(apk.path, PackageManager.GET_SIGNING_CERTIFICATES)
            ?: throw IOException("the download is not an Android app")
        val installed = pm.getPackageInfo(context.packageName, PackageManager.GET_SIGNING_CERTIFICATES)
        if (archive.packageName != context.packageName) {
            throw IOException("the download is a different app")
        }
        if (archive.longVersionCode <= installed.longVersionCode) {
            throw IOException("the download is not newer than this app")
        }
        val archiveSigners = signers(archive)
        if (archiveSigners.isNotEmpty() && archiveSigners != signers(installed)) {
            throw IOException("the download is not signed by Virtroid's key")
        }
    }

    private fun signers(info: PackageInfo): Set<String> =
        info.signingInfo?.apkContentsSigners.orEmpty()
            .map { hex(MessageDigest.getInstance("SHA-256").digest(it.toByteArray())) }
            .toSet()

    private fun commit(apk: File) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
            .apply { setAppPackageName(context.packageName) }
        val sessionId = installer.createSession(params)
        installer.openSession(sessionId).use { session ->
            session.openWrite("base.apk", 0, apk.length()).use { out ->
                apk.inputStream().use { it.copyTo(out) }
                session.fsync(out)
            }
            val status = PendingIntent.getBroadcast(
                context,
                sessionId,
                Intent(context, UpdateInstallReceiver::class.java).setPackage(context.packageName),
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE,
            )
            session.commit(status.intentSender)
        }
        apk.delete()
    }

    private fun download(url: String): ByteArray =
        client.newCall(Request.Builder().url(url).build()).execute().use { response ->
            if (!response.isSuccessful) throw IOException("download answered ${response.code}")
            response.body?.bytes() ?: throw IOException("download was empty")
        }

    companion object {
        const val REPOSITORY = "mattyperrott/Virtroid"
        private const val PREFS = "app_updates"
        private const val KEY_CHECKED_AT = "checked_at"
        private const val CHECK_EVERY_MS = 24L * 60 * 60 * 1000

        /** Compares dotted versions such as 0.4.10 and 0.4.9 numerically. */
        fun compareVersions(left: String, right: String): Int {
            val a = left.split('.', '-').map { it.toIntOrNull() ?: 0 }
            val b = right.split('.', '-').map { it.toIntOrNull() ?: 0 }
            for (i in 0 until maxOf(a.size, b.size)) {
                val diff = a.getOrElse(i) { 0 }.compareTo(b.getOrElse(i) { 0 })
                if (diff != 0) return diff
            }
            return 0
        }

        fun sha256(file: File): String {
            val digest = MessageDigest.getInstance("SHA-256")
            file.inputStream().use { input ->
                val buffer = ByteArray(64 * 1024)
                while (true) {
                    val read = input.read(buffer)
                    if (read < 0) break
                    digest.update(buffer, 0, read)
                }
            }
            return hex(digest.digest())
        }

        private fun hex(bytes: ByteArray) = bytes.joinToString("") { "%02x".format(it) }
    }
}
