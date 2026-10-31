package io.virtroid.client.viewer

import java.io.IOException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import java.net.SocketTimeoutException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Chooses where the stream connects when the runtime's node can be reached
 * both directly and through the edge relay. A phone whose own path to the node
 * is long or loses packets streams better through the edge, when the edge is
 * close to it and holds a clean path to the node; a phone with a good path to
 * the node keeps it, one hop shorter and off the edge.
 *
 * Both paths are timed together, before the stream connects, by a few TCP
 * connects each: a connect takes exactly one round trip, and one that lost a
 * packet is still waiting when the timing ends, since TCP resends a lost
 * connect only after a second. The server times the edge's own path to the
 * node (fallback_relay_node_rtt_ms), which the edge's path here leaves out.
 */
object RouteChooser {
    internal const val PROBES = 4
    internal const val PROBE_SPACING_MS = 40L

    // All of a path's connects end within this; any still waiting lost a packet.
    internal const val PROBE_BUDGET_MS = 900L

    // What a lost packet costs the stream: about the second TCP waits to resend.
    internal const val LOSS_COST_MS = 1_000L

    // The direct path is kept unless the edge's scores at least this much better.
    internal const val EDGE_MARGIN_MS = 25L

    /** One path's connects: how many answered, how many were lost, and the quickest. */
    data class PathTiming(val answered: Int, val lost: Int, val quickestMs: Long) {
        override fun toString(): String = when {
            answered > 0 -> "$quickestMs ms, $lost of $PROBES lost"
            lost > 0 -> "no answer, $lost of $PROBES lost"
            else -> "unreachable"
        }
    }

    data class Decision(
        val useEdge: Boolean,
        val direct: PathTiming,
        val edge: PathTiming,
        val relayLegMs: Int,
    ) {
        fun describe(): String = buildString {
            append(if (useEdge) "edge relay" else "direct to the node")
            append(" (direct ").append(direct)
            append("; edge ").append(edge)
            if (relayLegMs >= 0) append(" + $relayLegMs ms to the node")
            append(")")
        }
    }

    /**
     * Times the direct path to the node and the path to the edge, and decides.
     * relayLegMs is the edge's round trip to the node, or -1 when the server
     * did not say; the edge is then chosen only when the node does not answer.
     */
    @JvmStatic
    fun choose(directHost: String, directPort: Int, edgeHost: String, edgePort: Int, relayLegMs: Int): Decision {
        val direct = PathProbe(directHost, directPort)
        val edge = PathProbe(edgeHost, edgePort)
        direct.start()
        edge.start()
        val directTiming = direct.finish()
        val edgeTiming = edge.finish()
        return Decision(preferEdge(directTiming, edgeTiming, relayLegMs), directTiming, edgeTiming, relayLegMs)
    }

    /** True when the stream should go through the edge rather than straight to the node. */
    @JvmStatic
    fun preferEdge(direct: PathTiming, edge: PathTiming, relayLegMs: Int): Boolean {
        if (edge.answered == 0) return false
        if (direct.answered == 0) return true
        if (relayLegMs < 0) return false
        return score(edge, relayLegMs.toLong()) + EDGE_MARGIN_MS < score(direct, 0)
    }

    // What the stream should expect from a path: its round trip, plus the cost
    // of losing packets as often as the timing did.
    private fun score(timing: PathTiming, extraMs: Long): Long =
        timing.quickestMs + extraMs + timing.lost * LOSS_COST_MS / PROBES

    /** A path's connects, run on their own threads from start() until finish(). */
    private class PathProbe(private val host: String, private val port: Int) {
        private val done = CountDownLatch(PROBES)
        private val sockets = arrayOfNulls<Socket>(PROBES)
        private val elapsedMs = LongArray(PROBES) { WAITING }
        private var startedAt = 0L

        fun start() {
            startedAt = System.nanoTime()
            Thread({ run() }, "route-probe").apply { isDaemon = true }.start()
        }

        private fun run() {
            // Resolved before the clock matters, so a DNS lookup is not a round trip.
            val address = runCatching { InetAddress.getByName(host) }.getOrNull()
            if (address == null) {
                repeat(PROBES) { record(it, FAILED) }
                return
            }
            for (i in 0 until PROBES) {
                Thread({ connect(i, address) }, "route-probe-$i").apply { isDaemon = true }.start()
                if (i < PROBES - 1) {
                    try {
                        Thread.sleep(PROBE_SPACING_MS)
                    } catch (_: InterruptedException) {
                        Thread.currentThread().interrupt()
                        return
                    }
                }
            }
        }

        private fun connect(i: Int, address: InetAddress) {
            val socket = Socket()
            synchronized(sockets) { sockets[i] = socket }
            val remainingMs = PROBE_BUDGET_MS - (System.nanoTime() - startedAt) / 1_000_000
            if (remainingMs <= 0) {
                socket.close()
                record(i, LOST)
                return
            }
            val connectStartedAt = System.nanoTime()
            try {
                socket.connect(InetSocketAddress(address, port), remainingMs.toInt())
                record(i, maxOf((System.nanoTime() - connectStartedAt) / 1_000_000, 1L))
            } catch (_: SocketTimeoutException) {
                record(i, LOST)
            } catch (_: IOException) {
                // Refused, unreachable (no IPv6 here), or closed by finish().
                record(i, if (System.nanoTime() - startedAt >= PROBE_BUDGET_MS * 1_000_000) LOST else FAILED)
            } finally {
                runCatching { socket.close() }
            }
        }

        private fun record(i: Int, result: Long) {
            synchronized(elapsedMs) {
                if (elapsedMs[i] != WAITING) return
                elapsedMs[i] = result
            }
            done.countDown()
        }

        /** Waits out the budget at most, then counts every connect still waiting as lost. */
        fun finish(): PathTiming {
            val leftMs = PROBE_BUDGET_MS - (System.nanoTime() - startedAt) / 1_000_000
            if (leftMs > 0) {
                try {
                    done.await(leftMs, TimeUnit.MILLISECONDS)
                } catch (_: InterruptedException) {
                    Thread.currentThread().interrupt()
                }
            }
            for (i in 0 until PROBES) record(i, LOST)
            synchronized(sockets) { sockets.forEach { runCatching { it?.close() } } }
            val results = synchronized(elapsedMs) { elapsedMs.copyOf() }
            val answered = results.filter { it > 0 }
            return PathTiming(
                answered = answered.size,
                lost = results.count { it == LOST },
                quickestMs = answered.minOrNull() ?: 0L,
            )
        }

        private companion object {
            const val WAITING = 0L
            const val LOST = -1L
            const val FAILED = -2L
        }
    }
}
