# Embedded scrcpy server

`backend/cmd/virtnoded/assets/scrcpy-server.jar` is the app-process payload
built from the reviewable source at `third_party/scrcpy-server/`.

The exact upstream commit, upstream licenses, Virtroid patches, and rebuild
command are recorded in `third_party/scrcpy-server/PROVENANCE.md`. The embedded
server accepts five arguments:

```text
ip max_size bit_rate tunnel_forward audio_enabled
```

The node passes the runtime's `audio_enabled` value through both viewer launch
paths. Audio, video, control, clipboard, and demand-gated physical-microphone packets share
one synchronized multiplexed stream before the encrypted viewer proxy,
preventing concurrent packet writes from corrupting the transport. The server
uses its existing Android shell identity to register the runtime microphone
injection policy; no separately installed microphone package or public listener
port is required.

The server touches the clipboard only once a client asks with `CLIPBOARD_READY`,
and answers only if it can, so an older client or server on the other end sees no
change. Without `deploy/aosp/patches/frameworks/base` 0003 in the runtime image,
the system user may set the clipboard but not read it: clips still reach the
runtime and no longer come back.

CI builds the vendored module and compares the complete unsigned APK
byte-for-byte with the tracked payload. This is source/build evidence; an
Android build alone is not proof that
the ReDroid audio paths work on a deployed image. Runtime proof still requires
a live session with decoded runtime output and successful microphone recording
on the deployed ReDroid image.
