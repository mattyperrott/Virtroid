package org.client.scrcpy.model;

import static org.junit.Assert.assertArrayEquals;
import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertNull;

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
    public void microphoneCommandsMatchRuntimeWireValues() {
        assertEquals(2, CommandPacket.CmdType.MICROPHONE_AUDIO.getFlag());
        assertEquals(3, CommandPacket.CmdType.MICROPHONE_END.getFlag());
        assertEquals(4, CommandPacket.CmdType.MICROPHONE_STATE.getFlag());
    }

    @Test
    public void clipboardCommandsHaveStableWireValues() {
        assertEquals(5, CommandPacket.CmdType.CLIPBOARD_READY.getFlag());
        assertEquals(6, CommandPacket.CmdType.CLIPBOARD_TEXT.getFlag());
    }

    @Test
    public void clipboardTextRoundTrips() {
        byte[] text = ClipboardText.encode("0xAbC é ✓ 🔑");
        byte[] framed = CommandPacket.toArray(
                MediaPacket.Type.COMMAND,
                CommandPacket.CmdType.CLIPBOARD_TEXT,
                text
        );
        byte[] body = new byte[framed.length - 4];
        System.arraycopy(framed, 4, body, 0, body.length);

        CommandPacket decoded = new CommandPacket().fromArray(body);

        assertEquals(CommandPacket.CmdType.CLIPBOARD_TEXT.getFlag(), decoded.cmdType);
        assertEquals("0xAbC é ✓ 🔑", ClipboardText.decode(decoded.data));
    }

    // What lets an older peer keep streaming: it reads a type it doesn't know as
    // null, and both ends skip a command of a null type.
    @Test
    public void anUnknownCommandReadsAsNoType() {
        assertNull(CommandPacket.CmdType.getFlag((byte) 7));
    }
}
