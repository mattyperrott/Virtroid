package org.client.scrcpy;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Binder;
import android.os.Build;
import android.os.IBinder;
import android.text.TextUtils;
import android.util.Log;
import android.view.MotionEvent;
import android.view.Surface;

import io.virtroid.client.BuildConfig;
import io.virtroid.client.R;
import io.virtroid.client.security.TlsPins;

import org.client.scrcpy.decoder.AudioDecoder;
import org.client.scrcpy.decoder.VideoDecoder;
import org.client.scrcpy.crypto.ViewerEncryption;
import org.client.scrcpy.model.AudioPacket;
import org.client.scrcpy.model.ByteUtils;
import org.client.scrcpy.model.CommandPacket;
import org.client.scrcpy.model.ControlPacket;
import org.client.scrcpy.model.MediaPacket;
import org.client.scrcpy.model.VideoPacket;
import org.client.scrcpy.utils.Util;

import java.io.DataInputStream;
import java.io.DataOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.util.LinkedList;
import java.util.Queue;
import java.util.concurrent.atomic.AtomicBoolean;

import javax.net.ssl.SSLParameters;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;


public class Scrcpy extends Service {

    public static final String LOCAL_IP = "127.0.0.1";
    // 本地画面转发占用的端口
    public static final int LOCAL_FORWART_PORT = 7008;
    private static final String NOTIFICATION_CHANNEL_ID = "virtroid_viewer_session";
    private static final int NOTIFICATION_ID = 7318;

    public static final int DEFAULT_ADB_PORT = 5555;
    private String serverHost;
    private int serverPort = DEFAULT_ADB_PORT;
    private boolean relayTls = false;
    private String relayPath = "";
    private String relayToken = "";
    private String viewerPublicKey = "";
    private Surface surface;
    private int screenWidth;
    private int screenHeight;

    private final Queue<byte[]> event = new LinkedList<byte[]>();
    // private byte[] event = null;
    private VideoDecoder videoDecoder;
    private AudioDecoder audioDecoder;
    private final AtomicBoolean updateAvailable = new AtomicBoolean(false);
    private final AtomicBoolean firstVideoFrameDelivered = new AtomicBoolean(false);
    private final IBinder mBinder = new MyServiceBinder();
    private boolean first_time = true;

    private final AtomicBoolean LetServceRunning = new AtomicBoolean(true);
    private ServiceCallbacks serviceCallbacks;
    private final int[] remote_dev_resolution = new int[2];
    private boolean socket_status = false;

    private DataInputStream socketInputStream = null;
    private DataOutputStream socketOutputStream = null;
    private volatile Socket activeSocket = null;

    @Override
    public void onCreate() {
        super.onCreate();
        ensureForeground();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        ensureForeground();
        return START_STICKY;
    }

    @Override
    public IBinder onBind(Intent intent) {
        return mBinder;
    }

    public void setServiceCallbacks(ServiceCallbacks callbacks) {
        serviceCallbacks = callbacks;
    }

    public void setParms(Surface NewSurface, int NewWidth, int NewHeight) {
        this.screenWidth = NewWidth;
        this.screenHeight = NewHeight;
        this.surface = NewSurface;

        if (videoDecoder != null) {
            videoDecoder.start();
        }
        if (audioDecoder != null) {
            audioDecoder.start();
        }


        updateAvailable.set(true);

    }

    private VideoDecoder createVideoDecoder() {
        VideoDecoder decoder = new VideoDecoder();
        decoder.setFirstFrameRenderedListener(this::notifyFirstVideoFrame);
        return decoder;
    }

    public void start(Surface surface, String serverHost, int serverPort, boolean relayTls, String relayPath, String relayToken, String viewerPublicKey, int screenHeight, int screenWidth, int delay) {
        LetServceRunning.set(true);
        socket_status = false;
        first_time = true;
        remote_dev_resolution[0] = 0;
        remote_dev_resolution[1] = 0;
        firstVideoFrameDelivered.set(false);

        this.videoDecoder = createVideoDecoder();
        videoDecoder.start();

        this.audioDecoder = new AudioDecoder();
        audioDecoder.start();

        this.serverHost = serverHost;
        this.serverPort = serverPort;
        this.relayTls = relayTls;
        this.relayPath = relayPath;
        this.relayToken = relayToken;
        this.viewerPublicKey = viewerPublicKey;

        this.screenHeight = screenHeight;
        this.screenWidth = screenWidth;
        this.surface = surface;
        Thread thread = new Thread(new Runnable() {
            @Override
            public void run() {
                startConnection(serverHost, serverPort, delay);
            }
        });
        thread.start();
    }

