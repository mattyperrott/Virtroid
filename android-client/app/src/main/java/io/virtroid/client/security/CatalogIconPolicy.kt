package io.virtroid.client.security

import java.net.URI
import java.net.URL
import java.util.Locale

object CatalogIconPolicy {
    const val MAX_URL_LENGTH = 2_048
    const val MAX_ENCODED_BYTES = 512 * 1024
    const val MAX_EDGE_PIXELS = 512
    const val MAX_PIXEL_COUNT = MAX_EDGE_PIXELS * MAX_EDGE_PIXELS
    const val MAX_DECODED_BYTES = MAX_PIXEL_COUNT * 4
    const val MAX_CACHE_BYTES = 8 * 1024 * 1024

    private const val ALLOWED_HOST = "f-droid.org"
    private const val ALLOWED_PATH_PREFIX = "/repo/"
    private val ALLOWED_CONTENT_TYPES = setOf("image/png", "image/jpeg", "image/webp")

    fun validatedUrl(rawUrl: String): URL? {
        if (rawUrl.isBlank() || rawUrl.length > MAX_URL_LENGTH) {
            return null
        }
        return runCatching {
            val uri = URI(rawUrl)
            val host = uri.host?.lowercase(Locale.US)
            require(uri.scheme.equals("https", ignoreCase = true))
            require(host == ALLOWED_HOST)
            require(uri.userInfo == null)
            require(uri.port == -1 || uri.port == 443)
            require(uri.rawPath?.startsWith(ALLOWED_PATH_PREFIX) == true)
            require(uri.normalize() == uri)
            require(uri.rawQuery == null)
            require(uri.rawFragment == null)
            uri.toURL()
        }.getOrNull()
    }

    fun allowsContent(contentType: String?, contentLength: Long): Boolean {
        val normalizedType = contentType
            ?.substringBefore(';')
            ?.trim()
            ?.lowercase(Locale.US)
        return normalizedType in ALLOWED_CONTENT_TYPES &&
            contentLength in 1..MAX_ENCODED_BYTES.toLong()
    }

    fun allowsDecodedBitmap(width: Int, height: Int, allocationBytes: Int? = null): Boolean {
        if (width !in 1..MAX_EDGE_PIXELS || height !in 1..MAX_EDGE_PIXELS) {
            return false
        }
        if (width.toLong() * height.toLong() > MAX_PIXEL_COUNT) {
            return false
        }
        return allocationBytes == null || allocationBytes in 1..MAX_DECODED_BYTES
    }
}
