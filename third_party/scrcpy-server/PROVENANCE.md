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
- validate all five server arguments; and
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
`acb3499b6484568f0c8dc9f1a3a3f70fbecb67551f041a1f4149598e03b2c11c`.
CI independently rebuilds the module and checks every decompressed ZIP member
byte against the tracked asset. This ignores container-level APK ordering and
compression metadata, which Android build tools may vary between platforms,
without weakening the source-to-payload comparison.
