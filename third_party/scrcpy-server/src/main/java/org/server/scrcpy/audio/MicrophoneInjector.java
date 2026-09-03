package org.server.scrcpy.audio;

import android.annotation.SuppressLint;
import android.annotation.TargetApi;
import android.content.Context;
import android.media.AudioAttributes;
import android.media.AudioFormat;
import android.media.AudioManager;
import android.media.AudioRecordingConfiguration;
import android.media.AudioTrack;
import android.media.MediaRecorder;
import android.os.Build;
import android.os.SystemClock;

import org.server.scrcpy.Ln;
import org.server.scrcpy.util.FakeContext;

import java.lang.reflect.Constructor;
import java.lang.reflect.Method;
import java.util.List;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * Routes phone-originated PCM into runtime microphone consumers through an Android audio policy.
 * The scrcpy server runs as com.android.shell, which owns MODIFY_AUDIO_ROUTING on Android.
 */
@TargetApi(Build.VERSION_CODES.Q)
@SuppressLint({"PrivateApi", "DiscouragedPrivateApi"})
public final class MicrophoneInjector implements AutoCloseable {
    public static final int SAMPLE_RATE = 48_000;
    public static final int FRAME_BYTES = 1_920; // 20 ms, mono, PCM 16-bit
    public static final int MAX_PACKET_BYTES = FRAME_BYTES * 2;

    public interface StateListener {
        void onStateChanged(int state);
    }

    public static final int STATE_INACTIVE = 0;
    public static final int STATE_ACTIVE = 1;
    public static final int STATE_UNAVAILABLE = 2;

    private static final int RULE_MATCH_ATTRIBUTE_CAPTURE_PRESET = 2;
    private static final int MIX_ROLE_INJECTOR = 1;
    private static final int ROUTE_FLAG_LOOP_BACK = 2;
    private static final int AUDIO_POLICY_REGISTER_SUCCESS = 0;

    private static final int[] CAPTURE_PRESETS = {
            MediaRecorder.AudioSource.DEFAULT,
            MediaRecorder.AudioSource.MIC,
            MediaRecorder.AudioSource.CAMCORDER,
            MediaRecorder.AudioSource.VOICE_RECOGNITION,
            MediaRecorder.AudioSource.VOICE_COMMUNICATION,
            MediaRecorder.AudioSource.UNPROCESSED,
            MediaRecorder.AudioSource.VOICE_PERFORMANCE,
    };

    private final StateListener stateListener;
    private final BlockingQueue<byte[]> frames = new ArrayBlockingQueue<>(8);
    private final AtomicBoolean running = new AtomicBoolean(false);
    private final AtomicBoolean demanded = new AtomicBoolean(false);

    private AudioManager audioManager;
    private Object audioPolicy;
    private AudioTrack audioTrack;
    private Thread writerThread;
    private Thread monitorThread;

    public MicrophoneInjector(StateListener stateListener) {
        this.stateListener = stateListener;
    }

