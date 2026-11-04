package org.server.scrcpy;

import org.server.scrcpy.model.ClipboardText;
import org.server.scrcpy.model.CommandPacket;
import org.server.scrcpy.model.MediaPacket;

import java.io.Closeable;
import java.io.IOException;
import java.io.OutputStream;
import java.util.concurrent.Semaphore;

/**
 * Carries clipboard text over the viewer stream for a client that asked for it
 * with CLIPBOARD_READY. Never logs a clip.
 */
public final class ClipboardBridge implements Closeable {

    public interface Clipboard {
        /** The current clip's text, or null when it holds none or {@link #setText} put it there. */
        String getCopiedText();

        boolean setText(String text);

        boolean addChangeListener(Runnable listener);
    }

    private final Clipboard clipboard;
    private final OutputStream stream;
    private final Semaphore changes = new Semaphore(0);
    private final Thread sender = new Thread(this::sendChanges, "clipboard-sender");

    private ClipboardBridge(Clipboard clipboard, OutputStream stream) {
        this.clipboard = clipboard;
        this.stream = stream;
        // Must not keep the process alive once the viewer has gone.
        sender.setDaemon(true);
    }

    /**
     * Watches the clipboard and tells the client this end handles clips.
     * Returns null, telling it nothing, when the clipboard cannot be watched.
     */
    static ClipboardBridge start(Clipboard clipboard, OutputStream stream) throws IOException {
        ClipboardBridge bridge = new ClipboardBridge(clipboard, stream);
        if (!clipboard.addChangeListener(bridge.changes::release)) {
            return null;
        }
        bridge.write(CommandPacket.CmdType.CLIPBOARD_READY, new byte[0]);
        bridge.sender.start();
        return bridge;
    }

    /** Puts a clip from the client on this device's clipboard. */
    void apply(byte[] data) {
        String text = ClipboardText.decode(data);
        if (text != null) {
            clipboard.setText(text);
        }
    }

    private void sendChanges() {
        try {
            while (true) {
                changes.acquire();
                // Writes can wait on video, so a burst of changes sends only the last.
                changes.drainPermits();
                byte[] data = ClipboardText.encode(clipboard.getCopiedText());
                if (data != null) {
                    write(CommandPacket.CmdType.CLIPBOARD_TEXT, data);
                }
            }
        } catch (InterruptedException | IOException e) {
            // The viewer has gone.
        }
    }

    private void write(CommandPacket.CmdType type, byte[] data) throws IOException {
        stream.write(CommandPacket.toArray(MediaPacket.Type.COMMAND, type, data));
        stream.flush();
    }

    @Override
    public void close() {
        sender.interrupt();
    }
}
