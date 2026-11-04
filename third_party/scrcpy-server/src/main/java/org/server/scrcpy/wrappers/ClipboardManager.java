package org.server.scrcpy.wrappers;

import android.annotation.SuppressLint;
import android.content.ClipData;
import android.content.ClipDescription;
import android.content.IOnPrimaryClipChangedListener;
import android.os.Build;
import android.os.IInterface;
import android.os.PersistableBundle;
import android.text.TextUtils;

import org.server.scrcpy.ClipboardBridge;
import org.server.scrcpy.Ln;
import org.server.scrcpy.util.FakeContext;

import java.lang.reflect.Method;

@SuppressLint("PrivateApi,DiscouragedPrivateApi")
public final class ClipboardManager implements ClipboardBridge.Clipboard {

    /** Marks a clip this wrapper set, so its own change is not sent back. */
    private static final String VIEWER_CLIP_LABEL = "Virtroid viewer";
    private static final String EXTRA_IS_SENSITIVE = "android.content.extra.IS_SENSITIVE";
    private static final int USER_SYSTEM = 0;
    private static final int DEVICE_ID_DEFAULT = 0;

    // What IClipboard's methods take after the clip or listener, newest first:
    // package, attribution tag, user and device; then without the device
    // (Android 11-13); then package and user (Android 10); then only package.
    private static final Class<?>[][] CALLER_PARAMETERS = {
            {String.class, String.class, int.class, int.class},
            {String.class, String.class, int.class},
            {String.class, int.class},
            {String.class},
    };

    private final IInterface manager;
    private final Method getPrimaryClip;
    private final Method setPrimaryClip;
    private final Method addListener;

    private ClipboardManager(IInterface manager) throws NoSuchMethodException {
        this.manager = manager;
        getPrimaryClip = find("getPrimaryClip");
        setPrimaryClip = find("setPrimaryClip", ClipData.class);
        addListener = find("addPrimaryClipChangedListener", IOnPrimaryClipChangedListener.class);
    }

    /** The clipboard service, or null where this Android has none this can call. */
    static ClipboardManager create() {
        try {
            IInterface manager = ServiceManager.getService("clipboard", "android.content.IClipboard");
            return manager == null ? null : new ClipboardManager(manager);
        } catch (AssertionError | NoSuchMethodException e) {
            Ln.e("Could not reach the clipboard service", e);
            return null;
        }
    }

    private Method find(String name, Class<?>... leading) throws NoSuchMethodException {
        for (Class<?>[] caller : CALLER_PARAMETERS) {
            Class<?>[] parameters = new Class<?>[leading.length + caller.length];
            System.arraycopy(leading, 0, parameters, 0, leading.length);
            System.arraycopy(caller, 0, parameters, leading.length, caller.length);
            try {
                return manager.getClass().getMethod(name, parameters);
            } catch (NoSuchMethodException e) {
                // An older Android: try the next.
            }
        }
        throw new NoSuchMethodException(name);
    }

    /** Arguments for one of {@link #CALLER_PARAMETERS}, by its length. */
    private static Object[] callerArguments(int count) {
        switch (count) {
            case 4:
                return new Object[]{FakeContext.PACKAGE_NAME, null, USER_SYSTEM, DEVICE_ID_DEFAULT};
            case 3:
                return new Object[]{FakeContext.PACKAGE_NAME, null, USER_SYSTEM};
            case 2:
                return new Object[]{FakeContext.PACKAGE_NAME, USER_SYSTEM};
            default:
                return new Object[]{FakeContext.PACKAGE_NAME};
        }
    }

    private Object call(Method method, Object... leading) throws ReflectiveOperationException {
        Object[] args = new Object[method.getParameterTypes().length];
        Object[] caller = callerArguments(args.length - leading.length);
        System.arraycopy(leading, 0, args, 0, leading.length);
        System.arraycopy(caller, 0, args, leading.length, caller.length);
        return method.invoke(manager, args);
    }

    @Override
    public String getCopiedText() {
        try {
            ClipData clip = (ClipData) call(getPrimaryClip);
            if (clip == null || clip.getItemCount() == 0) {
                return null;
            }
            ClipDescription description = clip.getDescription();
            if (description != null && TextUtils.equals(VIEWER_CLIP_LABEL, description.getLabel())) {
                return null;
            }
            // Text only: coercing would open a URI item's content.
            CharSequence text = clip.getItemAt(0).getText();
            return text == null ? null : text.toString();
        } catch (ReflectiveOperationException e) {
            Ln.e("Could not read the clipboard", e);
            return null;
        }
    }

    @Override
    public boolean setText(String text) {
        ClipData clip = ClipData.newPlainText(VIEWER_CLIP_LABEL, text);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            PersistableBundle extras = new PersistableBundle();
            // The phone's clip may be a secret: keep it out of previews here too.
            extras.putBoolean(EXTRA_IS_SENSITIVE, true);
            clip.getDescription().setExtras(extras);
        }
        try {
            call(setPrimaryClip, clip);
            return true;
        } catch (ReflectiveOperationException e) {
            Ln.e("Could not set the clipboard", e);
            return false;
        }
    }

    @Override
    public boolean addChangeListener(Runnable listener) {
        try {
            call(addListener, new IOnPrimaryClipChangedListener.Stub() {
                @Override
                public void dispatchPrimaryClipChanged() {
                    listener.run();
                }
            });
            return true;
        } catch (ReflectiveOperationException e) {
            Ln.e("Could not watch the clipboard", e);
            return false;
        }
    }
}
