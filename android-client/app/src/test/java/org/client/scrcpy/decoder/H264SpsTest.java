package org.client.scrcpy.decoder;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertNull;

import java.nio.ByteBuffer;

import org.junit.Test;

public class H264SpsTest {
    // From the runtime's own encoder (C2SoftAvcEnc, 752x1600 stream).
    private static final String RUNTIME_PORTRAIT = "6742c02a8d680bc0c9a420202020f08846a0";
    // From x264: baseline and high profile, both orientations, and sizes that
    // need cropping (600 and 752 are not multiples of 16).
    private static final String BASELINE_LANDSCAPE = "6742c020d9006405fb0110000003001000000303c0f1832480";
    private static final String BASELINE_CROPPED = "6742c01fd900980a1e5f0110000003001000000303c0f1832480";
    private static final String HIGH_LANDSCAPE = "67640020acd9406405fb0110000003001000000303c0f1831960";
    private static final String HIGH_CROPPED_LANDSCAPE = "6764001facd9405004df970110000003001000000303c0f1831960";

    @Test
    public void readsTheRuntimeEncodersPortraitSize() {
        assertArrayEquals(new int[]{752, 1600}, size(RUNTIME_PORTRAIT, true));
    }

    @Test
    public void readsLandscapeAndCroppedSizes() {
        assertArrayEquals(new int[]{1600, 752}, size(BASELINE_LANDSCAPE, false));
        assertArrayEquals(new int[]{600, 1280}, size(BASELINE_CROPPED, false));
        assertArrayEquals(new int[]{1600, 752}, size(HIGH_LANDSCAPE, false));
        assertArrayEquals(new int[]{1280, 600}, size(HIGH_CROPPED_LANDSCAPE, true));
    }

    @Test
    public void rejectsWhatIsNotAnSps() {
        assertNull(H264Sps.pictureSize(ByteBuffer.wrap(hex("0000000168ce3c80"))));
        assertNull(H264Sps.pictureSize(ByteBuffer.wrap(hex("0000000167"))));
        assertNull(H264Sps.pictureSize((ByteBuffer) null));
    }

    private static int[] size(String nal, boolean startCode) {
        byte[] bytes = hex((startCode ? "00000001" : "") + nal);
        return H264Sps.pictureSize(ByteBuffer.wrap(bytes));
    }

    private static byte[] hex(String value) {
        byte[] out = new byte[value.length() / 2];
        for (int i = 0; i < out.length; i++) {
            out[i] = (byte) Integer.parseInt(value.substring(i * 2, i * 2 + 2), 16);
        }
        return out;
    }
}
