package io.virtroid.client.viewer

import io.virtroid.client.viewer.RouteChooser.PathTiming
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.net.ServerSocket
import kotlin.concurrent.thread

class RouteChooserTest {
    private fun clean(ms: Long) = PathTiming(answered = RouteChooser.PROBES, lost = 0, quickestMs = ms)

    @Test
    fun aGoodDirectPathIsKept() {
        // Close to the node over IPv6: the edge would add its own leg.
        assertFalse(RouteChooser.preferEdge(direct = clean(50), edge = clean(47), relayLegMs = 174))
    }

    @Test
    fun aLongLossyDirectPathGoesThroughTheEdge() {
        // Measured 2026-10-31 from South-East Asia to a node in Helsinki.
        val direct = PathTiming(answered = 3, lost = 1, quickestMs = 285)
        assertTrue(RouteChooser.preferEdge(direct = direct, edge = clean(46), relayLegMs = 174))
    }

    @Test
    fun aLongerButCleanDirectPathLosesOnlyByMoreThanTheMargin() {
        assertTrue(RouteChooser.preferEdge(direct = clean(290), edge = clean(46), relayLegMs = 174))
        val nearlyEven = 46 + 174 + RouteChooser.EDGE_MARGIN_MS
        assertFalse(RouteChooser.preferEdge(direct = clean(nearlyEven), edge = clean(46), relayLegMs = 174))
    }

    @Test
    fun lossOnTheDirectPathCountsAgainstIt() {
        val direct = PathTiming(answered = 3, lost = 1, quickestMs = 60)
        // 60 ms plus a quarter of a second for every packet in four lost, against 221.
        assertTrue(RouteChooser.preferEdge(direct = direct, edge = clean(47), relayLegMs = 174))
        val edgeLossy = PathTiming(answered = 2, lost = 2, quickestMs = 47)
        assertFalse(RouteChooser.preferEdge(direct = direct, edge = edgeLossy, relayLegMs = 174))
    }

    @Test
    fun anUnreachablePathIsNeverChosen() {
        val none = PathTiming(answered = 0, lost = 0, quickestMs = 0)
        assertTrue(RouteChooser.preferEdge(direct = none, edge = clean(400), relayLegMs = 300))
        assertFalse(RouteChooser.preferEdge(direct = clean(900), edge = none, relayLegMs = 10))
        val allLost = PathTiming(answered = 0, lost = RouteChooser.PROBES, quickestMs = 0)
        assertTrue(RouteChooser.preferEdge(direct = allLost, edge = clean(47), relayLegMs = 174))
    }

    @Test
    fun withoutTheEdgesLegTheEdgeIsOnlyAFallback() {
        val lossy = PathTiming(answered = 2, lost = 2, quickestMs = 300)
        assertFalse(RouteChooser.preferEdge(direct = lossy, edge = clean(47), relayLegMs = -1))
        val none = PathTiming(answered = 0, lost = 0, quickestMs = 0)
        assertTrue(RouteChooser.preferEdge(direct = none, edge = clean(47), relayLegMs = -1))
    }

    @Test
    fun choosingTimesBothPathsWithRealConnects() {
        ServerSocket(0, 50, java.net.InetAddress.getLoopbackAddress()).use { listening ->
            val accepting = thread(isDaemon = true) {
                runCatching { while (true) listening.accept().close() }
            }
            // A port nothing listens on: refused at once, so unreachable.
            val closedPort = ServerSocket(0, 1, java.net.InetAddress.getLoopbackAddress()).use { it.localPort }
            val decision = RouteChooser.choose("127.0.0.1", closedPort, "127.0.0.1", listening.localPort, 20)
            assertTrue(decision.useEdge)
            assertEquals(0, decision.direct.answered)
            assertEquals(RouteChooser.PROBES, decision.edge.answered)
            assertEquals(0, decision.edge.lost)
            assertTrue(decision.describe(), decision.describe().startsWith("edge relay (direct unreachable; edge "))
            accepting.interrupt()
        }
    }
}
