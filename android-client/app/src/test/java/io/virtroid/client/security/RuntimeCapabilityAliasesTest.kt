package io.virtroid.client.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class RuntimeCapabilityAliasesTest {
    @Test
    fun producesManagedAliasesForRuntimeIdentifiers() {
        val alias = RuntimeCapabilityAliases.forRuntime(" runtime/123 ")

        assertEquals("virtroid_runtime_capability_runtime_123", alias)
        assertTrue(RuntimeCapabilityAliases.isManaged(alias))
    }

    @Test
    fun rejectsAliasesOutsideTheDedicatedNamespace() {
        assertFalse(RuntimeCapabilityAliases.isManaged("virtroid_device_signing_key"))
        assertFalse(RuntimeCapabilityAliases.isManaged("virtroid_runtime_capability_../../device"))
        assertFalse(RuntimeCapabilityAliases.isManaged("virtroid_runtime_capability_"))
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsEmptyRuntimeIdentifiers() {
        RuntimeCapabilityAliases.forRuntime("   ")
    }
}
