package org.server.scrcpy.model;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;

import org.junit.Test;

public class CommandPacketTest {
    @Test
    public void microphoneAudioRoundTrips() {
        byte[] pcm = new byte[]{1, 2, 3, 4};
        byte[] framed = CommandPacket.toArray(
                MediaPacket.Type.COMMAND,
                CommandPacket.CmdType.MICROPHONE_AUDIO,
                pcm
        );
        byte[] body = new byte[framed.length - 4];
        System.arraycopy(framed, 4, body, 0, body.length);

        CommandPacket decoded = new CommandPacket().fromArray(body);

        assertEquals(MediaPacket.Type.COMMAND, decoded.type);
        assertEquals(CommandPacket.CmdType.MICROPHONE_AUDIO.getFlag(), decoded.cmdType);
        assertArrayEquals(pcm, decoded.data);
    }

    @Test
    public void microphoneCommandsHaveStableWireValues() {
        assertEquals(2, CommandPacket.CmdType.MICROPHONE_AUDIO.getFlag());
        assertEquals(3, CommandPacket.CmdType.MICROPHONE_END.getFlag());
        assertEquals(4, CommandPacket.CmdType.MICROPHONE_STATE.getFlag());
    }
}
