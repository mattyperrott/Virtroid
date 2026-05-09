package org.client.scrcpy.crypto;

import android.text.TextUtils;
import android.util.Base64;

import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.EOFException;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.MessageDigest;
import java.security.PublicKey;
import java.security.spec.ECGenParameterSpec;
import java.security.spec.X509EncodedKeySpec;
import java.util.Arrays;

import javax.crypto.Cipher;
import javax.crypto.KeyAgreement;
import javax.crypto.Mac;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;

public final class ViewerEncryption {
    private static final byte[] MAGIC = new byte[]{'V', 'R', 'T', 'E', 'N', 'C', '1', '\n'};
    private static final int MAX_PUBLIC_KEY = 2048;
    private static final int MAX_PLAIN_FRAME = 32 * 1024;
    private static final int MAX_CIPHER_FRAME = MAX_PLAIN_FRAME + 16;

    private ViewerEncryption() {
    }

    public static Streams open(Socket socket, String expectedServerPublicKey) throws IOException, GeneralSecurityException {
        InputStream input = socket.getInputStream();
        OutputStream output = socket.getOutputStream();

        KeyPairGenerator generator = KeyPairGenerator.getInstance("EC");
        generator.initialize(new ECGenParameterSpec("secp256r1"));
        KeyPair clientKeyPair = generator.generateKeyPair();
        byte[] clientPublic = clientKeyPair.getPublic().getEncoded();

        writeHandshake(output, clientPublic);
        byte[] serverPublic = readHandshake(input);
        verifyServerPublicKey(serverPublic, expectedServerPublicKey);
        PublicKey serverKey = KeyFactory.getInstance("EC")
            .generatePublic(new X509EncodedKeySpec(serverPublic));

        KeyAgreement agreement = KeyAgreement.getInstance("ECDH");
        agreement.init(clientKeyPair.getPrivate());
        agreement.doPhase(serverKey, true);
        byte[] sharedSecret = agreement.generateSecret();

        byte[] transcript = concat(clientPublic, serverPublic);
        TrafficKeys keys = deriveKeys(sharedSecret, transcript);
        return new Streams(
            new EncryptedInputStream(input, keys.serverToClient),
            new EncryptedOutputStream(output, keys.clientToServer)
        );
    }

    private static void verifyServerPublicKey(byte[] actual, String expected) throws GeneralSecurityException {
        if (TextUtils.isEmpty(expected)) {
            throw new GeneralSecurityException("viewer server public key is required");
        }
        byte[] expectedBytes = decodeExpectedPublicKey(expected);
        if (!MessageDigest.isEqual(expectedBytes, actual)) {
            throw new GeneralSecurityException("viewer server public key mismatch");
        }
    }

    private static byte[] decodeExpectedPublicKey(String expected) throws GeneralSecurityException {
        String trimmed = expected.trim();
        try {
            return Base64.decode(trimmed, Base64.DEFAULT);
        } catch (IllegalArgumentException first) {
            try {
                return Base64.decode(trimmed, Base64.NO_WRAP | Base64.NO_PADDING | Base64.URL_SAFE);
            } catch (IllegalArgumentException second) {
                throw new GeneralSecurityException("invalid viewer server public key", second);
            }
        }
    }

    private static byte[] readHandshake(InputStream input) throws IOException {
        byte[] magic = readFully(input, MAGIC.length);
        if (!MessageDigest.isEqual(MAGIC, magic)) {
            throw new IOException("invalid viewer encryption handshake");
        }
        byte[] lengthBytes = readFully(input, 2);
        int length = ((lengthBytes[0] & 0xff) << 8) | (lengthBytes[1] & 0xff);
        if (length <= 0 || length > MAX_PUBLIC_KEY) {
            throw new IOException("invalid viewer encryption public key length: " + length);
        }
        return readFully(input, length);
    }

    private static void writeHandshake(OutputStream output, byte[] publicKey) throws IOException {
        if (publicKey.length <= 0 || publicKey.length > MAX_PUBLIC_KEY) {
            throw new IOException("invalid viewer encryption public key length: " + publicKey.length);
        }
        output.write(MAGIC);
        output.write((publicKey.length >>> 8) & 0xff);
        output.write(publicKey.length & 0xff);
        output.write(publicKey);
        output.flush();
    }

    private static TrafficKeys deriveKeys(byte[] sharedSecret, byte[] transcript) throws GeneralSecurityException {
        byte[] salt = sha256(transcript);
        byte[] prk = hmacSha256(salt, sharedSecret);
        return new TrafficKeys(
            hkdfExpand(prk, "virtroid-viewer-e2ee-v1 client-to-runtime".getBytes(StandardCharsets.UTF_8), 32),
            hkdfExpand(prk, "virtroid-viewer-e2ee-v1 runtime-to-client".getBytes(StandardCharsets.UTF_8), 32)
        );
    }

    private static byte[] hkdfExpand(byte[] prk, byte[] info, int length) throws GeneralSecurityException {
        ByteArrayOutputStream output = new ByteArrayOutputStream();
        byte[] previous = new byte[0];
        int counter = 1;
        while (output.size() < length) {
            Mac mac = Mac.getInstance("HmacSHA256");
            mac.init(new SecretKeySpec(prk, "HmacSHA256"));
            mac.update(previous);
            mac.update(info);
            mac.update((byte) counter);
            previous = mac.doFinal();
            output.write(previous, 0, previous.length);
            counter++;
        }
        return Arrays.copyOf(output.toByteArray(), length);
    }

