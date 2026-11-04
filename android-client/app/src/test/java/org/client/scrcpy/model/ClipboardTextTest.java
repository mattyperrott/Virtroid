package org.client.scrcpy.model;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertNull;

import org.junit.Test;

import java.nio.charset.StandardCharsets;
import java.util.Arrays;

public class ClipboardTextTest {
    private static String repeated(char c, int count) {
        char[] chars = new char[count];
        Arrays.fill(chars, c);
        return new String(chars);
    }

    @Test
    public void textRoundTrips() {
        String text = "0x52908400098527886E0F7030069857D2E4169EE7\nsecond line é ✓ 🔑";
        assertEquals(text, ClipboardText.decode(ClipboardText.encode(text)));
    }

    @Test
    public void encodesAsUtf8() {
        assertArrayEquals("é".getBytes(StandardCharsets.UTF_8), ClipboardText.encode("é"));
    }

    @Test
    public void anEmptyOrMissingClipIsNotCarried() {
        assertNull(ClipboardText.encode(null));
        assertNull(ClipboardText.encode(""));
        assertNull(ClipboardText.decode(null));
        assertNull(ClipboardText.decode(new byte[0]));
    }

    @Test
    public void aClipAtTheLimitIsCarried() {
        byte[] data = ClipboardText.encode(repeated('a', ClipboardText.MAX_BYTES));
        assertNotNull(data);
        assertEquals(ClipboardText.MAX_BYTES, ClipboardText.decode(data).length());
    }

    @Test
    public void aClipOverTheLimitIsNotCarried() {
        assertNull(ClipboardText.encode(repeated('a', ClipboardText.MAX_BYTES + 1)));
        assertNull(ClipboardText.decode(new byte[ClipboardText.MAX_BYTES + 1]));
    }

    @Test
    public void theLimitCountsBytesNotCharacters() {
        // Two bytes each: half the limit in characters fits, one more doesn't.
        assertNotNull(ClipboardText.encode(repeated('é', ClipboardText.MAX_BYTES / 2)));
        assertNull(ClipboardText.encode(repeated('é', ClipboardText.MAX_BYTES / 2 + 1)));
    }

    @Test
    public void malformedUtf8IsDropped() {
        assertNull(ClipboardText.decode(new byte[]{(byte) 0xC3}));
        assertNull(ClipboardText.decode(new byte[]{'a', (byte) 0xFF, 'b'}));
    }
}
