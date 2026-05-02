# Keep the embedded scrcpy service/API stable; it is invoked by Android service binding
# and by the remote display bridge rather than through app-facing Kotlin symbols only.
-keep class org.client.scrcpy.** { *; }
