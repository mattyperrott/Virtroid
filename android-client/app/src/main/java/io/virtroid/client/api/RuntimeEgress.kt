package io.virtroid.client.api

/**
 * Where a runtime's own internet traffic leaves its VM. The phone's viewer
 * link to the runtime is direct in both modes.
 */
object RuntimeEgress {
    const val DIRECT = "direct"

    /** Through the VM's Tor client: TCP and DNS only, nothing else leaves. */
    const val TOR = "tor"
}
