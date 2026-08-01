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
Android test suites. CI independently rebuilds the module and checks that the
tracked asset is byte-for-byte identical to the reproducible unsigned APK.
