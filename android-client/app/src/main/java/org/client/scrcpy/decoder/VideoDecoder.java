package org.client.scrcpy.decoder;

import android.media.MediaCodec;
import android.media.MediaFormat;
import android.os.Build;
import android.view.Surface;


import java.io.IOException;
import java.nio.ByteBuffer;
import java.util.concurrent.atomic.AtomicBoolean;

public class VideoDecoder {
    private MediaCodec mCodec;
    private volatile Worker mWorker;
    private final AtomicBoolean mIsConfigured = new AtomicBoolean(false);
    private final AtomicBoolean mFirstFrameRendered = new AtomicBoolean(false);
    private final Object lifecycleLock = new Object();
    private final Object resourceLock = new Object();
    private Runnable firstFrameRenderedListener;

    public void decodeSample(byte[] data, int offset, int size, long presentationTimeUs, int flags) {
        Worker worker = mWorker;
        if (worker != null) {
            worker.decodeSample(data, offset, size, presentationTimeUs, flags);
        }
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
                synchronized (resourceLock) {
                    releaseCodecLocked();
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
            // MediaCodec is released only after the output worker has exited.
            synchronized (resourceLock) {
                releaseCodecLocked();
            }
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

    private class Worker extends Thread {

        private AtomicBoolean mIsRunning = new AtomicBoolean(false);

        Worker() {
            super("virtroid-video-decoder");
        }

        private void setRunning(boolean isRunning) {
            mIsRunning.set(isRunning);
        }

        private void configure(Surface surface, int width, int height, ByteBuffer csd0, ByteBuffer csd1) {
            synchronized (resourceLock) {
                if (!mIsRunning.get() || mWorker != this) {
                    return;
                }
                mIsConfigured.set(false);
                releaseCodecLocked();

                mFirstFrameRendered.set(false);
                MediaFormat format = MediaFormat.createVideoFormat("video/avc", width, height);
                format.setByteBuffer("csd-0", csd0);
                format.setByteBuffer("csd-1", csd1);
                try {
                    mCodec = MediaCodec.createDecoderByType("video/avc");
                } catch (IOException e) {
                    throw new RuntimeException("Failed to create codec", e);
                }
                mCodec.configure(format, surface, null, 0);
                mCodec.start();
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
                        if (mIsRunning.get() && mIsConfigured.get() && codec != null) {
                            waitingForConfiguration = false;
                            int index = codec.dequeueOutputBuffer(info, 0);
                            if (index >= 0) {
                                codec.releaseOutputBuffer(index, true);
                                Runnable listener = firstFrameRenderedListener;
                                if (listener != null && mFirstFrameRendered.compareAndSet(false, true)) {
                                    listener.run();
                                }
                                if ((info.flags & MediaCodec.BUFFER_FLAG_END_OF_STREAM) == MediaCodec.BUFFER_FLAG_END_OF_STREAM) {
                                    break;
                                }
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
