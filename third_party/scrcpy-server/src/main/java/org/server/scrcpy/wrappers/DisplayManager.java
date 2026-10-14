package org.server.scrcpy.wrappers;

import android.content.Context;
import android.hardware.display.VirtualDisplay;
import android.os.Handler;
import android.os.IInterface;
import android.view.Display;
import android.view.Surface;

import org.server.scrcpy.DisplayInfo;
import org.server.scrcpy.Ln;
import org.server.scrcpy.Size;
import org.server.scrcpy.util.Command;
import org.server.scrcpy.util.FakeContext;

import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
public final class DisplayManager {
    private final IInterface manager;

    private Method createVirtualDisplayMethod;

    public DisplayManager(IInterface manager) {
        this.manager = manager;
    }

    private static DisplayInfo getDisplayInfoFromDumpsysDisplay(int displayId) {
        try {
            String dumpsysDisplayOutput = Command.execReadOutput("dumpsys", "display");
            return parseDisplayInfo(dumpsysDisplayOutput, displayId);
        } catch (Exception e) {
            Ln.e("Could not get display info from \"dumpsys display\" output", e);
            return null;
        }
    }

    public static DisplayInfo parseDisplayInfo(String dumpsysDisplayOutput, int displayId) {
        Pattern regex = Pattern.compile(
                "^    mOverrideDisplayInfo=DisplayInfo\\{\".*?, displayId " + displayId + ".*?(, FLAG_.*)?, real ([0-9]+) x ([0-9]+).*?, "
                        + "rotation ([0-9]+).*?, layerStack ([0-9]+)",
                Pattern.MULTILINE);
        Matcher m = regex.matcher(dumpsysDisplayOutput);
        if (!m.find()) {
            return null;
        }
        int flags = parseDisplayFlags(m.group(1));
        int width = Integer.parseInt(m.group(2));
        int height = Integer.parseInt(m.group(3));
        int rotation = Integer.parseInt(m.group(4));
        int layerStack = Integer.parseInt(m.group(5));

        return new DisplayInfo(displayId, new Size(width, height), rotation, layerStack, flags);
    }

    private static int parseDisplayFlags(String text) {
        Pattern regex = Pattern.compile("FLAG_[A-Z_]+");
        if (text == null) {
            return 0;
        }

        int flags = 0;
        Matcher m = regex.matcher(text);
        while (m.find()) {
            String flagString = m.group();
            try {
                Field filed = Display.class.getDeclaredField(flagString);
                flags |= filed.getInt(null);
            } catch (ReflectiveOperationException e) {
                // Silently ignore, some flags reported by "dumpsys display" are @TestApi
            }
        }
        return flags;
    }


    public DisplayInfo getDisplayInfo() {
        if (manager == null) {
            return getDisplayInfoFromDumpsysDisplay(0);
        }
        try {
            Object displayInfo = manager.getClass().getMethod("getDisplayInfo", int.class).invoke(manager, 0);
            if (displayInfo == null) {
                return getDisplayInfoFromDumpsysDisplay(0);
            }
            Class<?> cls = displayInfo.getClass();
            // width and height already take the rotation into account
            int width = cls.getDeclaredField("logicalWidth").getInt(displayInfo);
            int height = cls.getDeclaredField("logicalHeight").getInt(displayInfo);
            int rotation = cls.getDeclaredField("rotation").getInt(displayInfo);
            return new DisplayInfo(new Size(width, height), rotation);
        } catch (Exception e) {
            throw new AssertionError(e);
        }
    }

    public DisplayInfo getDisplayInfo(int displayId) {
        if (manager == null) {
            return getDisplayInfoFromDumpsysDisplay(displayId);
        }
        try {
            Object displayInfo = manager.getClass().getMethod("getDisplayInfo", int.class).invoke(manager, displayId);
            if (displayInfo == null) {
                // fallback when displayInfo is null
                return getDisplayInfoFromDumpsysDisplay(displayId);
            }
            Class<?> cls = displayInfo.getClass();
            // width and height already take the rotation into account
            int width = cls.getDeclaredField("logicalWidth").getInt(displayInfo);
            int height = cls.getDeclaredField("logicalHeight").getInt(displayInfo);
            int rotation = cls.getDeclaredField("rotation").getInt(displayInfo);
            int layerStack = cls.getDeclaredField("layerStack").getInt(displayInfo);
            int flags = cls.getDeclaredField("flags").getInt(displayInfo);
            return new DisplayInfo(displayId, new Size(width, height), rotation, layerStack, flags);
        } catch (ReflectiveOperationException e) {
            throw new AssertionError(e);
        }
    }
    public int[] getDisplayIds() {
        try {
            return (int[]) manager.getClass().getMethod("getDisplayIds").invoke(manager);
        } catch (ReflectiveOperationException e) {
            throw new AssertionError(e);
        }
    }

    private Method getCreateVirtualDisplayMethod() throws NoSuchMethodException {
        if (createVirtualDisplayMethod == null) {
            createVirtualDisplayMethod = android.hardware.display.DisplayManager.class
                    .getMethod("createVirtualDisplay", String.class, int.class, int.class, int.class, Surface.class);
        }
        return createVirtualDisplayMethod;
    }

    public VirtualDisplay createVirtualDisplay(String name, int width, int height, int displayIdToMirror, Surface surface) throws Exception {
        Method method = getCreateVirtualDisplayMethod();
        return (VirtualDisplay) method.invoke(null, name, width, height, displayIdToMirror, surface);
    }

    private static final int VIRTUAL_DISPLAY_FLAG_SECURE = 1 << 2;
    private static final int VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR = 1 << 4;

    /**
     * A mirror of displayIdToMirror that also shows windows apps mark as
     * secure (FLAG_SECURE). Android allows it only to callers holding
     * CAPTURE_SECURE_VIDEO_OUTPUT, which the system user does.
     */
    public static VirtualDisplay createSecureMirror(String name, int width, int height, int displayIdToMirror, Surface surface)
            throws Exception {
        Class<?> builderClass = Class.forName("android.hardware.display.VirtualDisplayConfig$Builder");
        Object builder = builderClass.getConstructor(String.class, int.class, int.class, int.class)
                .newInstance(name, width, height, 1);
        builderClass.getMethod("setFlags", int.class).invoke(builder, VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR | VIRTUAL_DISPLAY_FLAG_SECURE);
        builderClass.getMethod("setSurface", Surface.class).invoke(builder, surface);
        builderClass.getMethod("setDisplayIdToMirror", int.class).invoke(builder, displayIdToMirror);
        Object config = builderClass.getMethod("build").invoke(builder);
        android.hardware.display.DisplayManager displayManager =
                (android.hardware.display.DisplayManager) FakeContext.get().getSystemService(Context.DISPLAY_SERVICE);
        Class<?> configClass = Class.forName("android.hardware.display.VirtualDisplayConfig");
        VirtualDisplay display = (VirtualDisplay) android.hardware.display.DisplayManager.class
                .getMethod("createVirtualDisplay", configClass, Handler.class, VirtualDisplay.Callback.class)
                .invoke(displayManager, config, null, null);
        if (display == null) {
            throw new IllegalStateException("secure virtual display was not created");
        }
        return display;
    }
}
