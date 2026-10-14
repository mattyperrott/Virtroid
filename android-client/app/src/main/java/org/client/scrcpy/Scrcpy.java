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
import org.client.scrcpy.crypto.NodeKeyPin;
import org.client.scrcpy.crypto.ViewerEncryption;
import org.client.scrcpy.audio.PhysicalMicrophoneBridge;
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
import java.security.GeneralSecurityException;
import java.util.Arrays;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
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
    private String relayTlsPin = "";
    // The edge, used when a remote node cannot be reached directly.
    private String relayFallbackHost = "";
    private int relayFallbackPort = 0;
    private String relayToken = "";
    private String viewerPublicKey = "";
    private int viewerProtocol = 1;
    private byte[] viewerKey = null;
    private boolean audioEnabled = false;
    private boolean physicalMicrophoneEnabled = false;
    private volatile boolean physicalMicrophoneActive = false;
    private Surface surface;
    private int screenWidth;
    private int screenHeight;

    private VideoDecoder videoDecoder;
    private AudioDecoder audioDecoder;
    private final AtomicBoolean updateAvailable = new AtomicBoolean(false);
    private final FirstFrameGate firstVideoFrameGate = new FirstFrameGate();
    private final IBinder mBinder = new MyServiceBinder();
    private boolean first_time = true;
    // How far the picture is behind the quickest frame seen this session:
    // the time video spends queued on the way, or -1 before any frame.
    private volatile long streamBehindMs = -1;
    // A key frame further behind than this is skipped for a fresher one.
    private static final long LATE_KEY_FRAME_MS = 750;

    private final AtomicBoolean LetServceRunning = new AtomicBoolean(true);
    private ServiceCallbacks serviceCallbacks;
    private final int[] remote_dev_resolution = new int[2];
    private boolean socket_status = false;

    private DataInputStream socketInputStream = null;
    private DataOutputStream socketOutputStream = null;
    private volatile Socket activeSocket = null;
    private final Object socketWriteLock = new Object();
    private final ThreadPoolExecutor socketCommandExecutor = new ThreadPoolExecutor(
            1,
            1,
            0L,
            TimeUnit.MILLISECONDS,
            new ArrayBlockingQueue<>(256),
            runnable -> {
                Thread thread = new Thread(runnable, "virtroid-viewer-command");
                thread.setDaemon(true);
                return thread;
            },
            new ThreadPoolExecutor.AbortPolicy()
    );
    private final AtomicBoolean commandWriteFailureNotified = new AtomicBoolean(false);
    private final AtomicBoolean keyFrameRequestQueued = new AtomicBoolean(false);
    private final AtomicBoolean shutdownStarted = new AtomicBoolean(false);
    private PhysicalMicrophoneBridge physicalMicrophoneBridge;

    @Override
    public void onCreate() {
        super.onCreate();
        ensureForeground();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent == null) {
            shutdownViewerResources();
            stopSelf(startId);
            return START_NOT_STICKY;
        }
        physicalMicrophoneEnabled = intent.getBooleanExtra(EXTRA_PHYSICAL_MICROPHONE_ENABLED, false);
        ensureForeground();
        // A viewer transport cannot be reconstructed safely without the
        // single-use relay token and active Surface held by SessionActivity.
        return START_NOT_STICKY;
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
        firstVideoFrameGate.reset();

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

    public void start(Surface surface, String serverHost, int serverPort, boolean relayTls, String relayPath, String relayTlsPin, String relayFallbackHost, int relayFallbackPort, String relayToken, String viewerPublicKey, int viewerProtocol, String viewerKey, boolean audioEnabled, boolean physicalMicrophoneEnabled, int screenHeight, int screenWidth, int delay) {
        LetServceRunning.set(true);
        socket_status = false;
        first_time = true;
        remote_dev_resolution[0] = 0;
        remote_dev_resolution[1] = 0;
        firstVideoFrameGate.reset();

        this.serverHost = serverHost;
        this.serverPort = serverPort;
        this.relayTls = relayTls;
        this.relayPath = relayPath;
        this.relayTlsPin = relayTlsPin == null ? "" : relayTlsPin.trim();
        this.relayFallbackHost = relayFallbackHost == null ? "" : relayFallbackHost.trim();
        this.relayFallbackPort = relayFallbackPort;
        this.relayToken = relayToken;
        this.viewerPublicKey = viewerPublicKey;
        this.viewerProtocol = viewerProtocol;
        this.viewerKey = ViewerEncryption.decodeViewerKey(viewerKey);
        this.audioEnabled = audioEnabled;
        this.physicalMicrophoneEnabled = physicalMicrophoneEnabled;
        commandWriteFailureNotified.set(false);
        ensureForeground();

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

        // Request asynchronously so Surface/activity callbacks never perform
        // encrypted network I/O on Android's main thread.
        requestNewKeyFrame();
    }

    public void StopService() {
        shutdownViewerResources();
        stopSelf();
    }

    @Override
    public void onTimeout(int startId, int fgsType) {
        debugLog("viewer foreground service timed out");
        boolean ownsCleanup = beginViewerShutdown();
        // Android gives a timed-out foreground service only a few seconds to
        // leave the foreground and stop itself. Do that before waiting for
        // codec workers or AudioRecord to finish.
        stopForegroundCompat();
        stopSelf(startId);
        if (ownsCleanup) {
            Thread cleanupThread = new Thread(this::releaseViewerResources, "virtroid-viewer-timeout-cleanup");
            cleanupThread.setDaemon(true);
            cleanupThread.start();
        }
    }

    private void shutdownViewerResources() {
        if (!beginViewerShutdown()) {
            return;
        }
        releaseViewerResources();
        stopForegroundCompat();
    }

    private boolean beginViewerShutdown() {
        if (!shutdownStarted.compareAndSet(false, true)) {
            return false;
        }
        LetServceRunning.set(false);
        socketCommandExecutor.shutdownNow();
        closeActiveSocketAsync();
        return true;
    }

    private void releaseViewerResources() {
        stopPhysicalMicrophone();
        if (videoDecoder != null) {
            videoDecoder.stop();
        }
        if (audioDecoder != null) {
            audioDecoder.stop();
        }
    }

    @Override
    public void onDestroy() {
        shutdownViewerResources();
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
            int foregroundTypes = ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC;
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R && physicalMicrophoneEnabled) {
                foregroundTypes |= ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE;
            }
            startForeground(NOTIFICATION_ID, notification, foregroundTypes);
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
                .setContentText(getString(physicalMicrophoneActive
                        ? R.string.viewer_service_notification_microphone_active
                        : R.string.viewer_service_notification_body))
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
        enqueueControl(array);
    }

    public int[] get_remote_device_resolution() {
        return remote_dev_resolution;
    }

    private volatile String activeEndpointHost = "";
    private volatile int activeEndpointPort;
    private volatile String activeEndpointPin = "";

    /** The host the stream is connected to, or "" before it connects. */
    public String activeEndpointHost() {
        return activeEndpointHost;
    }

    /** How far the picture is behind, in ms, or -1 before any frame. */
    public long streamBehindMs() {
        return streamBehindMs;
    }

    public int activeEndpointPort() {
        return activeEndpointPort;
    }

    /** The node key pin the stream verified, or "" when it went through the edge. */
    public String activeEndpointPin() {
        return activeEndpointPin;
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
        enqueueControl(array);
    }

    private void startConnection(String ip, int port, int delay) {

        videoDecoder = createVideoDecoder();
        videoDecoder.start();
        audioDecoder = audioEnabled ? new AudioDecoder() : null;
        if (audioDecoder != null) {
            audioDecoder.start();
        }

        DataInputStream dataInputStream = null;
        DataOutputStream dataOutputStream = null;
        Socket socket = null;
        boolean relayTokenConsumed = false;
        int attempts = 12;
        long retryDelayMs = 250L;
        while (attempts > 0 && LetServceRunning.get()) {
            // Which step failed goes into the disconnect reason the app logs.
            String phase = "connecting";
            try {
                debugLog("Connecting to relay");
                socket = connectRelaySocket(ip, port);
                activeSocket = socket;
                if (!LetServceRunning.get()) {
                    return;
                }

                debugLog("Connected to relay");
                // The fallback clears relayTlsPin, so this records whether the
                // stream reached the node itself or the edge.
                activeEndpointHost = ip;
                activeEndpointPort = port;
                activeEndpointPin = relayTlsPin == null ? "" : relayTlsPin;
                phase = "relay handshake";
                performRelayHandshake(socket, ip, port);
                relayTokenConsumed = true;
                phase = "encrypted viewer handshake";
                ViewerEncryption.Streams encryptedStreams = ViewerEncryption.open(socket, viewerPublicKey, viewerProtocol, viewerKey);
                phase = "first stream header";

                socket.setSoTimeout(10_000);
                dataInputStream = new DataInputStream(encryptedStreams.input);
                dataOutputStream = new DataOutputStream(encryptedStreams.output);
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

                phase = "stream";
                loop(dataInputStream, dataOutputStream, delay);

            } catch (Exception e) {
                debugLog("viewer connection failed", e);
                socket_status = false;
                if (LetServceRunning.get()) {
                    // Relay tokens are single-use. Once the encrypted stream was
                    // established, the activity must issue a fresh token before
                    // reconnecting; retrying this token would only hammer the
                    // relay and leave a frozen surface marked connected.
                    if (relayTokenConsumed) {
                        if (serviceCallbacks != null) {
                            serviceCallbacks.errorDisconnect(phase + " via " + ip + ":" + port + ": " + describe(e));
                        }
                        return;
                    }
                    if (!TextUtils.isEmpty(relayTlsPin) && !TextUtils.isEmpty(relayFallbackHost) && relayFallbackPort > 0) {
                        // The node is not reachable directly from this network (for
                        // example no IPv6), and the token is still unused: go through
                        // the edge, whose node forwards the stream to the remote node.
                        debugLog("direct runtime node unreachable; using the edge relay");
                        ip = relayFallbackHost;
                        port = relayFallbackPort;
                        relayTlsPin = "";
                        continue;
                    }
                    attempts--;
                    if (attempts <= 0) {
                        if (serviceCallbacks != null) {
                            serviceCallbacks.errorDisconnect(phase + " via " + ip + ":" + port + ": " + describe(e));
                        }
                        return;
                    }
                    try {
                        Thread.sleep(retryDelayMs);
                        retryDelayMs = Math.min(retryDelayMs * 2L, 1_500L);
                    } catch (InterruptedException ignore) {
                        Thread.currentThread().interrupt();
                        return;
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
                stopPhysicalMicrophone();

            }

        }

    }

    private Socket connectRelaySocket(String host, int port) throws IOException {
        if (!relayTls) {
            throw new IOException("insecure relay transport rejected");
        }

        boolean directToNode = !TextUtils.isEmpty(relayTlsPin);
        SSLSocketFactory sslSocketFactory;
        if (directToNode) {
            // A remote node is reached by IP with a self-signed certificate; the
            // pin from the authenticated session API is the whole trust decision.
            try {
                sslSocketFactory = NodeKeyPin.socketFactory(relayTlsPin);
            } catch (GeneralSecurityException e) {
                throw new IOException("pinned runtime node TLS unavailable", e);
            }
        } else {
            sslSocketFactory = (SSLSocketFactory) SSLSocketFactory.getDefault();
        }
        SSLSocket sslSocket = (SSLSocket) sslSocketFactory.createSocket();
        // Touch packets are tiny; Nagle would hold them back waiting for ACKs.
        sslSocket.setTcpNoDelay(true);
        sslSocket.connect(new InetSocketAddress(host, port), 5000);
        // A stalled server must not hang the TLS, relay or viewer handshakes;
        // the stream itself clears this once frames flow. Keepalive notices
        // a peer that vanished without closing.
        sslSocket.setSoTimeout(15_000);
        sslSocket.setKeepAlive(true);
        if (!directToNode) {
            SSLParameters sslParameters = sslSocket.getSSLParameters();
            sslParameters.setEndpointIdentificationAlgorithm("HTTPS");
            sslSocket.setSSLParameters(sslParameters);
        }
        sslSocket.startHandshake();
        if (!directToNode) {
            TlsPins.checkPeerCertificates(host, sslSocket.getSession().getPeerCertificates());
        }
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
    public boolean requestNewKeyFrame() {
        if (!keyFrameRequestQueued.compareAndSet(false, true)) {
            return true;
        }
        boolean queued = enqueueCommand(
                CommandPacket.CmdType.VIDEO_NEW_KEY_FRAME,
                new byte[0],
                false,
                () -> keyFrameRequestQueued.set(false)
        );
        if (!queued) {
            keyFrameRequestQueued.set(false);
        }
        return queued;
    }

    private void loop(DataInputStream dataInputStream, DataOutputStream dataOutputStream, int delay) throws IOException, InterruptedException {
        VideoPacket.StreamSettings streamSettings = null;
        byte[] packetSize = new byte[4];

        // 由于网络传输存在延迟，丢弃数据包计数
        // The lowest arrival-minus-capture time seen: frames that met no queue.
        // It creeps up 10 ms a minute so the two devices' clocks may drift.
        long lastVideoOffset = Long.MAX_VALUE;
        long offsetRaisedAt = System.currentTimeMillis();
        streamBehindMs = -1;
        long lastAudioOffset = 0;

        boolean waitKeyFrame = false;


        // Control input is written by the command executor as soon as it is
        // produced, so this loop only reads and never delays a touch.
        while (LetServceRunning.get()) {
            try {
                {
                    dataInputStream.readFully(packetSize, 0, 4);
                    int size = ByteUtils.bytesToInt(packetSize);
                    if (size < 1 || size > 4 * 1024 * 1024) {  // 如果单个数据包大于 4m ，直接断开连接
                        if (serviceCallbacks != null) {
                            serviceCallbacks.errorDisconnect("stream: invalid packet size " + size);
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
                            long now = System.currentTimeMillis();
                            long offset = now - (videoPacket.presentationTimeStamp / 1000);
                            if (offset < lastVideoOffset) {
                                lastVideoOffset = offset;
                                offsetRaisedAt = now;
                            } else if (now - offsetRaisedAt >= 60_000) {
                                lastVideoOffset += 10;
                                offsetRaisedAt = now;
                            }
                            long behind = offset - lastVideoOffset;
                            streamBehindMs = streamBehindMs < 0 ? behind : (streamBehindMs * 7 + behind) / 8;
                            boolean attempted = false;
                            boolean decoded = false;
                            boolean keyFrameLate = false;
                            if (videoPacket.flag == VideoPacket.Flag.KEY_FRAME) {
                                if (behind < Math.max(delay, LATE_KEY_FRAME_MS)) {
                                    attempted = true;
                                    decoded = videoDecoder.decodeSample(packet, videoPacket.headLength(), packet.length - videoPacket.headLength(),
                                            0, videoPacket.flag.getFlag());
                                } else {
                                    keyFrameLate = true;
                                }
                            } else if (!waitKeyFrame) {
                                attempted = true;
                                decoded = videoDecoder.decodeSample(packet, videoPacket.headLength(), packet.length - videoPacket.headLength(),
                                        0, videoPacket.flag.getFlag());
                            }
                            // A dropped frame would corrupt every frame after it,
                            // so resynchronise on a fresh key frame instead.
                            boolean frameLost = attempted && !decoded;
                            if (decoded) {
                                waitKeyFrame = false;
                            } else if (keyFrameLate || frameLost) {
                                waitKeyFrame = true;
                                requestNewKeyFrame();
                            }
                        }
                        first_time = false;
                    } else if (MediaPacket.Type.getType(packet[0]) == MediaPacket.Type.COMMAND) {
                        handleRuntimeCommand(new CommandPacket().fromArray(packet));
                    } else if (audioDecoder != null && MediaPacket.Type.getType(packet[0]) == MediaPacket.Type.AUDIO) {
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
                // The outer connection loop owns reconnect policy. Propagating
                // EOF avoids an unbounded busy loop over a dead encrypted
                // stream and lets the activity obtain a fresh relay token.
                throw e;
            }
        }
    }

    private void notifyFirstVideoFrame() {
        if (firstVideoFrameGate.markDelivered() && serviceCallbacks != null) {
            serviceCallbacks.firstVideoFrame();
        }
    }

    private void handleRuntimeCommand(CommandPacket command) {
        CommandPacket.CmdType type = CommandPacket.CmdType.getFlag(command.cmdType);
        if (type != CommandPacket.CmdType.MICROPHONE_STATE || command.data == null || command.data.length != 1) {
            return;
        }
        if (command.data[0] == 2) {
            stopPhysicalMicrophone();
            if (serviceCallbacks != null) {
                serviceCallbacks.physicalMicrophoneStateChanged(false, "runtime-unavailable");
            }
            return;
        }
        boolean requested = command.data[0] == 1;
        if (!requested) {
            stopPhysicalMicrophone();
            return;
        }
        if (!physicalMicrophoneEnabled) {
            sendMicrophoneEnd();
            if (serviceCallbacks != null) {
                serviceCallbacks.physicalMicrophoneStateChanged(false, "disabled");
            }
            return;
        }
        if (physicalMicrophoneBridge == null) {
            physicalMicrophoneBridge = new PhysicalMicrophoneBridge(
                    this,
                    new PhysicalMicrophoneBridge.FrameSink() {
                        @Override
                        public void send(byte[] pcm) throws Exception {
                            if (!enqueueCommand(CommandPacket.CmdType.MICROPHONE_AUDIO, pcm, true)) {
                                throw new IOException("viewer command queue is unavailable");
                            }
                        }

                        @Override
                        public void end() {
                            sendMicrophoneEnd();
                        }
                    },
                    (active, detail) -> {
                        physicalMicrophoneActive = active;
                        ensureForeground();
                        if (serviceCallbacks != null) {
                            serviceCallbacks.physicalMicrophoneStateChanged(active, detail);
                        }
                    }
            );
        }
        physicalMicrophoneBridge.setRequested(true);
    }

    private void stopPhysicalMicrophone() {
        PhysicalMicrophoneBridge bridge = physicalMicrophoneBridge;
        physicalMicrophoneBridge = null;
        physicalMicrophoneActive = false;
        if (bridge != null) {
            bridge.stop();
        }
    }

    private void sendMicrophoneEnd() {
        enqueueCommand(CommandPacket.CmdType.MICROPHONE_END, new byte[0], true);
    }

    private boolean enqueueControl(byte[] controlData) {
        DataOutputStream output = socketOutputStream;
        if (!LetServceRunning.get() || output == null || socketCommandExecutor.isShutdown()) {
            return false;
        }
        byte[] packet = ControlPacket.toArray(MediaPacket.Type.CONTROL, controlData);
        try {
            socketCommandExecutor.execute(() -> writeCommandPacket(output, packet, true));
            return true;
        } catch (RejectedExecutionException rejected) {
            debugLog("viewer control queue rejected packet", rejected);
            return false;
        }
    }

    private boolean enqueueCommand(CommandPacket.CmdType type, byte[] data, boolean flush) {
        return enqueueCommand(type, data, flush, null);
    }

    private boolean enqueueCommand(CommandPacket.CmdType type, byte[] data, boolean flush, Runnable completion) {
        DataOutputStream output = socketOutputStream;
        if (!LetServceRunning.get() || output == null || socketCommandExecutor.isShutdown()) {
            return false;
        }
        byte[] commandData = data == null ? new byte[0] : Arrays.copyOf(data, data.length);
        byte[] packet = CommandPacket.toArray(MediaPacket.Type.COMMAND, type, commandData);
        try {
            socketCommandExecutor.execute(() -> {
                try {
                    writeCommandPacket(output, packet, flush);
                } finally {
                    if (completion != null) {
                        completion.run();
                    }
                }
            });
            return true;
        } catch (RejectedExecutionException rejected) {
            debugLog("viewer command queue rejected packet", rejected);
            return false;
        }
    }

    private void writeCommandPacket(DataOutputStream expectedOutput, byte[] packet, boolean flush) {
        try {
            synchronized (socketWriteLock) {
                if (!LetServceRunning.get() || socketOutputStream != expectedOutput) {
                    return;
                }
                expectedOutput.write(packet);
                if (flush) {
                    expectedOutput.flush();
                }
            }
        } catch (IOException error) {
            debugLog("viewer command write failed", error);
            socket_status = false;
            LetServceRunning.set(false);
            closeActiveSocketAsync();
            if (commandWriteFailureNotified.compareAndSet(false, true) && serviceCallbacks != null) {
                serviceCallbacks.errorDisconnect("sending input: " + describe(error));
            }
        }
    }

    private static void debugLog(String message) {
        if (BuildConfig.DEBUG) {
            Log.d("Scrcpy", message);
        }
    }

    private static String describe(Throwable error) {
        String message = error.getMessage();
        return error.getClass().getSimpleName() + (TextUtils.isEmpty(message) ? "" : " " + message);
    }

    private static void debugLog(String message, Throwable throwable) {
        if (BuildConfig.DEBUG) {
            Log.e("Scrcpy", message, throwable);
        }
    }

    public interface ServiceCallbacks {
        void loadNewRotation();

        void errorDisconnect(String reason);

        void firstVideoFrame();

        void physicalMicrophoneStateChanged(boolean active, String detail);
    }

    public static final String EXTRA_PHYSICAL_MICROPHONE_ENABLED =
            "org.client.scrcpy.extra.PHYSICAL_MICROPHONE_ENABLED";

    public class MyServiceBinder extends Binder {
        public Scrcpy getService() {
            return Scrcpy.this;
        }
    }


}
