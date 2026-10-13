package io.virtroid.client.viewer

import android.os.SystemClock
import io.virtroid.client.security.TlsPins
import org.client.scrcpy.crypto.NodeKeyPin
import java.net.InetSocketAddress
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory

/** Where the viewer's stream is connected: the runtime node, or the edge it fell back to. */
internal data class StreamEndpoint(val host: String, val port: Int, val tlsPin: String)

/**
 * Measures the round trip to the endpoint the stream uses. A TCP connect takes
 * exactly one round trip, so its duration is the measurement. The TLS
 * handshake is then completed the way the stream does it, so the far end sees
 * a well-formed connection rather than an aborted one, and the probe only
 * talks to the endpoint the stream trusts.
 */
internal object RelayLatencyProbe {
    private const val TIMEOUT_MS = 3_000

    /** The round trip in milliseconds, or null when the endpoint did not answer. */
    fun measure(endpoint: StreamEndpoint): Long? = runCatching {
        val pinned = endpoint.tlsPin.isNotBlank()
        val factory = if (pinned) {
            NodeKeyPin.socketFactory(endpoint.tlsPin)
        } else {
            SSLSocketFactory.getDefault() as SSLSocketFactory
        }
        (factory.createSocket() as SSLSocket).use { socket ->
            socket.soTimeout = TIMEOUT_MS
            val startedAt = SystemClock.elapsedRealtimeNanos()
            socket.connect(InetSocketAddress(endpoint.host, endpoint.port), TIMEOUT_MS)
            val roundTripMs = (SystemClock.elapsedRealtimeNanos() - startedAt) / 1_000_000
            if (!pinned) {
                socket.sslParameters = socket.sslParameters.apply { endpointIdentificationAlgorithm = "HTTPS" }
            }
            socket.startHandshake()
            if (!pinned) {
                TlsPins.checkPeerCertificates(endpoint.host, socket.session.peerCertificates)
            }
            roundTripMs
        }
    }.getOrNull()
}
