# Interactive demo runtime

The public `/demo/` page embeds a real signed Virtroid APK from one dedicated
ReDroid handset. `ws-scrcpy-web` is only a private pixel/input gateway; its
administration, shell, file, and ADB surfaces are not routed publicly.

Production invariants:

- the handset and gateway images are addressed by immutable SHA-256 digests;
- ADB is bound to VPS loopback only;
- the gateway is reachable by `virtroidd` on the private control network;
- `/demo/device/` allowlists the embed assets and stream WebSocket only;
- one expiring HttpOnly Virtroid session owns the handset at a time;
- the signed APK checksum is verified before installation.

The live host currently uses `virtroid-demo-handset`,
`virtroid-demo-gateway`, `virtroid-demo-handset-data`, and
`virtroid-demo-gateway-data`. The gateway reads the reviewed
`ws-scrcpy-config.json`, which permits only the production hostname.
