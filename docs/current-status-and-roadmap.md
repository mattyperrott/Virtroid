# Virtroid Current Status and Roadmap

Authoritative status date: 2026-07-25

This report replaces the historical 2026-05 and 2026-07-19 status snapshots.
It separates live verification, repository verification, target architecture,
and unverified work. Historical audits remain useful as provenance only.

## Executive status

Virtroid is currently a working, single-VPS remote Android development system:

- the public control plane and relay are on `virtroid-cp`
  (`185.223.207.157`), behind HTTPS on `virtroid.network`;
- the Android client supports signed onboarding, runtime lifecycle operations,
  encrypted viewer sessions, F-Droid catalog selection, local app lock, and
  encrypted local client state;
- the VPS currently hosts the control plane, node agent, PostgreSQL, HAProxy,
  ReDroid containers, and encrypted local-disk runtime snapshots;
- renterd/Sia is inactive and intentionally deferred;
- confidential VM runtimes, attestation-bound key release, true end-to-end
  encrypted persistence, and camera passthrough are not implemented.

This is a trusted-operator system, not a trustless or confidential service. The
runtime node controls the live Android plaintext and receives material needed to
restore/save encrypted runtime state. A compromised VPS or privileged operator
can compromise an active runtime.

## Live VPS verification

Verified read-only on 2026-07-25 at approximately 11:55 UTC:

| Check | Verified result |
| --- | --- |
| Host | `virtroid-cp`, uptime 47 minutes during the check |
| Public DNS | `virtroid.network` resolves to `185.223.207.157` |
| Public health | `https://virtroid.network/healthz` returned `{"ok":true}` |
| Public listener | HAProxy exposes TCP 443 |
| Private services | `virtroidd` is host-loopback on 8080; `virtnoded` is host-loopback on 8090 |
| Containers | edge, PostgreSQL, control plane, and node agent running; PostgreSQL healthy |
| Host hardening services | Docker, fail2ban, and auditd active |
| Disk | 180 GiB total, 18 GiB used, 162 GiB available (10% used) |
| Deployed source | `ee7ee673c7f7916a53cd07a7728649adf3eddf88` |
| Deployed schema | `2026072102` |
| Release image | protected local image and immutable release state present |

The deployed VPS does not yet contain local commit `df0ae36` (the
`golang.org/x/text` security update) or the Android log fixes in the current
working tree.

### Live database and runtime inventory

Aggregate counts only; no user identifiers were exported:

| Item | Count |
| --- | ---: |
| Active accounts | 2 |
| Active devices | 2 |
| Active runtime rows | 2 |
| Deleted runtime rows retained for lifecycle history | 1 |
| Runtimes requesting `running` | 0 |
| Runtime cleanup obligations pending | 0 |
| Live sessions | 0 |
| Unexpired blob-key handoffs | 0 |
| Runtime containers | 0 |
| Approved operators / nodes | 1 / 1 |
| Orphan devices, runtimes, sessions, or runtime logs by foreign-key audit | 0 |
| Local snapshot files | 0 |

One runtime data directory exists on disk. A future orphan audit should compare
every runtime directory, blob namespace, Docker network, and database row by
expected identifier after each fault-injection case.

## Verified repository state

### Backend and deployment

Verified locally on 2026-07-25:

- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `go vet ./...`
- `go build ./cmd/...`
- `govulncheck ./...` reported no vulnerabilities after upgrading
  `golang.org/x/text` to `v0.39.0`;
- deployment shell syntax, environment-safety tests, and Compose configuration
  tests pass.

Previously established controls still present in current code include:

- signed device requests with bounded timestamps, nonces, body hashes, and
  replay rejection;
- runtime-scoped capability signing;
- signed node and callback traffic;
- approved operator/node/key registry;
- allowlisted Android runtime images;
- hashed relay-token handling, expiry, close, and heartbeat flows;
- monotonic authenticated snapshot generations and rollback checks;
- runtime, start-rate, trial-time, storage-quota, and free-disk enforcement;
- local-disk snapshot encryption and authenticated manifests;
- explicit cleanup obligations for stop, wipe, delete, and account erasure;
- reproducible VPS hardening and release scripts under `deploy/vps`.

### Android client

The complete local Android gate passed on 2026-07-25:

- debug unit tests;
- debug and release lint;
- debug APK assembly;
- release security-manifest verification.

The current debug APK was installed over the emulator and the real log viewer
was exercised:

- All logs is the initial filter;
- info, warning, error, and critical rows are visible under All;
- Clear removes the persisted log store, not only the current projection;
- re-entering the screen does not resurrect cleared rows;
- restarting the process restores no cleared history; only the new `App startup`
  event appears;
- the unread error/critical count becomes zero after clear in regression tests.

The public F-Droid catalog was also checked live. It returned 250 entries; every
returned entry was marked `fdroid` and had a 64-character APK SHA-256 value.

## Capability matrix

