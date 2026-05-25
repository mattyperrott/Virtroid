package io.virtroid.client.data

import android.content.Context

class AppSelectionStore(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences("virtroid_app_selections", Context.MODE_PRIVATE)

    fun pendingSelections(): Set<String> {
        return prefs.getStringSet(KEY_PENDING_PACKAGES, emptySet()).orEmpty()
            .filter { it.isNotBlank() }
            .toSet()
    }

    fun savePendingSelections(packageNames: Set<String>) {
        prefs.edit()
            .putStringSet(KEY_PENDING_PACKAGES, packageNames.filter { it.isNotBlank() }.toSet())
            .apply()
    }

    fun clearPendingSelections() {
        prefs.edit().remove(KEY_PENDING_PACKAGES).apply()
    }

    private companion object {
        const val KEY_PENDING_PACKAGES = "pending_package_names"
    }
}
