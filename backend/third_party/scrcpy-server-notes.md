Patched upstream source used to rebuild `backend/cmd/virtnoded/assets/scrcpy-server.jar`.

Base source:
- `https://github.com/zwc456baby/ScrcpyForAndroid`

Local patch applied on 2026-04-14:
- `org.server.scrcpy.wrappers.DisplayManager#getDisplayInfo()` now falls back when `IDisplayManager.getDisplayInfo(0)` returns `null`.
- `org.server.scrcpy.wrappers.DisplayManager#getDisplayInfo()` and `getDisplayInfo(int)` now also fall back when the wrapped `IDisplayManager` binder itself is `null`.
- `org.server.scrcpy.Device#computeScreenInfo()` now falls back to `ro.boot.redroid_width` / `ro.boot.redroid_height` when both display-service paths fail.
- Reason: when the server is launched from the in-guest init service, ReDroid on this VPS can return `null` for display 0 even though `dumpsys display` has valid display data. Without the fallback, viewer prepare fails with:
  - `java.lang.AssertionError: java.lang.NullPointerException`
  - `org.server.scrcpy.wrappers.DisplayManager.getDisplayInfo(DisplayManager.java:85)`

Minimal source diff:

```diff
 public DisplayInfo getDisplayInfo() {
     try {
         Object displayInfo = manager.getClass().getMethod("getDisplayInfo", int.class).invoke(manager, 0);
+        if (displayInfo == null) {
+            return getDisplayInfoFromDumpsysDisplay(0);
+        }
         Class<?> cls = displayInfo.getClass();
         int width = cls.getDeclaredField("logicalWidth").getInt(displayInfo);
         int height = cls.getDeclaredField("logicalHeight").getInt(displayInfo);
         int rotation = cls.getDeclaredField("rotation").getInt(displayInfo);
         return new DisplayInfo(new Size(width, height), rotation);
     } catch (Exception e) {
         throw new AssertionError(e);
     }
 }
```
