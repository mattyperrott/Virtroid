package org.client.scrcpy.audio;

import android.Manifest;
import android.content.Context;
import android.content.pm.PackageManager;
import android.media.AudioFormat;
import android.media.AudioRecord;
import android.media.MediaRecorder;
import android.media.audiofx.AcousticEchoCanceler;
import android.media.audiofx.AudioEffect;
import android.media.audiofx.AutomaticGainControl;
import android.media.audiofx.NoiseSuppressor;
import android.util.Log;

import java.util.concurrent.atomic.AtomicBoolean;
import java.util.ArrayList;
import java.util.List;

/** Captures the phone microphone only while the runtime reports an active recorder. */
public final class PhysicalMicrophoneBridge {
    public static final int SAMPLE_RATE = 48_000;
    public static final int FRAME_BYTES = 1_920; // 20 ms, mono, PCM 16-bit

    public interface FrameSink {
        void send(byte[] pcm) throws Exception;
        void end();
    }

    public interface StateListener {
        void onStateChanged(boolean active, String detail);
    }

    private final Context context;
    private final FrameSink frameSink;
    private final StateListener stateListener;
    private final AtomicBoolean running = new AtomicBoolean(false);
    private AudioRecord recorder;
    private Thread captureThread;
    private final List<AudioEffect> effects = new ArrayList<>();

    public PhysicalMicrophoneBridge(Context context, FrameSink frameSink, StateListener stateListener) {
        this.context = context.getApplicationContext();
        this.frameSink = frameSink;
        this.stateListener = stateListener;
    }

    public synchronized void setRequested(boolean requested) {
        if (requested) {
            start();
        } else {
            stop();
        }
    }

    @SuppressWarnings("MissingPermission")
    private synchronized void start() {
        if (running.get()) {
            return;
        }
        if (context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            frameSink.end();
            stateListener.onStateChanged(false, "permission-required");
            return;
        }

        int minimum = AudioRecord.getMinBufferSize(
                SAMPLE_RATE,
                AudioFormat.CHANNEL_IN_MONO,
                AudioFormat.ENCODING_PCM_16BIT
        );
        if (minimum <= 0) {
            frameSink.end();
            stateListener.onStateChanged(false, "unsupported-audio-format");
            return;
        }

        AudioRecord candidate = new AudioRecord.Builder()
                .setAudioSource(MediaRecorder.AudioSource.VOICE_COMMUNICATION)
                .setAudioFormat(new AudioFormat.Builder()
                        .setSampleRate(SAMPLE_RATE)
                        .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                        .setChannelMask(AudioFormat.CHANNEL_IN_MONO)
                        .build())
                .setBufferSizeInBytes(Math.max(minimum * 2, FRAME_BYTES * 4))
                .build();
        if (candidate.getState() != AudioRecord.STATE_INITIALIZED) {
            candidate.release();
            frameSink.end();
            stateListener.onStateChanged(false, "initialization-failed");
            return;
        }

        recorder = candidate;
        enableEffects(candidate.getAudioSessionId());
        try {
            candidate.startRecording();
        } catch (RuntimeException error) {
            candidate.release();
            releaseEffects();
            recorder = null;
            frameSink.end();
            stateListener.onStateChanged(false, "capture-start-failed");
            return;
        }

        running.set(true);
        stateListener.onStateChanged(true, "active");
        captureThread = new Thread(() -> capture(candidate), "virtroid-physical-microphone");
        captureThread.start();
    }

    private void capture(AudioRecord activeRecorder) {
        byte[] frame = new byte[FRAME_BYTES];
        try {
            while (running.get()) {
                int offset = 0;
                while (running.get() && offset < frame.length) {
                    int read = activeRecorder.read(frame, offset, frame.length - offset, AudioRecord.READ_BLOCKING);
                    if (read < 0) {
                        throw new IllegalStateException("AudioRecord read failed: " + read);
                    }
                    offset += read;
                }
                if (running.get() && offset == frame.length) {
                    frameSink.send(frame.clone());
                }
            }
        } catch (Exception error) {
            if (running.getAndSet(false)) {
                stop();
                Log.w("Scrcpy", "Physical microphone bridge stopped", error);
                frameSink.end();
                stateListener.onStateChanged(false, "capture-failed");
            }
        }
    }

    public synchronized void stop() {
        boolean wasRunning = running.getAndSet(false);
        AudioRecord activeRecorder = recorder;
        recorder = null;
        if (activeRecorder != null) {
            try {
                activeRecorder.stop();
            } catch (IllegalStateException ignored) {
            }
            activeRecorder.release();
        }
        releaseEffects();
        Thread activeThread = captureThread;
        captureThread = null;
        if (activeThread != null) {
            activeThread.interrupt();
        }
        if (wasRunning) {
            frameSink.end();
            stateListener.onStateChanged(false, "inactive");
        }
    }

    private void enableEffects(int sessionId) {
        try {
            if (AcousticEchoCanceler.isAvailable()) {
                AcousticEchoCanceler effect = AcousticEchoCanceler.create(sessionId);
                if (effect != null) {
                    effect.setEnabled(true);
                    effects.add(effect);
                }
            }
            if (NoiseSuppressor.isAvailable()) {
                NoiseSuppressor effect = NoiseSuppressor.create(sessionId);
                if (effect != null) {
                    effect.setEnabled(true);
                    effects.add(effect);
                }
            }
            if (AutomaticGainControl.isAvailable()) {
                AutomaticGainControl effect = AutomaticGainControl.create(sessionId);
                if (effect != null) {
                    effect.setEnabled(true);
                    effects.add(effect);
                }
            }
        } catch (RuntimeException error) {
            Log.w("Scrcpy", "Phone audio effects are unavailable", error);
        }
    }

    private void releaseEffects() {
        for (AudioEffect effect : effects) {
            effect.release();
        }
        effects.clear();
    }
}