| Capability | Status | Current truth |
| --- | --- | --- |
| Public signed account bootstrap | Implemented | Open creation is intentional; device proof-of-possession protects subsequent API use |
| Trusted-device list and revoke | Implemented | Android and backend flows exist |
| Runtime create/start/connect/stop | Implemented | Proven on the current single-VPS model |
| Persona restart and factory reset | Implemented, needs more fault testing | Cleanup semantics exist; complete orphan evidence remains a reliability task |
| Runtime/account deletion cleanup | Implemented, needs repeated failure tests | Current live relational orphan checks are zero |
| Viewer encryption and TLS relay | Implemented | Protects transport; does not make the runtime node confidential |
| Local app lock and secure vault | Implemented | Keystore-bound local state, retry controls, biometric support, secure windows |
| F-Droid catalog | Implemented | 250 live pinned entries at the current check |
| F-Droid install into runtime | Implemented happy path | Needs repeated package/version/signature failure testing |
| Local-disk encrypted snapshots | Implemented | Current production storage path |
| Snapshot rollback/generation checks | Implemented | More live fork/corruption/failure testing remains |
| Sia/renterd production storage | Deferred | Inactive by explicit decision |
| End-to-end encrypted persistence | Not implemented | Runtime node can access active plaintext/key material |
| Confidential runtime VM | Not implemented | Current ReDroid containers are privileged on the VPS |
| Attestation and lease-scoped key release | Not implemented | Design/POC documents only |
| File import into an active runtime | Not implemented | Viewer action remains hidden |
| Live camera passthrough | Not implemented | See dedicated milestone below |
| Central operator control-panel UI | Not implemented | Current “control plane” is API/service infrastructure |

## Camera passthrough: exact gap

The database has a `camera_mode` field and the Android layouts contain disabled
camera controls, but that is only dormant model/UI scaffolding. The backend
rejects every creation profile where `camera_mode != "disabled"`.

True passthrough does not exist because the system has none of the required
media path:

1. no Android-client camera permission or CameraX capture pipeline;
2. no session-bound upstream video channel from the physical client;
3. no node-side decoder/frame conversion and backpressure policy;
4. no per-runtime V4L2 loopback device;
5. no working V4L2 camera HAL in the pinned ReDroid guest image;
6. no lifecycle cleanup for camera devices and media buffers;
7. no camera privacy indicator, explicit enable/disable state, or failure UI.

scrcpy camera mirroring is the opposite direction: it captures the Android
device camera and sends it to its client. It does not inject the physical
client camera into a remote Android guest. ReDroid's upstream V4L2 camera path
has historically been experimental and requires validation against the exact
pinned image before any product switch is enabled:

- <https://github.com/Genymobile/scrcpy>
- <https://github.com/remote-android/redroid-doc/issues/14>

### Camera passthrough milestone

Do this only on a disposable runtime/node until it is proven:

1. Build a host feasibility rig with one dedicated `v4l2loopback` device.
2. Pin a ReDroid image that contains and successfully boots a V4L2 camera HAL.
3. Prove Camera2 enumeration, preview, still capture, rotation, reconnect, and
   container deletion without exposing another runtime's device.
4. Add a session-scoped encrypted camera channel. Bind authorization to the
   active runtime capability, device, session, expiry, and replay controls.
5. Capture with CameraX into bounded memory buffers; do not write frames to the
   gallery or long-lived client storage.
6. Convert only explicitly supported formats/resolutions, enforce bandwidth and
   backpressure limits, and stop immediately on background, disconnect, expiry,
   runtime stop, or user action.
7. Create/remove a dedicated video device per runtime and audit all device,
   process, socket, and buffer cleanup.
8. Enable `camera_mode=passthrough` and reveal the UI only after the complete
   end-to-end test passes.

This path still exposes camera frames to the current trusted VPS/runtime node.
Confidentiality against the operator requires the future confidential-runtime
architecture.

## Remaining security blockers

Highest-impact open items:

1. ReDroid and `virtnoded` still have a privileged container/host trust boundary
   with Docker control and host devices.
2. Active runtime plaintext and key material are not protected from the node or
   VPS operator.
3. There is no confidential VM, attestation verification, runtime lease, or
   lease-scoped key release.
4. Encrypted user blobs remain on the same VPS as the control plane and runtime
   node.
5. Reliability evidence is not yet complete for interruption, disk pressure,
   rollback/fork, corruption, and cleanup failure cases.
6. Camera/file import channels are absent and must not be represented as
   available.
7. The local security dependency update and Android fixes are not yet deployed.

## What is next

### Milestone 0 — finish this maintenance release

- review the final diff;
- commit the log fix, dead-resource cleanup, and updated reports;
- complete user acceptance on the installed final debug APK;
- create a reviewed VPS release containing the Go dependency fix;
- verify public/VPS health and repeat the smoke checks after deployment.

### Milestone 1 — runtime reliability and cleanup evidence

- concurrent and repeated start/stop/delete operations;
- Android viewer network loss, reconnection, and service restarts;
- control-plane/node restarts during provisioning and snapshot save;
- snapshot rollback, fork, corruption, and skipped-generation rejection;
- free-disk headroom, storage quota, trial-time, and disk-pressure failures;
- persona restart, factory reset, runtime deletion, and account deletion;
- database, container, network, session, capability, snapshot, and filesystem
  orphan audits after every injected failure.

### Milestone 2 — camera passthrough feasibility

Run the disposable V4L2/ReDroid proof above. Do not enable the production API or
UI until the guest HAL and isolation requirements pass.

### Milestone 3 — reduce the runtime trust boundary

- package ReDroid in a full confidential VM candidate;
- implement attested runtime leases;
- bind viewer and storage-key release to verified measurements and expiry;
- move runtime nodes away from the control-plane host;
- document what the control plane, node operator, storage provider, and client
  can each observe.

### Deferred by current decision

- renterd wallet funding and production Sia cutover;
- multi-operator scheduling;
- public claims of anonymity, trustlessness, or end-to-end confidentiality.

Those claims remain prohibited until the architecture and evidence actually
support them.
