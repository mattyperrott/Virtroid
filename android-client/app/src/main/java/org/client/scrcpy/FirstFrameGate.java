package org.client.scrcpy;

import java.util.concurrent.atomic.AtomicBoolean;

/** Delivers one first-frame signal for each newly attached viewer surface. */
final class FirstFrameGate {
    private final AtomicBoolean delivered = new AtomicBoolean(false);

    void reset() {
        delivered.set(false);
    }

    boolean markDelivered() {
        return delivered.compareAndSet(false, true);
    }
}
