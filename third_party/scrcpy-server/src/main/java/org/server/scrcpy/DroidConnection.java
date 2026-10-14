package org.server.scrcpy;


import android.net.LocalServerSocket;
import android.net.LocalSocket;
import android.net.LocalSocketAddress;

import org.server.scrcpy.model.ByteUtils;
import org.server.scrcpy.model.CommandPacket;
import org.server.scrcpy.model.ControlPacket;
import org.server.scrcpy.model.MediaPacket;

import java.io.Closeable;
import java.io.DataInputStream;
import java.io.EOFException;
import java.io.File;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;

public final class DroidConnection implements Closeable {

    /**
     * Set by the viewer service: the Unix socket the encryption proxy reaches
     * this server on, in a directory only the system user can open. Apps in
     * the runtime share its network, so a loopback port would let any of them
     * read the unencrypted screen and inject input.
     */
    private static final String SOCKET_ENV = "VIRTROID_VIEWER_SOCKET";

    private final Closeable socket;
    private final Runnable shutdown;
    private OutputStream outputStream;
    private DataInputStream inputStream;

    private DroidConnection(Closeable socket, Runnable shutdown, InputStream input, OutputStream output) {
        this.socket = socket;
        this.shutdown = shutdown;
        inputStream = new DataInputStream(input);
        outputStream = output;
    }

    private static DroidConnection acceptUnix(String path) throws IOException {
        File file = new File(path);
        if (file.exists() && !file.delete()) {
            throw new IOException("cannot replace " + path);
        }
        LocalSocket bound = new LocalSocket(LocalSocket.SOCKET_STREAM);
        bound.bind(new LocalSocketAddress(path, LocalSocketAddress.Namespace.FILESYSTEM));
        LocalServerSocket server = new LocalServerSocket(bound.getFileDescriptor());
        try {
            LocalSocket client = server.accept();
            // Small, so video the network cannot carry waits in the encoder,
            // which then skips frames, instead of queueing here.
            client.setSendBufferSize(64 * 1024);
            return new DroidConnection(client, () -> {
                try {
                    client.shutdownInput();
                    client.shutdownOutput();
                } catch (IOException ignored) {
                    // Closing below is what matters.
                }
            }, client.getInputStream(), client.getOutputStream());
        } finally {
            // Both wrap the same descriptor; closing it twice is a no-op.
            server.close();
            bound.close();
            //noinspection ResultOfMethodCallIgnored
            file.delete();
        }
    }

    private static DroidConnection acceptLoopback() throws IOException {
        // Only for development builds started over adb, without the viewer
        // service: any process in the runtime can reach this port.
        ServerSocket serverSocket = new ServerSocket(); // nosemgrep: java.lang.security.audit.crypto.unencrypted-socket.unencrypted-socket
        serverSocket.bind(new InetSocketAddress(InetAddress.getLoopbackAddress(), 7007), 1);
        try {
            Socket sock = serverSocket.accept();
            return new DroidConnection(sock, () -> {
                try {
                    sock.shutdownInput();
                    sock.shutdownOutput();
                } catch (IOException ignored) {
                    // Closing below is what matters.
                }
            }, sock.getInputStream(), sock.getOutputStream());
        } finally {
            serverSocket.close();
        }
    }

    public static DroidConnection open(String ip) throws IOException {
        String path = System.getenv(SOCKET_ENV);
        if (path != null && !path.isEmpty()) {
            return acceptUnix(path);
        }
        Ln.w("no " + SOCKET_ENV + "; serving the unencrypted viewer on loopback");
        return acceptLoopback();
    }

    public void close() throws IOException {
        shutdown.run();
        socket.close();
    }

    public OutputStream getOutputStream() {
        return outputStream;
    }


    /**
     *
     * @return
     * @throws IOException
     */
    public MediaPacket NewReceiveEvent() throws IOException {

        byte[] packetSize = new byte[4];
        inputStream.readFully(packetSize, 0, packetSize.length);

        int size = ByteUtils.bytesToInt(packetSize);

        if (size < 1 || size > 4 * 1024 * 1024) {  // 如果单个数据包大于 4m ，直接断开连接
            throw new EOFException("Event controller socket closed");
        }
        byte[] packet = new byte[size];
        inputStream.readFully(packet, 0, size);

        MediaPacket.Type type = MediaPacket.Type.getType(packet[0]);
        switch (type) {
            case CONTROL:
                return new ControlPacket().fromArray(packet);
            case COMMAND:
                return new CommandPacket().fromArray(packet);
        }


//        byte[] buf = new byte[20];
//        int n = inputStream.read(buf, 0, 20);
//        if (n == -1) {
//            throw new EOFException("Event controller socket closed");
//        }
//
//        final int[] array = new int[buf.length / 4];
//        for (int i = 0; i < array.length; i++)
//            array[i] = (((int) (buf[i * 4]) << 24) & 0xFF000000) |
//                    (((int) (buf[i * 4 + 1]) << 16) & 0xFF0000) |
//                    (((int) (buf[i * 4 + 2]) << 8) & 0xFF00) |
//                    ((int) (buf[i * 4 + 3]) & 0xFF);
//        return array;


        return null;

    }

}