    public void pause() {
        if (videoDecoder != null) {
            videoDecoder.stop();
        }

        if (audioDecoder != null) {
            audioDecoder.stop();
        }
    }

    public void resume() {
        if (videoDecoder != null) {
            videoDecoder.start();
        }
        if (audioDecoder != null) {
            audioDecoder.start();
        }
        updateAvailable.set(true);

        try {  // 请求关键帧, 避免花屏
            requestNewKeyFrame();
        } catch (IOException e) {
            debugLog("request keyframe failed", e);
        }
    }

    public void StopService() {
        LetServceRunning.set(false);
        stopForegroundCompat();
        closeActiveSocketAsync();
        if (videoDecoder != null) {
            videoDecoder.stop();
        }
        if (audioDecoder != null) {
            audioDecoder.stop();
        }
        stopSelf();
    }

    @Override
    public void onDestroy() {
        LetServceRunning.set(false);
        closeActiveSocketAsync();
        if (videoDecoder != null) {
            videoDecoder.stop();
        }
        if (audioDecoder != null) {
            audioDecoder.stop();
        }
        stopForegroundCompat();
        super.onDestroy();
    }

    private void closeActiveSocketAsync() {
        Socket socket = activeSocket;
        activeSocket = null;
        if (socket == null) {
            return;
        }
        Thread closeThread = new Thread(() -> {
            try {
                socket.close();
            } catch (IOException e) {
                debugLog("close active socket failed", e);
            }
        }, "virtroid-viewer-socket-close");
        closeThread.start();
    }

