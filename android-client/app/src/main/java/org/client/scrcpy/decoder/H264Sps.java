package org.client.scrcpy.decoder;

import java.nio.ByteBuffer;

/**
 * Reads the picture size from an H.264 sequence parameter set, so the viewer
 * knows the stream's real shape when the runtime's display rotates.
 */
public final class H264Sps {
    private H264Sps() {
    }

    /**
     * Returns {width, height} in pixels after cropping, or null when the SPS
     * cannot be read. The buffer may start with an Annex B start code.
     */
    public static int[] pictureSize(ByteBuffer sps) {
        if (sps == null) {
            return null;
        }
        ByteBuffer view = sps.duplicate();
        byte[] nal = new byte[view.remaining()];
        view.get(nal);
        try {
            return pictureSize(nal);
        } catch (IndexOutOfBoundsException | IllegalArgumentException e) {
            return null;
        }
    }

    static int[] pictureSize(byte[] nal) {
        int start = 0;
        while (start + 2 < nal.length && nal[start] == 0 && nal[start + 1] == 0) {
            if (nal[start + 2] == 1) {
                start += 3;
                break;
            }
            start++;
        }
        if (start >= nal.length || (nal[start] & 0x1f) != 7) {
            return null;
        }
        Bits bits = new Bits(unescape(nal, start + 1));
        int profileIdc = bits.read(8);
        bits.read(16); // constraint flags, level_idc
        bits.ue(); // seq_parameter_set_id
        int chromaFormatIdc = 1;
        boolean separateColourPlanes = false;
        if (profileIdc == 100 || profileIdc == 110 || profileIdc == 122 || profileIdc == 244
                || profileIdc == 44 || profileIdc == 83 || profileIdc == 86 || profileIdc == 118
                || profileIdc == 128 || profileIdc == 138 || profileIdc == 139 || profileIdc == 134
                || profileIdc == 135) {
            chromaFormatIdc = bits.ue();
            if (chromaFormatIdc == 3) {
                separateColourPlanes = bits.read(1) == 1;
            }
            bits.ue(); // bit_depth_luma_minus8
            bits.ue(); // bit_depth_chroma_minus8
            bits.read(1); // qpprime_y_zero_transform_bypass_flag
            if (bits.read(1) == 1) { // seq_scaling_matrix_present_flag
                int lists = chromaFormatIdc == 3 ? 12 : 8;
                for (int i = 0; i < lists; i++) {
                    if (bits.read(1) == 1) {
                        skipScalingList(bits, i < 6 ? 16 : 64);
                    }
                }
            }
        }
        bits.ue(); // log2_max_frame_num_minus4
        int picOrderCntType = bits.ue();
        if (picOrderCntType == 0) {
            bits.ue(); // log2_max_pic_order_cnt_lsb_minus4
        } else if (picOrderCntType == 1) {
            bits.read(1); // delta_pic_order_always_zero_flag
            bits.se(); // offset_for_non_ref_pic
            bits.se(); // offset_for_top_to_bottom_field
            int cycle = bits.ue();
            for (int i = 0; i < cycle; i++) {
                bits.se();
            }
        }
        bits.ue(); // max_num_ref_frames
        bits.read(1); // gaps_in_frame_num_value_allowed_flag
        int widthInMbs = bits.ue() + 1;
        int heightInMapUnits = bits.ue() + 1;
        int frameMbsOnly = bits.read(1);
        if (frameMbsOnly == 0) {
            bits.read(1); // mb_adaptive_frame_field_flag
        }
        bits.read(1); // direct_8x8_inference_flag
        int width = widthInMbs * 16;
        int height = (2 - frameMbsOnly) * heightInMapUnits * 16;
        if (bits.read(1) == 1) { // frame_cropping_flag
            int left = bits.ue();
            int right = bits.ue();
            int top = bits.ue();
            int bottom = bits.ue();
            int cropUnitX = 1;
            int cropUnitY = 2 - frameMbsOnly;
            int arrayType = separateColourPlanes ? 0 : chromaFormatIdc;
            if (arrayType != 0) {
                cropUnitX = arrayType == 3 ? 1 : 2;
                cropUnitY *= arrayType == 1 ? 2 : 1;
            }
            width -= cropUnitX * (left + right);
            height -= cropUnitY * (top + bottom);
        }
        if (width <= 0 || height <= 0) {
            return null;
        }
        return new int[]{width, height};
    }

    private static void skipScalingList(Bits bits, int size) {
        int last = 8;
        int next = 8;
        for (int j = 0; j < size; j++) {
            if (next != 0) {
                next = (last + bits.se() + 256) % 256;
            }
            last = next == 0 ? last : next;
        }
    }

    /** Removes emulation prevention bytes (00 00 03 -> 00 00). */
    private static byte[] unescape(byte[] nal, int from) {
        byte[] out = new byte[nal.length - from];
        int n = 0;
        int zeros = 0;
        for (int i = from; i < nal.length; i++) {
            int b = nal[i] & 0xff;
            if (zeros >= 2 && b == 3) {
                zeros = 0;
                continue;
            }
            zeros = b == 0 ? zeros + 1 : 0;
            out[n++] = (byte) b;
        }
        byte[] trimmed = new byte[n];
        System.arraycopy(out, 0, trimmed, 0, n);
        return trimmed;
    }

    private static final class Bits {
        private final byte[] data;
        private int position;

        Bits(byte[] data) {
            this.data = data;
        }

        int read(int count) {
            int value = 0;
            for (int i = 0; i < count; i++) {
                int bit = (data[position >> 3] >> (7 - (position & 7))) & 1;
                position++;
                value = (value << 1) | bit;
            }
            return value;
        }

        int ue() {
            int zeros = 0;
            while (read(1) == 0) {
                if (++zeros > 31) {
                    throw new IllegalArgumentException("bad exp-Golomb code");
                }
            }
            return zeros == 0 ? 0 : (1 << zeros) - 1 + read(zeros);
        }

        int se() {
            int value = ue();
            return (value & 1) == 1 ? (value + 1) / 2 : -(value / 2);
        }
    }
}