    public boolean start() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q) {
            stateListener.onStateChanged(STATE_UNAVAILABLE);
            return false;
        }
        try {
            createInjectionPolicy();
            running.set(true);
            audioTrack.play();
            writerThread = new Thread(this::writeAudio, "virtroid-microphone-injector");
            monitorThread = new Thread(this::monitorRecorders, "virtroid-microphone-demand");
            writerThread.start();
            monitorThread.start();
            stateListener.onStateChanged(STATE_INACTIVE);
            return true;
        } catch (Exception error) {
            Ln.e("Physical microphone injection is unavailable", error);
            close();
            stateListener.onStateChanged(STATE_UNAVAILABLE);
            return false;
        }
    }

    public void offer(byte[] pcm) {
        if (!running.get() || !demanded.get() || pcm == null || pcm.length == 0
                || pcm.length > MAX_PACKET_BYTES || (pcm.length & 1) != 0) {
            return;
        }
        byte[] copy = pcm.clone();
        if (!frames.offer(copy)) {
            frames.poll();
            frames.offer(copy);
        }
    }

    public void endInput() {
        frames.clear();
    }

    @SuppressLint("WrongConstant")
    private void createInjectionPolicy() throws Exception {
        Context context = FakeContext.get();
        audioManager = context.getSystemService(AudioManager.class);
        if (audioManager == null) {
            throw new IllegalStateException("AudioManager is unavailable");
        }

        Class<?> ruleClass = Class.forName("android.media.audiopolicy.AudioMixingRule");
        Class<?> ruleBuilderClass = Class.forName("android.media.audiopolicy.AudioMixingRule$Builder");
        Object ruleBuilder = ruleBuilderClass.getConstructor().newInstance();
        ruleBuilderClass.getMethod("setTargetMixRole", int.class).invoke(ruleBuilder, MIX_ROLE_INJECTOR);
        Method addRule = ruleBuilderClass.getMethod("addRule", AudioAttributes.class, int.class);
        Method capturePreset = AudioAttributes.Builder.class.getDeclaredMethod("setInternalCapturePreset", int.class);
        capturePreset.setAccessible(true);
        for (int preset : CAPTURE_PRESETS) {
            AudioAttributes.Builder attributesBuilder = new AudioAttributes.Builder();
            capturePreset.invoke(attributesBuilder, preset);
            addRule.invoke(ruleBuilder, attributesBuilder.build(), RULE_MATCH_ATTRIBUTE_CAPTURE_PRESET);
        }
        Object rule = ruleBuilderClass.getMethod("build").invoke(ruleBuilder);

        AudioFormat format = new AudioFormat.Builder()
                .setSampleRate(SAMPLE_RATE)
                .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                .setChannelMask(AudioFormat.CHANNEL_OUT_MONO)
                .build();
        Class<?> mixClass = Class.forName("android.media.audiopolicy.AudioMix");
        Class<?> mixBuilderClass = Class.forName("android.media.audiopolicy.AudioMix$Builder");
        Constructor<?> mixConstructor = mixBuilderClass.getConstructor(ruleClass);
        Object mixBuilder = mixConstructor.newInstance(rule);
        mixBuilderClass.getMethod("setFormat", AudioFormat.class).invoke(mixBuilder, format);
        mixBuilderClass.getMethod("setRouteFlags", int.class).invoke(mixBuilder, ROUTE_FLAG_LOOP_BACK);
        Object mix = mixBuilderClass.getMethod("build").invoke(mixBuilder);

        Class<?> policyClass = Class.forName("android.media.audiopolicy.AudioPolicy");
        Class<?> policyBuilderClass = Class.forName("android.media.audiopolicy.AudioPolicy$Builder");
        Object policyBuilder = policyBuilderClass.getConstructor(Context.class).newInstance(context);
        policyBuilderClass.getMethod("addMix", mixClass).invoke(policyBuilder, mix);
        audioPolicy = policyBuilderClass.getMethod("build").invoke(policyBuilder);

        Method register = AudioManager.class.getDeclaredMethod("registerAudioPolicy", policyClass);
        register.setAccessible(true);
        int status = (Integer) register.invoke(audioManager, audioPolicy);
        if (status != AUDIO_POLICY_REGISTER_SUCCESS) {
            throw new IllegalStateException("Audio policy registration failed: " + status);
        }

        Method createSource = policyClass.getMethod("createAudioTrackSource", mixClass);
        audioTrack = (AudioTrack) createSource.invoke(audioPolicy, mix);
        if (audioTrack == null || audioTrack.getState() != AudioTrack.STATE_INITIALIZED) {
            throw new IllegalStateException("Audio policy did not create an injection track");
        }
    }

    private void writeAudio() {
        byte[] silence = new byte[FRAME_BYTES];
        while (running.get()) {
            try {
                byte[] frame = demanded.get() ? frames.poll(20, TimeUnit.MILLISECONDS) : null;
                byte[] output = frame == null ? silence : frame;
                int offset = 0;
                while (running.get() && offset < output.length) {
                    int written = audioTrack.write(output, offset, output.length - offset, AudioTrack.WRITE_BLOCKING);
                    if (written < 0) {
                        throw new IllegalStateException("AudioTrack write failed: " + written);
                    }
                    offset += written;
                }
            } catch (InterruptedException ignored) {
                Thread.currentThread().interrupt();
                return;
            } catch (RuntimeException error) {
                if (running.get()) {
                    Ln.e("Microphone injection stopped", error);
                    running.set(false);
                    stateListener.onStateChanged(STATE_UNAVAILABLE);
                }
                return;
            }
        }
    }

    private void monitorRecorders() {
        while (running.get()) {
            try {
                boolean active = hasMatchingRecorder(audioManager.getActiveRecordingConfigurations());
                boolean previous = demanded.getAndSet(active);
                if (active != previous) {
                    if (!active) {
                        frames.clear();
                    }
                    stateListener.onStateChanged(active ? STATE_ACTIVE : STATE_INACTIVE);
                }
            } catch (RuntimeException error) {
                if (running.getAndSet(false)) {
                    Ln.e("Could not monitor runtime microphone demand", error);
                    stateListener.onStateChanged(STATE_UNAVAILABLE);
                }
                return;
            }
            SystemClock.sleep(200);
        }
    }

    static boolean hasMatchingRecorder(List<AudioRecordingConfiguration> configurations) {
        for (AudioRecordingConfiguration configuration : configurations) {
            int source = configuration.getClientAudioSource();
            if (!configuration.isClientSilenced() && isInjectedSource(source)) {
                return true;
            }
        }
        return false;
    }

    static boolean isInjectedSource(int source) {
        for (int preset : CAPTURE_PRESETS) {
            if (source == preset) {
                return true;
            }
        }
        return false;
    }

    @Override
    public void close() {
        running.set(false);
        demanded.set(false);
        frames.clear();
        if (writerThread != null) writerThread.interrupt();
        if (monitorThread != null) monitorThread.interrupt();
        if (audioTrack != null) {
            try {
                audioTrack.stop();
            } catch (IllegalStateException ignored) {
            }
            audioTrack.release();
            audioTrack = null;
        }
        if (audioManager != null && audioPolicy != null) {
            try {
                Class<?> policyClass = Class.forName("android.media.audiopolicy.AudioPolicy");
                Method unregister = AudioManager.class.getDeclaredMethod("unregisterAudioPolicy", policyClass);
                unregister.setAccessible(true);
                unregister.invoke(audioManager, audioPolicy);
            } catch (Exception error) {
                Ln.e("Could not unregister microphone audio policy", error);
            }
        }
        audioPolicy = null;
    }
}