    private void ensureForeground() {
        createNotificationChannel();
        Notification notification = buildForegroundNotification();
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC);
        } else {
            startForeground(NOTIFICATION_ID, notification);
        }
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return;
        }
        NotificationManager notificationManager = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        if (notificationManager == null || notificationManager.getNotificationChannel(NOTIFICATION_CHANNEL_ID) != null) {
            return;
        }
        NotificationChannel channel = new NotificationChannel(
                NOTIFICATION_CHANNEL_ID,
                getString(R.string.viewer_service_channel_name),
                NotificationManager.IMPORTANCE_LOW
        );
        channel.setShowBadge(false);
        notificationManager.createNotificationChannel(channel);
    }

    private Notification buildForegroundNotification() {
        Intent launchIntent = getPackageManager().getLaunchIntentForPackage(getPackageName());
        if (launchIntent == null) {
            launchIntent = new Intent();
        }
        launchIntent.addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP | Intent.FLAG_ACTIVITY_CLEAR_TOP);
        int pendingIntentFlags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            pendingIntentFlags |= PendingIntent.FLAG_IMMUTABLE;
        }
        PendingIntent contentIntent = PendingIntent.getActivity(this, 0, launchIntent, pendingIntentFlags);

        Notification.Builder builder;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            builder = new Notification.Builder(this, NOTIFICATION_CHANNEL_ID);
        } else {
            builder = new Notification.Builder(this);
        }
        return builder
                .setSmallIcon(R.drawable.ic_phone)
                .setContentTitle(getString(R.string.viewer_service_notification_title))
                .setContentText(getString(R.string.viewer_service_notification_body))
                .setContentIntent(contentIntent)
                .setOngoing(true)
                .setOnlyAlertOnce(true)
                .setCategory(Notification.CATEGORY_SERVICE)
                .build();
    }

    private void stopForegroundCompat() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(STOP_FOREGROUND_REMOVE);
        } else {
            stopForeground(true);
        }
    }


    public boolean touchevent(MotionEvent touch_event, boolean landscape, int displayW, int displayH) {
        if (!socket_status || displayW <= 0 || displayH <= 0 || screenWidth <= 0 || screenHeight <= 0 ||
                remote_dev_resolution[0] <= 0 || remote_dev_resolution[1] <= 0) {
            return false;
        }

        float remoteW;
        float remoteH;
        float realH;
        float realW;

        if (landscape) {  // 横屏的话，宽高相反
            remoteW = Math.max(remote_dev_resolution[0], remote_dev_resolution[1]);
            remoteH = Math.min(remote_dev_resolution[0], remote_dev_resolution[1]);

            realW = Math.min(remoteW, screenWidth);
            realH = realW * remoteH / remoteW;
        } else {
            remoteW = Math.min(remote_dev_resolution[0], remote_dev_resolution[1]);
            remoteH = Math.max(remote_dev_resolution[0], remote_dev_resolution[1]);
            realH = Math.min(remoteH, screenHeight);
            realW = realH * remoteW / remoteH;
        }

        int actionIndex = touch_event.getActionIndex();
        int pointerId = touch_event.getPointerId(actionIndex);
        int pointCount = touch_event.getPointerCount();
        // Log.e("Scrcpy", "pointer id: " + pointerId + " , action: " + touch_event.getAction() + " ,point count: " + pointCount + " x: " + touch_event.getX() + " y: " + touch_event.getY());

        switch (touch_event.getAction()) {
            case MotionEvent.ACTION_MOVE: // 所有手指移动
                // 遍历所有触摸点，使用 pointerId 和 pointerIndex 来获取所有触摸点的信息
                for (int i = 0; i < touch_event.getPointerCount(); i++) {
                    int currentPointerId = touch_event.getPointerId(i);
                    int x = (int) touch_event.getX(i);
                    int y = (int) touch_event.getY(i);
                    // 处理每一个触摸点的x, y坐标
                    // Log.e("Scrcpy", "触摸移动，index : " + i + " ,x : " + x + " , y: " + y + " ,currentPointerId: " + currentPointerId);
                    sendTouchEvent(touch_event.getAction(), touch_event.getButtonState(), (int) (x * realW / displayW), (int) (y * realH / displayH), currentPointerId);
                }
                break;
            case MotionEvent.ACTION_POINTER_UP: // 中间手指抬起
            case MotionEvent.ACTION_UP: // 最后一个手指抬起
            case MotionEvent.ACTION_DOWN: // 第一个手指按下
            case MotionEvent.ACTION_POINTER_DOWN: // 中间的手指按下
            default:
                sendTouchEvent(touch_event.getAction(), touch_event.getButtonState(), (int) (touch_event.getX() * realW / displayW), (int) (touch_event.getY() * realH / displayH), pointerId);
                break;

        }
        return true;
    }

    private void sendTouchEvent(int action, int buttonState, int x, int y, int pointerId) {
        // 为支持多点触控，将 pointid 添加到最末尾
        // TODO : 后续需要改造 event 传输方式
        int[] buf = new int[]{action, buttonState, x, y, pointerId};
        final byte[] array = new byte[buf.length * 4]; // https://stackoverflow.com/questions/2183240/java-integer-to-byte-array
        for (int j = 0; j < buf.length; j++) {
            final int c = buf[j];
            array[j * 4] = (byte) ((c & 0xFF000000) >> 24);
            array[j * 4 + 1] = (byte) ((c & 0xFF0000) >> 16);
            array[j * 4 + 2] = (byte) ((c & 0xFF00) >> 8);
            array[j * 4 + 3] = (byte) (c & 0xFF);
        }
        if (LetServceRunning.get()) {
            event.offer(array);
        }
        // event = array;
    }

    public int[] get_remote_device_resolution() {
        return remote_dev_resolution;
    }

    public boolean check_socket_connection() {
        return socket_status;
    }

    public void sendKeyevent(int keycode) {
        // The server expects control packets to follow the same int-array shape as touch events.
        int[] buf = new int[]{keycode, 0, 0, 0, 0};

        final byte[] array = new byte[buf.length * 4];   // https://stackoverflow.com/questions/2183240/java-integer-to-byte-array
        for (int j = 0; j < buf.length; j++) {
            final int c = buf[j];
            array[j * 4] = (byte) ((c & 0xFF000000) >> 24);
            array[j * 4 + 1] = (byte) ((c & 0xFF0000) >> 16);
            array[j * 4 + 2] = (byte) ((c & 0xFF00) >> 8);
            array[j * 4 + 3] = (byte) (c & 0xFF);
        }
        if (LetServceRunning.get()) {
            event.offer(array);
            // event = array;
        }
    }

    private void startConnection(String ip, int port, int delay) {

        videoDecoder = createVideoDecoder();
        videoDecoder.start();
        audioDecoder = new AudioDecoder();
        audioDecoder.start();

        DataInputStream dataInputStream = null;
        DataOutputStream dataOutputStream = null;
        Socket socket = null;
        boolean firstConnect = true;
        int attempts = 50;
        while (attempts > 0 && LetServceRunning.get()) {
            try {
                debugLog("Connecting to relay");
                socket = connectRelaySocket(ip, port);
                activeSocket = socket;
                if (!LetServceRunning.get()) {
                    return;
                }

                debugLog("Connected to relay");
                performRelayHandshake(socket, ip, port);
                ViewerEncryption.Streams encryptedStreams = ViewerEncryption.open(socket, viewerPublicKey);

                // 能够正常进行连接，说明可能建立了 tcp 连接，需要等待数据
                // 一次等待时间为 2s ，最多等待五次，也就是 10秒
                if (firstConnect) {  // 此处有 while 循环，不能一直设置为10
                    firstConnect = false;
                    // waitResolutionCount 为 10，等待100ms 也就是共计一秒钟，设置attempts 为 5，也就是 5秒后则退出
                    attempts = 5;
                }
                socket.setSoTimeout(10_000);
                dataInputStream = new DataInputStream(encryptedStreams.input);
                dataOutputStream = new DataOutputStream(encryptedStreams.output);
                attempts = 0;
                byte[] buf = new byte[8];
                dataInputStream.readFully(buf, 0, 8);
                socket.setSoTimeout(0);
                for (int i = 0; i < remote_dev_resolution.length; i++) {
                    remote_dev_resolution[i] = (((int) (buf[i * 4]) << 24) & 0xFF000000) |
                            (((int) (buf[i * 4 + 1]) << 16) & 0xFF0000) |
                            (((int) (buf[i * 4 + 2]) << 8) & 0xFF00) |
                            ((int) (buf[i * 4 + 3]) & 0xFF);
                }
                if (remote_dev_resolution[0] > remote_dev_resolution[1]) {
                    first_time = false;
                    int i = remote_dev_resolution[0];
                    remote_dev_resolution[0] = remote_dev_resolution[1];
                    remote_dev_resolution[1] = i;
                }

                socketInputStream = dataInputStream;
                socketOutputStream = dataOutputStream;

                socket_status = true;

                loop(dataInputStream, dataOutputStream, delay);

            } catch (Exception e) {
                debugLog("viewer connection failed", e);
                if (LetServceRunning.get()) {
                    attempts--;
                    if (attempts < 0) {
                        socket_status = false;

                        if (serviceCallbacks != null) {
                            serviceCallbacks.errorDisconnect();
                        }
                        return;
                    }
                    try {
                        Thread.sleep(100);
                    } catch (InterruptedException ignore) {
                    }
                }
                debugLog("viewer connection retry scheduled");
            } finally {
                if (socket != null) {
                    try {
                        socket.close();
                    } catch (IOException e) {
                        debugLog("close socket failed", e);
                    }
                }
                if (dataOutputStream != null) {
                    try {
                        dataOutputStream.close();
                    } catch (IOException e) {
                        debugLog("close output stream failed", e);
                    }
                }
                if (dataInputStream != null) {
                    try {
                        dataInputStream.close();
                    } catch (IOException e) {
                        debugLog("close input stream failed", e);
                    }
                }
                socketInputStream = null;
                socketOutputStream = null;
                activeSocket = null;
                // 清除事件队列
                event.clear();

            }

        }

    }

    private Socket connectRelaySocket(String host, int port) throws IOException {
        Socket socket = new Socket();
        socket.connect(new InetSocketAddress(host, port), 5000);
        if (!relayTls) {
            socket.close();
            throw new IOException("insecure relay transport rejected");
        }

        SSLSocketFactory sslSocketFactory = (SSLSocketFactory) SSLSocketFactory.getDefault();
        SSLSocket sslSocket = (SSLSocket) sslSocketFactory.createSocket(socket, host, port, true);
        SSLParameters sslParameters = sslSocket.getSSLParameters();
        sslParameters.setEndpointIdentificationAlgorithm("HTTPS");
        sslSocket.setSSLParameters(sslParameters);
        sslSocket.startHandshake();
        TlsPins.checkPeerCertificates(host, sslSocket.getSession().getPeerCertificates());
        return sslSocket;
    }

    private void performRelayHandshake(Socket socket, String host, int port) throws IOException {
        if (TextUtils.isEmpty(relayPath)) {
            return;
        }

        OutputStream outputStream = socket.getOutputStream();
        String request =
                "GET " + relayPath + " HTTP/1.1\r\n" +
                "Host: " + host + ":" + port + "\r\n" +
                "Connection: Upgrade\r\n" +
                "Upgrade: virtroid-relay\r\n" +
                "X-Virtroid-Relay-Token: " + relayToken + "\r\n" +
                "\r\n";
        outputStream.write(request.getBytes("UTF-8"));
        outputStream.flush();

        InputStream inputStream = socket.getInputStream();
        String statusLine = readHttpLine(inputStream);
        if (TextUtils.isEmpty(statusLine) || (!statusLine.contains(" 101 ") && !statusLine.contains(" 200 "))) {
            throw new IOException("relay connect failed: " + statusLine);
        }
        while (true) {
            String headerLine = readHttpLine(inputStream);
            if (headerLine == null || headerLine.isEmpty()) {
                break;
            }
        }
    }

    private String readHttpLine(InputStream inputStream) throws IOException {
        StringBuilder builder = new StringBuilder();
        int previous = -1;
        while (true) {
            int value = inputStream.read();
            if (value < 0) {
                if (builder.length() == 0) {
                    return null;
                }
                break;
            }
            if (previous == '\r' && value == '\n') {
                builder.setLength(Math.max(builder.length() - 1, 0));
                break;
            }
            builder.append((char) value);
            previous = value;
        }
        return builder.toString();
    }

    /**
     * Request Keyframe
     * 请求关键帧
     */
    public boolean requestNewKeyFrame() throws IOException {
        if (LetServceRunning.get() && socketOutputStream != null) {
            socketOutputStream.write(CommandPacket.toArray(MediaPacket.Type.COMMAND, CommandPacket.CmdType.VIDEO_NEW_KEY_FRAME, new byte[0]));
            return true;
        }
        return false;
    }

    private void loop(DataInputStream dataInputStream, DataOutputStream dataOutputStream, int delay) throws InterruptedException {
        VideoPacket.StreamSettings streamSettings = null;
        byte[] packetSize = new byte[4];

        // 由于网络传输存在延迟，丢弃数据包计数
        long lastVideoOffset = 0;
        long lastAudioOffset = 0;

        boolean waitKeyFrame = false;


        while (LetServceRunning.get()) {
            boolean waitEvent = true;
            try {
                byte[] sendevent = event.poll();
                if (sendevent != null) {
                    waitEvent = false;
                    try {
                        byte[] data = ControlPacket.toArray(MediaPacket.Type.CONTROL, sendevent);
                        dataOutputStream.write(data);
                    } catch (IOException e) {
                        debugLog("write control packet failed", e);
                        if (serviceCallbacks != null) {
                            serviceCallbacks.errorDisconnect();
                        }
                        LetServceRunning.set(false);
                    } finally {
                        // event = null;
                    }
                }

                {
                    waitEvent = false;
                    dataInputStream.readFully(packetSize, 0, 4);
                    int size = ByteUtils.bytesToInt(packetSize);
                    if (size > 4 * 1024 * 1024) {  // 如果单个数据包大于 4m ，直接断开连接
                        if (serviceCallbacks != null) {
                            serviceCallbacks.errorDisconnect();
                        }
                        LetServceRunning.set(false);
                        return;
                    }
                    byte[] packet = new byte[size];
                    dataInputStream.readFully(packet, 0, size);
                    if (MediaPacket.Type.getType(packet[0]) == MediaPacket.Type.VIDEO) {
                        VideoPacket videoPacket = VideoPacket.readHead(packet);
                        // byte[] data = videoPacket.data;
                        if (videoPacket.flag == VideoPacket.Flag.CONFIG || updateAvailable.get()) {
                            if (!updateAvailable.get()) {
                                int dataLength = packet.length - videoPacket.headLength();
                                byte[] data = new byte[dataLength];
                                System.arraycopy(packet, videoPacket.headLength(), data, 0, dataLength);
                                streamSettings = VideoPacket.getStreamSettings(data);
                                if (!first_time) {
                                    if (serviceCallbacks != null) {
                                        serviceCallbacks.loadNewRotation();
                                    }
                                    while (!updateAvailable.get()) {
                                        // Waiting for new surface
                                        try {
                                            Thread.sleep(100);
                                        } catch (InterruptedException e) {
                                            debugLog("wait for surface interrupted", e);
                                        }
                                    }

                                }
                            }
                            updateAvailable.set(false);
                            if (streamSettings != null) {
                                // Configure the decoder for the encoded stream dimensions, not the local view size.
                                videoDecoder.configure(
                                        surface,
                                        remote_dev_resolution[0],
                                        remote_dev_resolution[1],
                                        streamSettings.sps,
                                        streamSettings.pps
                                );
                            }
                        } else if (videoPacket.flag == VideoPacket.Flag.END) {
                            // need close stream
                            debugLog("Video stream ended");
                        } else {
                            // Log.e("Scrcpy", "videoPacket presentationTimeStamp ... " + videoPacket.presentationTimeStamp);
                            // 帧在 100 ms 以内
                            if (lastVideoOffset == 0) {
                                lastVideoOffset = System.currentTimeMillis() - (videoPacket.presentationTimeStamp / 1000);
                            }
                            if (videoPacket.flag == VideoPacket.Flag.KEY_FRAME) {
                                if (System.currentTimeMillis() - (lastVideoOffset + (videoPacket.presentationTimeStamp / 1000)) < delay) {
                                    waitKeyFrame = false;
                                    videoDecoder.decodeSample(packet, videoPacket.headLength(), packet.length - videoPacket.headLength(),
                                            0, videoPacket.flag.getFlag());
                                } else {
                                    waitKeyFrame = true;
                                    requestNewKeyFrame();
                                }
                            } else {
                                if (!waitKeyFrame) {
                                    videoDecoder.decodeSample(packet, videoPacket.headLength(), packet.length - videoPacket.headLength(),
                                            0, videoPacket.flag.getFlag());
                                }
                            }
                        }
                        first_time = false;
                    } else if (MediaPacket.Type.getType(packet[0]) == MediaPacket.Type.AUDIO) {
                        AudioPacket audioPacket = AudioPacket.readHead(packet);
                        // byte[] data = audioPacket.data;
                        if (audioPacket.flag == AudioPacket.Flag.CONFIG) {
                            int dataLength = packet.length - audioPacket.headLength();
                            byte[] data = new byte[dataLength];
                            System.arraycopy(packet, audioPacket.headLength(), data, 0, dataLength);
                            audioDecoder.configure(data);
                        } else if (audioPacket.flag == AudioPacket.Flag.END) {
                            // need close stream
                            debugLog("Audio stream ended");
                        } else {
                            if (lastAudioOffset == 0) {
                                lastAudioOffset = System.currentTimeMillis() - (audioPacket.presentationTimeStamp / 1000);
                            }
                            if (System.currentTimeMillis() - (lastAudioOffset + (audioPacket.presentationTimeStamp / 1000)) < delay) {
                                audioDecoder.decodeSample(packet, audioPacket.headLength(), packet.length - audioPacket.headLength(),
                                        0, audioPacket.flag.getFlag());
                            }
                        }
                    }

                }
            } catch (IOException e) {
                debugLog("viewer loop I/O failed", e);
            } finally {
                if (waitEvent) {
                    Thread.sleep(5);
                }
            }
        }
    }

    private void notifyFirstVideoFrame() {
        if (firstVideoFrameDelivered.compareAndSet(false, true) && serviceCallbacks != null) {
            serviceCallbacks.firstVideoFrame();
        }
    }

    private static void debugLog(String message) {
        if (BuildConfig.DEBUG) {
            Log.d("Scrcpy", message);
        }
    }

    private static void debugLog(String message, Throwable throwable) {
        if (BuildConfig.DEBUG) {
            Log.e("Scrcpy", message, throwable);
        }
    }

    public interface ServiceCallbacks {
        void loadNewRotation();

        void errorDisconnect();

        void firstVideoFrame();
    }

    public class MyServiceBinder extends Binder {
        public Scrcpy getService() {
            return Scrcpy.this;
        }
    }


}
