package org.server.scrcpy;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import org.junit.After;
import org.junit.Test;
import org.server.scrcpy.model.ClipboardText;
import org.server.scrcpy.model.CommandPacket;
import org.server.scrcpy.model.MediaPacket;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;

public class ClipboardBridgeTest {
    private static final byte[] READY_FRAME =
            CommandPacket.toArray(MediaPacket.Type.COMMAND, CommandPacket.CmdType.CLIPBOARD_READY, new byte[0]);

    private static final class FakeClipboard implements ClipboardBridge.Clipboard {
        final List<String> set = new ArrayList<>();
        volatile String copied;
        volatile Runnable listener;
        boolean watchable = true;

        @Override
        public String getCopiedText() {
            return copied;
        }

        @Override
        public boolean setText(String text) {
            set.add(text);
            return true;
        }

        @Override
        public boolean addChangeListener(Runnable listener) {
            this.listener = listener;
            return watchable;
        }

        void copy(String text) {
            copied = text;
            listener.run();
        }
    }

    /** Hands over what each flush sent, as the socket would carry it. */
    private static class Frames extends OutputStream {
        final BlockingQueue<byte[]> flushed = new LinkedBlockingQueue<>();
        private final ByteArrayOutputStream pending = new ByteArrayOutputStream();

        @Override
        public void write(int value) {
            pending.write(value);
        }

        @Override
        public void write(byte[] value, int offset, int length) {
            pending.write(value, offset, length);
        }

        @Override
        public void flush() {
            flushed.add(pending.toByteArray());
            pending.reset();
        }

        byte[] next() throws InterruptedException {
            return flushed.poll(5, TimeUnit.SECONDS);
        }
    }

    private static byte[] textFrame(String text) {
        return CommandPacket.toArray(MediaPacket.Type.COMMAND, CommandPacket.CmdType.CLIPBOARD_TEXT, ClipboardText.encode(text));
    }

    private final FakeClipboard clipboard = new FakeClipboard();
    private final Frames frames = new Frames();
    private ClipboardBridge bridge;

    private void startAndTakeReady() throws Exception {
        bridge = ClipboardBridge.start(clipboard, frames);
        assertNotNull(bridge);
        assertTrue(Arrays.equals(READY_FRAME, frames.next()));
    }

    @After
    public void closeBridge() {
        if (bridge != null) {
            bridge.close();
        }
    }

    @Test
    public void saysNothingWhenTheClipboardCannotBeWatched() throws Exception {
        clipboard.watchable = false;
        assertNull(ClipboardBridge.start(clipboard, frames));
        assertTrue(frames.flushed.isEmpty());
    }

    @Test
    public void sendsWhatIsCopied() throws Exception {
        startAndTakeReady();
        clipboard.copy("0xabc");
        assertTrue(Arrays.equals(textFrame("0xabc"), frames.next()));
    }

    @Test
    public void sendsTheSameTextCopiedAgain() throws Exception {
        startAndTakeReady();
        clipboard.copy("0xabc");
        assertTrue(Arrays.equals(textFrame("0xabc"), frames.next()));
        // The phone may have cleared it since: copying again must reach it.
        clipboard.copy("0xabc");
        assertTrue(Arrays.equals(textFrame("0xabc"), frames.next()));
    }

    @Test
    public void skipsWhatIsNotTextAndWhatIsTooLong() throws Exception {
        startAndTakeReady();
        clipboard.copy(null); // an image, or a clip the bridge itself set
        char[] tooLong = new char[ClipboardText.MAX_BYTES + 1];
        Arrays.fill(tooLong, 'a');
        clipboard.copy(new String(tooLong));
        clipboard.copy("after");
        assertTrue(Arrays.equals(textFrame("after"), frames.next()));
    }

    @Test
    public void aBurstOfCopiesWaitingOnTheStreamSendsTheLast() throws Exception {
        CountDownLatch release = new CountDownLatch(1);
        CountDownLatch writing = new CountDownLatch(1);
        Frames stalled = new Frames() {
            private int flushes;

            @Override
            public void flush() {
                super.flush();
                if (++flushes == 2) {
                    writing.countDown();
                    try {
                        release.await();
                    } catch (InterruptedException e) {
                        Thread.currentThread().interrupt();
                    }
                }
            }
        };
        bridge = ClipboardBridge.start(clipboard, stalled);
        assertTrue(Arrays.equals(READY_FRAME, stalled.next()));
        clipboard.copy("first");
        assertTrue(writing.await(5, TimeUnit.SECONDS));
        clipboard.copy("second");
        clipboard.copy("third");
        release.countDown();
        assertTrue(Arrays.equals(textFrame("first"), stalled.next()));
        assertTrue(Arrays.equals(textFrame("third"), stalled.next()));
        assertNull(stalled.flushed.poll(200, TimeUnit.MILLISECONDS));
    }

    @Test
    public void stopsWhenTheStreamFails() throws Exception {
        OutputStream failing = new OutputStream() {
            private int writes;

            @Override
            public void write(int value) throws IOException {
                throw new IOException("closed");
            }

            @Override
            public void write(byte[] value, int offset, int length) throws IOException {
                if (++writes > 1) {
                    throw new IOException("closed");
                }
            }
        };
        bridge = ClipboardBridge.start(clipboard, failing);
        clipboard.copy("gone"); // Must not throw on the service's binder thread.
    }

    @Test
    public void appliesWhatTheClientSends() throws Exception {
        startAndTakeReady();
        bridge.apply(ClipboardText.encode("from the phone"));
        assertEquals(List.of("from the phone"), clipboard.set);
    }

    @Test
    public void ignoresWhatIsNotAClip() throws Exception {
        startAndTakeReady();
        bridge.apply(new byte[0]);
        bridge.apply(new byte[]{(byte) 0xFF});
        bridge.apply(new byte[ClipboardText.MAX_BYTES + 1]);
        assertTrue(clipboard.set.isEmpty());
    }
}
