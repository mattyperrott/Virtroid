package org.server.scrcpy.model;

import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;

/** A clip's text as it crosses the viewer stream: UTF-8, never empty, within a limit. */
public final class ClipboardText {

    /**
     * About upstream scrcpy's limit. A longer clip stays on its own device:
     * half of one would paste as if it were whole.
     */
    public static final int MAX_BYTES = 256 * 1024;

    private ClipboardText() {
        // not instantiable
    }

    /** The text's wire form, or null when it is missing, empty or over the limit. */
    public static byte[] encode(CharSequence text) {
        // UTF-8 takes at least a byte a char, so this spares encoding a huge clip.
        if (text == null || text.length() == 0 || text.length() > MAX_BYTES) {
            return null;
        }
        byte[] data = text.toString().getBytes(StandardCharsets.UTF_8);
        return data.length > MAX_BYTES ? null : data;
    }

    /** The text in a wire form, or null when it is empty, over the limit or not UTF-8. */
    public static String decode(byte[] data) {
        if (data == null || data.length == 0 || data.length > MAX_BYTES) {
            return null;
        }
        try {
            return StandardCharsets.UTF_8.newDecoder()
                    .onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT)
                    .decode(ByteBuffer.wrap(data))
                    .toString();
        } catch (CharacterCodingException e) {
            return null;
        }
    }
}
