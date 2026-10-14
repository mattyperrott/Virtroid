# Embedded scrcpy server provenance

The source in this directory is vendored from
[`zwc456baby/ScrcpyForAndroid`](https://github.com/zwc456baby/ScrcpyForAndroid)
at commit `a7927a00e5ab860e52974153d343c95a0951e55b` (2026-07-29).

The upstream `server/` directory carries the Apache License 2.0 in `LICENSE`.
The upstream repository also carries GPL-3.0 at its root; that file is retained
unchanged as `UPSTREAM_ROOT_LICENSE` so both upstream notices travel with the
vendored source.

Virtroid changes are deliberately small and reviewable:

- retain ReDroid display and power-service fallbacks;
- bind the plaintext scrcpy-to-viewercrypt hop to guest loopback;
- serialize audio/video writes to the multiplexed viewer stream;
- carry demand-gated physical-microphone PCM over the existing encrypted viewer
  stream and inject it into matching runtime recorders with an Android audio policy;
- validate all five server arguments;
- when run as Android's system user, capture through a secure mirror display so
  windows that apps mark as secure (FLAG_SECURE) are streamed instead of black,
  and identify as the `android` package for permission checks;
- keep the viewer socket's send buffer small, ask the encoder for realtime
  priority, and lower the video bitrate while the viewer's connection falls
  behind, raising it again once it keeps up; and
- build the tracked server asset from this source in CI.

From `android-client/`, build with:

```sh
./gradlew --no-daemon :scrcpy-server:syncEmbeddedAsset
```

Use the tracked Gradle 9.3.1 wrapper, Android Gradle Plugin 9.1.1, and JDK 17,
matching the pinned CI build environment. D8 output can differ when the same
sources are compiled under a different JDK major version.

The resulting APK is a zip-compatible app-process payload. The sync task copies
it to `../backend/cmd/virtnoded/assets/scrcpy-server.jar`; then run the backend
and Android test suites. The tracked asset's pinned SHA-256 digest is
`7e2f0f8b047812d221532c91652ee06c59366660f704d47257c721502a23620d`.
CI independently rebuilds the module and checks every decompressed ZIP member
byte against the tracked asset. This ignores container-level APK ordering and
compression metadata, which Android build tools may vary between platforms,
without weakening the source-to-payload comparison.
