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
- validate all five server arguments; and
- build the tracked server asset from this source in CI.

From `android-client/`, build with:

```sh
./gradlew --no-daemon :scrcpy-server:assembleRelease
```

The resulting APK is a zip-compatible app-process payload. Copy
`../third_party/scrcpy-server/build/outputs/apk/release/scrcpy-server-release-unsigned.apk`
to `../backend/cmd/virtnoded/assets/scrcpy-server.jar`, then run the backend and
Android test suites. The tracked asset's pinned SHA-256 digest is
`4cc8c509fd73f6594e89419c0c29347e2c20b5528da12f087cb9fa9f003b56a7`.
CI independently rebuilds the module and checks every decompressed ZIP member
byte against the tracked asset. This ignores container-level APK ordering and
compression metadata, which Android build tools may vary between platforms,
without weakening the source-to-payload comparison.
