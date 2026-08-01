package org.server.scrcpy.wrappers;

import android.annotation.SuppressLint;
import android.os.Build;
import android.os.IInterface;


import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
public final class PowerManager {
    private final IInterface manager;
    private final Method isScreenOnMethod;

    public PowerManager(IInterface manager) {
        this.manager = manager;
        if (manager == null) {
            isScreenOnMethod = null;
            return;
        }
        try {
            @SuppressLint("ObsoleteSdkInt") // we may lower minSdkVersion in the future
                    String methodName = Build.VERSION.SDK_INT >= Build.VERSION_CODES.KITKAT_WATCH ? "isInteractive" : "isScreenOn";
            isScreenOnMethod = manager.getClass().getMethod(methodName);
        } catch (NoSuchMethodException e) {
            throw new AssertionError(e);
        }
    }

    public boolean isScreenOn() {
        if (manager == null || isScreenOnMethod == null) {
            return true;
        }
        try {
            return (Boolean) isScreenOnMethod.invoke(manager);
        } catch (InvocationTargetException | IllegalAccessException e) {
            // throw new AssertionError(e);
            return false;
        }
    }
}
