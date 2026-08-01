package org.server.scrcpy;

import java.io.IOException;
import java.io.OutputStream;

/** Keeps each multiplexed audio/video packet contiguous on the shared socket. */
final class SynchronizedOutputStream extends OutputStream {
    private final OutputStream delegate;

    SynchronizedOutputStream(OutputStream delegate) {
        this.delegate = delegate;
    }

    @Override
    public synchronized void write(int value) throws IOException {
        delegate.write(value);
    }

    @Override
    public synchronized void write(byte[] value, int offset, int length) throws IOException {
        delegate.write(value, offset, length);
    }

    @Override
    public synchronized void flush() throws IOException {
        delegate.flush();
    }

    @Override
    public synchronized void close() throws IOException {
        delegate.close();
    }
}
