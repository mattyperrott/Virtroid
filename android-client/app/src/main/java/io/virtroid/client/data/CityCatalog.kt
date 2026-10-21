package io.virtroid.client.data

import android.content.Context
import java.io.BufferedReader
import java.io.InputStreamReader
import java.text.Normalizer
import java.util.Locale
import kotlin.math.cos
import kotlin.math.pow

data class City(
    val name: String,
    val countryCode: String,
    val latitude: Double,
    val longitude: Double,
    val timezone: String,
) {
    /** "Berlin, Germany", in the phone's language. */
    fun label(locale: Locale = Locale.getDefault()): String {
        val country = Locale("", countryCode).getDisplayCountry(locale).ifBlank { countryCode }
        return "$name, $country"
    }
}

/**
 * The cities a runtime can be placed in, bundled with the app (assets/
 * cities.tsv, from GeoNames) so choosing one sends nothing anywhere.
 */
class CityCatalog private constructor(private val entries: List<Entry>) {
    private class Entry(val city: City, val keys: List<String>)

    /** Cities whose name starts with [query], then those containing it, largest first. */
    fun search(query: String, limit: Int = 40): List<City> {
        val needle = fold(query)
        if (needle.isEmpty()) return entries.take(limit).map { it.city }
        val prefix = mutableListOf<City>()
        val contains = mutableListOf<City>()
        for (entry in entries) {
            when {
                entry.keys.any { it.startsWith(needle) } -> prefix += entry.city
                contains.size < limit && entry.keys.any { it.contains(needle) } -> contains += entry.city
            }
            if (prefix.size >= limit) break
        }
        return (prefix + contains).take(limit)
    }

    /** The closest bundled city, for the timezone of typed coordinates. */
    fun nearest(latitude: Double, longitude: Double): City? =
        entries.minByOrNull { distanceSquared(latitude, longitude, it.city) }?.city

    companion object {
        @Volatile
        private var loaded: CityCatalog? = null

        /** Reads the bundled list once; call it off the main thread. */
        fun get(context: Context): CityCatalog = loaded ?: synchronized(this) {
            loaded ?: read(context.applicationContext).also { loaded = it }
        }

        private fun read(context: Context): CityCatalog {
            val entries = ArrayList<Entry>(35_000)
            context.assets.open("cities.tsv").use { stream ->
                BufferedReader(InputStreamReader(stream, Charsets.UTF_8)).forEachLine { line ->
                    if (line.startsWith("#")) return@forEachLine
                    val fields = line.split('\t')
                    if (fields.size != 6) return@forEachLine
                    val latitude = fields[3].toDoubleOrNull() ?: return@forEachLine
                    val longitude = fields[4].toDoubleOrNull() ?: return@forEachLine
                    val city = City(fields[0], fields[2], latitude, longitude, fields[5])
                    entries += Entry(city, listOf(fold(fields[0]), fold(fields[1])).filter { it.isNotEmpty() }.distinct())
                }
            }
            return CityCatalog(entries)
        }

        /** Lower case without accents, so "sao" finds São Paulo. */
        internal fun fold(text: String): String =
            Normalizer.normalize(text.trim(), Normalizer.Form.NFD)
                .replace(Regex("\\p{Mn}+"), "")
                .lowercase(Locale.ROOT)

        // Equirectangular distance: enough to tell which city is nearest.
        private fun distanceSquared(latitude: Double, longitude: Double, city: City): Double {
            val x = Math.toRadians(city.longitude - longitude) * cos(Math.toRadians((city.latitude + latitude) / 2))
            val y = Math.toRadians(city.latitude - latitude)
            return x.pow(2) + y.pow(2)
        }
    }
}
