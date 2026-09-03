# NotificationListenerService and manifest-declared components are retained by
# Android's manifest-aware shrinker. Keep runtime-agent data classes readable
# for conservative release builds and incident diagnostics.
-keep class io.virtroid.runtimeagent.RuntimeNotificationEvent { *; }