    private static byte[] hmacSha256(byte[] key, byte[] payload) throws GeneralSecurityException {
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(key, "HmacSHA256"));
        return mac.doFinal(payload);
    }

    private static byte[] sha256(byte[] payload) throws GeneralSecurityException {
        MessageDigest digest = MessageDigest.getInstance("SHA-256");
        return digest.digest(payload);
    }

    private static byte[] concat(byte[] left, byte[] right) {
        byte[] out = new byte[left.length + right.length];
        System.arraycopy(left, 0, out, 0, left.length);
        System.arraycopy(right, 0, out, left.length, right.length);
        return out;
    }

    private static byte[] readFully(InputStream input, int length) throws IOException {
        byte[] payload = new byte[length];
        int offset = 0;
        while (offset < length) {
            int read = input.read(payload, offset, length - offset);
            if (read < 0) {
                throw new EOFException();
            }
            offset += read;
        }
        return payload;
    }

    public static final class Streams {
        public final InputStream input;
        public final OutputStream output;

        private Streams(InputStream input, OutputStream output) {
            this.input = input;
            this.output = output;
        }
    }

    private static final class TrafficKeys {
        final byte[] clientToServer;
        final byte[] serverToClient;

        TrafficKeys(byte[] clientToServer, byte[] serverToClient) {
            this.clientToServer = clientToServer;
            this.serverToClient = serverToClient;
        }
    }

    private static final class EncryptedInputStream extends InputStream {
        private final InputStream input;
        private final byte[] key;
        private ByteArrayInputStream plaintext = new ByteArrayInputStream(new byte[0]);
        private long sequence = 0;

        EncryptedInputStream(InputStream input, byte[] key) {
            this.input = input;
            this.key = key;
        }

        @Override
        public int read() throws IOException {
            byte[] one = new byte[1];
            int read = read(one, 0, 1);
            return read < 0 ? -1 : one[0] & 0xff;
        }

        @Override
        public int read(byte[] buffer, int offset, int length) throws IOException {
            if (length == 0) {
                return 0;
            }
            while (plaintext.available() == 0) {
                readFrame();
            }
            return plaintext.read(buffer, offset, length);
        }

        @Override
        public int available() throws IOException {
            return plaintext.available();
        }

        private void readFrame() throws IOException {
            byte[] lengthBytes = readFully(input, 4);
            int frameLength = ((lengthBytes[0] & 0xff) << 24)
                | ((lengthBytes[1] & 0xff) << 16)
                | ((lengthBytes[2] & 0xff) << 8)
                | (lengthBytes[3] & 0xff);
            if (frameLength <= 0 || frameLength > MAX_CIPHER_FRAME) {
                throw new IOException("invalid encrypted frame length: " + frameLength);
            }
            byte[] ciphertext = readFully(input, frameLength);
            try {
                Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
                cipher.init(Cipher.DECRYPT_MODE, new SecretKeySpec(key, "AES"), new GCMParameterSpec(128, nonce(sequence++)));
                plaintext = new ByteArrayInputStream(cipher.doFinal(ciphertext));
            } catch (GeneralSecurityException e) {
                throw new IOException("decrypt viewer frame", e);
            }
        }
    }

    private static final class EncryptedOutputStream extends OutputStream {
        private final OutputStream output;
        private final byte[] key;
        private long sequence = 0;

        EncryptedOutputStream(OutputStream output, byte[] key) {
            this.output = output;
            this.key = key;
        }

        @Override
        public void write(int value) throws IOException {
            write(new byte[]{(byte) value}, 0, 1);
        }

        @Override
        public synchronized void write(byte[] buffer, int offset, int length) throws IOException {
            int written = 0;
            while (written < length) {
                int chunk = Math.min(MAX_PLAIN_FRAME, length - written);
                byte[] plaintext = Arrays.copyOfRange(buffer, offset + written, offset + written + chunk);
                byte[] ciphertext;
                try {
                    Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
                    cipher.init(Cipher.ENCRYPT_MODE, new SecretKeySpec(key, "AES"), new GCMParameterSpec(128, nonce(sequence++)));
                    ciphertext = cipher.doFinal(plaintext);
                } catch (GeneralSecurityException e) {
                    throw new IOException("encrypt viewer frame", e);
                }
                output.write((ciphertext.length >>> 24) & 0xff);
                output.write((ciphertext.length >>> 16) & 0xff);
                output.write((ciphertext.length >>> 8) & 0xff);
                output.write(ciphertext.length & 0xff);
                output.write(ciphertext);
                written += chunk;
            }
        }

        @Override
        public void flush() throws IOException {
            output.flush();
        }
    }

    private static byte[] nonce(long sequence) {
        byte[] nonce = new byte[12];
        nonce[4] = (byte) ((sequence >>> 56) & 0xff);
        nonce[5] = (byte) ((sequence >>> 48) & 0xff);
        nonce[6] = (byte) ((sequence >>> 40) & 0xff);
        nonce[7] = (byte) ((sequence >>> 32) & 0xff);
        nonce[8] = (byte) ((sequence >>> 24) & 0xff);
        nonce[9] = (byte) ((sequence >>> 16) & 0xff);
        nonce[10] = (byte) ((sequence >>> 8) & 0xff);
        nonce[11] = (byte) (sequence & 0xff);
        return nonce;
    }
}
