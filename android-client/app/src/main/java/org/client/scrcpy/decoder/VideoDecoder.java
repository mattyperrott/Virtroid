package org.client.scrcpy.decoder;

import android.media.MediaCodec;
import android.media.MediaCodecInfo;
import android.media.MediaFormat;
import android.os.Build;
import android.view.Surface;


import java.io.IOException;
import java.nio.ByteBuffer;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.locks.ReentrantReadWriteLock;

public class VideoDecoder {
    // Bounded waits keep the feeder and the renderer from spinning while never
    // holding up codec reconfiguration for longer than one wait.
    private static final long INPUT_TIMEOUT_US = 10_000L;
    private static final long OUTPUT_TIMEOUT_US = 10_000L;

    private MediaCodec mCodec;
    private volatile Worker mWorker;
    private final AtomicBoolean mIsConfigured = new AtomicBoolean(false);
    private final AtomicBoolean mFirstFrameRendered = new AtomicBoolean(false);
    private final Object lifecycleLock = new Object();
    // Feeding and draining use MediaCodec from two threads at once (read lock);
    // configure and release need it exclusively (write lock).
    private final ReentrantReadWriteLock codecLock = new ReentrantReadWriteLock();
    private Runnable firstFrameRenderedListener;

    /**
     * Queues one encoded sample.
     *
     * @return false when the sample could not be queued. The caller must then
     * resynchronise on a key frame, because later frames reference this one.
     */
    public boolean decodeSample(byte[] data, int offset, int size, long presentationTimeUs, int flags) {
        Worker worker = mWorker;
        return worker != null && worker.decodeSample(data, offset, size, presentationTimeUs, flags);
    }

    public void configure(Surface surface, int width, int height, ByteBuffer csd0, ByteBuffer csd1) {
        Worker worker = mWorker;
        if (worker != null) {
            worker.configure(surface, width, height, csd0, csd1);
        }
    }

    public void setFirstFrameRenderedListener(Runnable listener) {
        firstFrameRenderedListener = listener;
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
                releaseCodecExclusive();
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
            // MediaCodec is released only after the output worker has exited.
            releaseCodecExclusive();
        }
    }

    private void releaseCodecExclusive() {
        codecLock.writeLock().lock();
        try {
            releaseCodecLocked();
        } finally {
            codecLock.writeLock().unlock();
        }
    }

    private void releaseCodecLocked() {
        if (mCodec == null) {
            return;
        }
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

    private static void requestLowLatency(MediaCodec codec, MediaFormat format) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            // 0 = realtime priority.
            format.setInteger(MediaFormat.KEY_PRIORITY, 0);
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            try {
                MediaCodecInfo.CodecCapabilities capabilities =
                        codec.getCodecInfo().getCapabilitiesForType(MediaFormat.MIMETYPE_VIDEO_AVC);
                if (capabilities.isFeatureSupported(MediaCodecInfo.CodecCapabilities.FEATURE_LowLatency)) {
                    format.setInteger(MediaFormat.KEY_LOW_LATENCY, 1);
                }
            } catch (IllegalArgumentException ignored) {
            }
        }
    }

    private class Worker extends Thread {

        private AtomicBoolean mIsRunning = new AtomicBoolean(false);

        Worker() {
            super("virtroid-video-decoder");
        }

        private void setRunning(boolean isRunning) {
            mIsRunning.set(isRunning);
        }

        private void configure(Surface surface, int width, int height, ByteBuffer csd0, ByteBuffer csd1) {
            codecLock.writeLock().lock();
            try {
                if (!mIsRunning.get() || mWorker != this) {
                    return;
                }
                mIsConfigured.set(false);
                releaseCodecLocked();

                mFirstFrameRendered.set(false);
                MediaFormat format = MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_AVC, width, height);
                format.setByteBuffer("csd-0", csd0);
                format.setByteBuffer("csd-1", csd1);
                try {
                    mCodec = MediaCodec.createDecoderByType(MediaFormat.MIMETYPE_VIDEO_AVC);
                } catch (IOException e) {
                    throw new RuntimeException("Failed to create codec", e);
                }
                requestLowLatency(mCodec, format);
                mCodec.configure(format, surface, null, 0);
                mCodec.start();
                mIsConfigured.set(true);
            } finally {
                codecLock.writeLock().unlock();
            }
        }


        @SuppressWarnings("deprecation")
        public boolean decodeSample(byte[] data, int offset, int size, long presentationTimeUs, int flags) {
            codecLock.readLock().lock();
            try {
                MediaCodec codec = mCodec;
                if (!mIsConfigured.get() || !mIsRunning.get() || mWorker != this || codec == null) {
                    return false;
                }
                int index = codec.dequeueInputBuffer(INPUT_TIMEOUT_US);
                if (index < 0) {
                    return false;
                }
                ByteBuffer buffer;
                if (Build.VERSION.SDK_INT < Build.VERSION_CODES.LOLLIPOP) {
                    buffer = codec.getInputBuffers()[index];
                    buffer.clear();
                } else {
                    buffer = codec.getInputBuffer(index);
                }
                if (buffer == null) {
                    codec.queueInputBuffer(index, 0, 0, presentationTimeUs, 0);
                    return false;
                }
                buffer.put(data, offset, size);
                codec.queueInputBuffer(index, 0, size, presentationTimeUs, flags);
                return true;
            } catch (IllegalStateException e) {
                return false;
            } finally {
                codecLock.readLock().unlock();
            }
        }

        @Override
        public void run() {
            try {
                MediaCodec.BufferInfo info = new MediaCodec.BufferInfo();
                MediaCodec.BufferInfo newer = new MediaCodec.BufferInfo();
                while (mIsRunning.get()) {
                    boolean waitingForConfiguration = true;
                    boolean endOfStream = false;
                    codecLock.readLock().lock();
                    try {
                        MediaCodec codec = mCodec;
                        if (mIsRunning.get() && mIsConfigured.get() && codec != null) {
                            waitingForConfiguration = false;
                            int index = codec.dequeueOutputBuffer(info, OUTPUT_TIMEOUT_US);
                            if (index >= 0) {
                                // When frames have piled up, show only the newest one.
                                int next;
                                while ((info.flags & MediaCodec.BUFFER_FLAG_END_OF_STREAM) == 0
                                        && (next = codec.dequeueOutputBuffer(newer, 0)) >= 0) {
                                    codec.releaseOutputBuffer(index, false);
                                    index = next;
                                    MediaCodec.BufferInfo swap = info;
                                    info = newer;
                                    newer = swap;
                                }
                                codec.releaseOutputBuffer(index, true);
                                Runnable listener = firstFrameRenderedListener;
                                if (listener != null && mFirstFrameRendered.compareAndSet(false, true)) {
                                    listener.run();
                                }
                                endOfStream = (info.flags & MediaCodec.BUFFER_FLAG_END_OF_STREAM) != 0;
                            }
                        }
                    } finally {
                        codecLock.readLock().unlock();
                    }
                    if (endOfStream) {
                        break;
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
