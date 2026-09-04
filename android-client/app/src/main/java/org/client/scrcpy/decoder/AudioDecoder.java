package org.client.scrcpy.decoder;

import android.media.AudioFormat;
import android.media.AudioManager;
import android.media.AudioTrack;
import android.media.MediaCodec;
import android.media.MediaFormat;
import android.os.Build;

import java.io.IOException;
import java.nio.ByteBuffer;
import java.util.concurrent.atomic.AtomicBoolean;

public class AudioDecoder {

    public static final String MIMETYPE_AUDIO_AAC = "audio/mp4a-latm";

    private MediaCodec mCodec;
    private volatile Worker mWorker;
    private final AtomicBoolean mIsConfigured = new AtomicBoolean(false);
    private final Object lifecycleLock = new Object();
    private final Object resourceLock = new Object();

    private AudioTrack audioTrack;
    private final int SAMPLE_RATE = 48000;

    private void initAudioTrack() {
        int bufferSizeInBytes = AudioTrack.getMinBufferSize(SAMPLE_RATE, AudioFormat.CHANNEL_OUT_STEREO, AudioFormat.ENCODING_PCM_16BIT);
        audioTrack = new AudioTrack(AudioManager.STREAM_MUSIC, SAMPLE_RATE, AudioFormat.CHANNEL_OUT_STEREO, AudioFormat.ENCODING_PCM_16BIT,
                bufferSizeInBytes, AudioTrack.MODE_STREAM);
    }

    public void decodeSample(byte[] data, int offset, int size, long presentationTimeUs, int flags) {
        Worker worker = mWorker;
        if (worker != null) {
            worker.decodeSample(data, offset, size, presentationTimeUs, flags);
        }
    }

    public void configure(byte[] data) {
        Worker worker = mWorker;
        if (worker != null) {
            worker.configure(data);
        }
    }


    public void start() {
        synchronized (lifecycleLock) {
            if (mWorker == null) {
                Worker worker = new Worker();
                worker.setRunning(true);
                mWorker = worker;
                worker.start();
            }
        }
    }

    public void stop() {
        synchronized (lifecycleLock) {
            Worker worker = mWorker;
            mWorker = null;
            mIsConfigured.set(false);
            if (worker == null) {
                synchronized (resourceLock) {
                    releaseResourcesLocked();
                }
                return;
            }
            worker.setRunning(false);
            worker.interrupt();
            boolean interrupted = false;
            while (worker.isAlive()) {
                try {
                    worker.join();
                } catch (InterruptedException ignored) {
                    interrupted = true;
                }
            }
            if (interrupted) {
                Thread.currentThread().interrupt();
            }
            // The worker can no longer be using either object, so releasing
            // them cannot race releaseOutputBuffer() or AudioTrack.write().
            synchronized (resourceLock) {
                releaseResourcesLocked();
            }
        }
    }

    private void releaseResourcesLocked() {
        if (mCodec != null) {
            try {
                mCodec.stop();
            } catch (IllegalStateException ignored) {
            }
            try {
                mCodec.release();
            } catch (IllegalStateException ignored) {
            }
            mCodec = null;
        }
        if (audioTrack != null) {
            try {
                audioTrack.stop();
            } catch (IllegalStateException ignored) {
            }
            try {
                audioTrack.release();
            } catch (IllegalStateException ignored) {
            }
            audioTrack = null;
        }
    }

    private class Worker extends Thread {

        private AtomicBoolean mIsRunning = new AtomicBoolean(false);

        Worker() {
            super("virtroid-audio-decoder");
        }

        private void setRunning(boolean isRunning) {
            mIsRunning.set(isRunning);
        }

        private void configure(byte[] data) {
            synchronized (resourceLock) {
                if (!mIsRunning.get() || mWorker != this) {
                    return;
                }
                mIsConfigured.set(false);
                releaseResourcesLocked();

                MediaFormat format = MediaFormat.createAudioFormat(MIMETYPE_AUDIO_AAC, SAMPLE_RATE, 2);
                format.setInteger(MediaFormat.KEY_BIT_RATE, 128000);
                format.setByteBuffer("csd-0", ByteBuffer.wrap(data));

                try {
                    mCodec = MediaCodec.createDecoderByType(MIMETYPE_AUDIO_AAC);
                } catch (IOException e) {
                    throw new RuntimeException("Failed to create codec", e);
                }
                mCodec.configure(format, null, null, 0);
                mCodec.start();

                initAudioTrack();
                audioTrack.play();
                mIsConfigured.set(true);
            }
        }


        @SuppressWarnings("deprecation")
        public void decodeSample(byte[] data, int offset, int size, long presentationTimeUs, int flags) {
            synchronized (resourceLock) {
                MediaCodec codec = mCodec;
                if (!mIsConfigured.get() || !mIsRunning.get() || mWorker != this || codec == null) {
                    return;
                }
                int index = codec.dequeueInputBuffer(0);
                if (index >= 0) {
                    ByteBuffer buffer;

                    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.LOLLIPOP) {
                        buffer = codec.getInputBuffers()[index];
                        buffer.clear();
                    } else {
                        buffer = codec.getInputBuffer(index);
                    }
                    if (buffer != null) {
                        buffer.put(data, offset, size);
                        codec.queueInputBuffer(index, 0, size, presentationTimeUs, flags);
                    }
                }
            }
        }

        @Override
        public void run() {
            try {
                MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
                while (mIsRunning.get()) {
                    boolean waitingForConfiguration = true;
                    synchronized (resourceLock) {
                        MediaCodec codec = mCodec;
                        AudioTrack track = audioTrack;
                        if (mIsRunning.get() && mIsConfigured.get() && codec != null && track != null) {
                            waitingForConfiguration = false;
                            int index = codec.dequeueOutputBuffer(info, 0);
                            if (index >= 0) {
                                if ((info.flags & MediaCodec.BUFFER_FLAG_END_OF_STREAM) == MediaCodec.BUFFER_FLAG_END_OF_STREAM) {
                                    codec.releaseOutputBuffer(index, false);
                                    break;
                                }

                                ByteBuffer outputBuffer = codec.getOutputBuffer(index);
                                if (outputBuffer != null) {
                                    byte[] data = new byte[info.size];
                                    outputBuffer.get(data);
                                    outputBuffer.clear();
                                    track.write(data, 0, info.size);
                                }
                                codec.releaseOutputBuffer(index, false);
                            }
                        }
                    }
                    if (waitingForConfiguration) {
                        try {
                            Thread.sleep(5);
                        } catch (InterruptedException ignore) {
                            if (!mIsRunning.get()) {
                                break;
                            }
                        }
                    }
                }
            } catch (RuntimeException ignored) {
            }
        }
    }
}
