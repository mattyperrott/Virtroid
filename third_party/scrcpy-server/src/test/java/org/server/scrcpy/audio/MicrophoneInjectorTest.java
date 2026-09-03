package org.server.scrcpy.audio;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import android.media.MediaRecorder;

import org.junit.Test;

public class MicrophoneInjectorTest {
    @Test
    public void injectsInteractiveAndRecordingSources() {
        assertTrue(MicrophoneInjector.isInjectedSource(MediaRecorder.AudioSource.MIC));
        assertTrue(MicrophoneInjector.isInjectedSource(MediaRecorder.AudioSource.VOICE_COMMUNICATION));
        assertTrue(MicrophoneInjector.isInjectedSource(MediaRecorder.AudioSource.CAMCORDER));
        assertTrue(MicrophoneInjector.isInjectedSource(MediaRecorder.AudioSource.VOICE_RECOGNITION));
    }

    @Test
    public void doesNotTreatRuntimePlaybackCaptureAsMicrophoneDemand() {
        assertFalse(MicrophoneInjector.isInjectedSource(MediaRecorder.AudioSource.REMOTE_SUBMIX));
        assertFalse(MicrophoneInjector.isInjectedSource(MediaRecorder.AudioSource.VOICE_CALL));
    }
}
