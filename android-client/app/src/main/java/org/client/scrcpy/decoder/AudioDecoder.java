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
import java.util.concurrent.locks.ReentrantReadWriteLock;

public class AudioDecoder {

    public static final String MIMETYPE_AUDIO_AAC = "audio/mp4a-latm";
    private static final long OUTPUT_TIMEOUT_US = 10_000L;

    private MediaCodec mCodec;
    private volatile Worker mWorker;
    private final AtomicBoolean mIsConfigured = new AtomicBoolean(false);
    private final Object lifecycleLock = new Object();
    // Feeding and draining use MediaCodec from two threads at once (read lock);
    // configure and release need it exclusively (write lock). AudioTrack.write()
    // blocks in real time, so it runs under its own lock and never holds up the
    // stream reader, which also carries video.
    private final ReentrantReadWriteLock codecLock = new ReentrantReadWriteLock();
    private final Object trackLock = new Object();

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
                releaseResourcesExclusive();
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
            releaseResourcesExclusive();
        }
    }

    private void releaseResourcesExclusive() {
        codecLock.writeLock().lock();
        try {
            releaseResourcesLocked();
        } finally {
            codecLock.writeLock().unlock();
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
        synchronized (trackLock) {
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
            codecLock.writeLock().lock();
            try {
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

                synchronized (trackLock) {
                    initAudioTrack();
                    audioTrack.play();
                }
                mIsConfigured.set(true);
            } finally {
                codecLock.writeLock().unlock();
            }
        }


        @SuppressWarnings("deprecation")
        public void decodeSample(byte[] data, int offset, int size, long presentationTimeUs, int flags) {
            codecLock.readLock().lock();
            try {
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
                // No free input buffer: drop this audio packet rather than stall the stream.
            } catch (IllegalStateException ignored) {
            } finally {
                codecLock.readLock().unlock();
            }
        }

        @Override
        public void run() {
            try {
                MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
                while (mIsRunning.get()) {
                    boolean waitingForConfiguration = true;
                    boolean endOfStream = false;
                    byte[] pcm = null;
                    codecLock.readLock().lock();
                    try {
                        MediaCodec codec = mCodec;
                        if (mIsRunning.get() && mIsConfigured.get() && codec != null && audioTrack != null) {
                            waitingForConfiguration = false;
                            int index = codec.dequeueOutputBuffer(info, OUTPUT_TIMEOUT_US);
                            if (index >= 0) {
                                if ((info.flags & MediaCodec.BUFFER_FLAG_END_OF_STREAM) == MediaCodec.BUFFER_FLAG_END_OF_STREAM) {
                                    endOfStream = true;
                                } else {
                                    ByteBuffer outputBuffer = codec.getOutputBuffer(index);
                                    if (outputBuffer != null) {
                                        pcm = new byte[info.size];
                                        outputBuffer.get(pcm);
                                        outputBuffer.clear();
                                    }
                                }
                                codec.releaseOutputBuffer(index, false);
                            }
                        }
                    } finally {
                        codecLock.readLock().unlock();
                    }
                    if (endOfStream) {
                        break;
                    }
                    if (pcm != null) {
                        synchronized (trackLock) {
                            AudioTrack track = audioTrack;
                            if (track != null && mIsRunning.get()) {
                                track.write(pcm, 0, pcm.length);
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
