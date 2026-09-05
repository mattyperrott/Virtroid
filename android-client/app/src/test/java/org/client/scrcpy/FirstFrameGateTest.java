package org.client.scrcpy;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class FirstFrameGateTest {
    @Test
    public void deliversOneCallbackPerSurfaceGeneration() {
        FirstFrameGate gate = new FirstFrameGate();

        assertTrue(gate.markDelivered());
        assertFalse(gate.markDelivered());

        gate.reset();

        assertTrue(gate.markDelivered());
        assertFalse(gate.markDelivered());
    }
}
