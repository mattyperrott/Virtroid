package io.virtroid.client.security

internal object RuntimeCapabilityAliases {
    const val PREFIX = "virtroid_runtime_capability_"
    private val SAFE_ALIAS = Regex("^${PREFIX}[A-Za-z0-9_-]{1,200}$")

    fun forRuntime(runtimeId: String): String {
        val cleanRuntimeId = runtimeId.trim().replace(Regex("""[^A-Za-z0-9_-]"""), "_")
        require(cleanRuntimeId.isNotBlank() && cleanRuntimeId.length <= 200) {
            "Runtime ID cannot be represented as a safe capability alias"
        }
        return "$PREFIX$cleanRuntimeId"
    }

    fun isManaged(alias: String): Boolean = SAFE_ALIAS.matches(alias)
}
