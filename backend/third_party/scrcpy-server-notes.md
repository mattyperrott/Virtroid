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
paths. Audio and video packets share one synchronized multiplexed stream before
the encrypted viewer proxy, preventing concurrent packet writes from corrupting
the transport.

CI builds the vendored module and compares the complete unsigned APK
byte-for-byte with the tracked payload. This is source/build evidence; an
Android build alone is not proof that
the ReDroid audio capture API works on a deployed image. Runtime proof still
requires a live session with decoded audio on the client.
