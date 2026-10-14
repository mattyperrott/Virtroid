package org.client.scrcpy.crypto;

import static org.junit.Assert.assertEquals;

import java.nio.charset.StandardCharsets;
import java.util.Arrays;

import org.junit.Test;

/** The protocol 2 values must match viewercrypt's TestViewerProtocolV2Vectors. */
public class ViewerEncryptionV2Test {
    @Test
    public void handshakeMatchesViewercrypt() throws Exception {
        byte[] viewerKey = filled(0x11, 32);
        byte[] transcript = ViewerEncryption.transcriptV2(
            "client-public".getBytes(StandardCharsets.UTF_8),
            "server-public".getBytes(StandardCharsets.UTF_8),
            filled(0x22, 32));
        assertEquals("711bcdb68798c5884260f3420cd35d55d311faddc4b40634688f766acce64121", hex(transcript));
        assertEquals("04746ae333598f9388dbbc93e53bde921aeba012cd8bfcdb8c2d65b2ea0202b1",
            hex(ViewerEncryption.proof(viewerKey, "virtroid-viewer-auth-v2 client\n", transcript)));
        assertEquals("298b70ed7a278845269d669a91f7f220442eae8d4d0122fff011fdf1ce9a2245",
            hex(ViewerEncryption.proof(viewerKey, "virtroid-viewer-auth-v2 runtime\n", transcript)));
        ViewerEncryption.TrafficKeys keys = ViewerEncryption.deriveKeysV2(filled(0x33, 32), transcript, viewerKey);
        assertEquals("9bbaeb35a49bab0fec8f4c1cac61f8f10dd90aa703d40addc4dfa96411b7d288", hex(keys.clientToServer));
        assertEquals("3167ce4de470e13b0df113cd1ba9591b66b38d067c150d130132646facf5dc4f", hex(keys.serverToClient));
    }

    private static byte[] filled(int value, int length) {
        byte[] bytes = new byte[length];
        Arrays.fill(bytes, (byte) value);
        return bytes;
    }

    private static String hex(byte[] bytes) {
        StringBuilder out = new StringBuilder();
        for (byte b : bytes) {
            out.append(String.format("%02x", b));
        }
        return out.toString();
    }
}
