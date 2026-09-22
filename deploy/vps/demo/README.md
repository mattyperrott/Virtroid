# Interactive demo runtime

The public `/demo/` page embeds a real signed Virtroid APK from one dedicated
ReDroid handset. `ws-scrcpy-web` is only a private pixel/input gateway; its
administration, shell, file, and ADB surfaces are not routed publicly.

Production invariants:

- the dedicated handset runs the tested Android 12 image
  `redroid/redroid@sha256:a6c464bbedcf1dcb67dbf91f329fbb19bee5b50631f0ca6bda6ed7c41b0e64e2`;
- the handset and gateway images are addressed by immutable SHA-256 digests;
- ADB is bound to VPS loopback only;
- the gateway is reachable by `virtroidd` on the private control network;
- `/demo/device/` allowlists the embed assets and stream WebSocket only;
- one expiring HttpOnly Virtroid session owns the handset at a time;
- the signed APK checksum is verified before installation.

The production APK keeps screen-capture protection enabled by default. The
disposable demo handset turns off the same user-facing preference only in its
local app data so that the private browser stream can capture the display. This
does not change the release APK or the default used on normal client devices.

The live host currently uses `virtroid-demo-handset`,
`virtroid-demo-gateway`, `virtroid-demo-handset-data`, and
`virtroid-demo-gateway-data`. The gateway reads the reviewed
`ws-scrcpy-config.json`, which permits only the production hostname.
