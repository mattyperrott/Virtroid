# Virtroid Current Status and Roadmap

Authoritative status date: 2026-08-02

This report separates repository implementation, local validation, the current
live deployment snapshot, and work that remains unproved. Historical
audits under `docs/` remain provenance rather than current-state claims.

## Executive assessment

Virtroid is now a deployed single-VPS remote Android release candidate, not yet
a production-proven private computing service.

The current repository implements signed invite-gated onboarding, runtime and
persona lifecycle operations, an encrypted viewer, encrypted stopped-runtime
snapshots, approved node/operator identities, active-runtime file import,
viewer audio, physical-camera photo import, node-aware readiness, metrics,
trace-context propagation, and alert rules.

The strongest remaining boundary is unchanged: ReDroid and the node agent run
inside a trusted, privileged VPS boundary. The node can observe active Android
plaintext and participates in snapshot restoration. Virtroid is not
confidential computing, host-blind, operator-blind, or end-to-end encrypted
against the runtime host.

The media and observability additions have local build, unit, PostgreSQL, and
live VPS evidence. Disposable live ReDroid acceptance has proved encrypted
viewer audio packets plus file and JPEG imports. Physical-handset Camera2 use
and audible handset playback remain separate acceptance boundaries.

## Evidence ledger

### Current repository candidate

Implemented in this repository:

- one-time, expiring, invite-gated bootstrap with signed device
  proof-of-possession;
- signed device, capability, node, and callback requests with replay controls;
- runtime create, schedule, start, connect, stop, restore, reset, restart,
  delete, and account-erasure workflows;
- approved operator, node, and overlapping node-key registries;
- encrypted local snapshots with authenticated monotonic generations;
- runtime audio propagated from the runtime profile through the node and
  embedded scrcpy server to the Android viewer decoder;
- session-bound, signed, bounded active-runtime file upload, with node-side
  content checks, temporary-file cleanup, ADB push, and media scan;
- explicit in-app Camera2 photo capture and a signed, session-bound, bounded
  JPEG import into `/sdcard/Pictures/Virtroid`, followed by a media scan;
- `/healthz`, `/readyz`, and `/metrics` on control and node services;
- control readiness that includes database health and fresh approved-node
  readiness;
- W3C `traceparent` propagation and sampled structured trace logs;
- bounded Prometheus metrics plus reviewed Prometheus/Alertmanager rules; and
- a source-vendored, reproducibly built scrcpy server with retained upstream
  notices.

### Locally verified for this candidate

Passed locally on 2026-08-02:

- the full Go unit suite and race detector, `go vet`, and every command build;
- `govulncheck`, with no reachable Go vulnerabilities;
- schema and lifecycle integration against an actual PostgreSQL 18 server;
- PostgreSQL proof that photo-import runtimes do not consume obsolete host
  camera slots;
- Android unit tests, debug/release lint, debug assembly, and the release
  security-manifest gate;
- byte-for-byte comparison between a clean vendored scrcpy build and the
  embedded node asset;
- deployment shell syntax, environment-safety, and Compose checks;
- a `linux/amd64` backend image build with the expected health check;
- Trivy repository and image scans with no high/critical findings, Semgrep
  security/secrets rules with no findings, and TruffleHog with no secrets; and
- Prometheus configuration plus all seven alert rules through `promtool`, and
  the Alertmanager configuration through `amtool`.

### GitHub verification for this candidate

Commit `8e29d6592d3c5c6356145da11ceb07a2b28922fa` passed the independent
post-push checks on 2026-08-01:

- [CI run `30706774390`](https://github.com/mattyperrott/Virtroid/actions/runs/30706774390):
  backend tests, race detector, vet, command builds,
  `govulncheck`, Android tests/lint/build/security gate, scrcpy provenance,
  deployment checks, and monitoring validation all passed;
- [CodeQL run `30706774376`](https://github.com/mattyperrott/Virtroid/actions/runs/30706774376):
  Actions, Go, Python, and Java/Kotlin analysis all passed;
- the two cleartext Android preference findings closed after the affected
  identity binding values were moved behind Android Keystore-backed AES-GCM
  storage; and
- the remaining three UI findings were reviewed and dismissed with audit notes
  because they intentionally display non-secret opaque account/device
  identifiers, not passwords, tokens, private keys, or derived encryption keys.

At the end of the review, GitHub reported zero open code-scanning, Dependabot,
or secret-scanning alerts and zero open pull requests. This is repository and
GitHub evidence, not VPS or live ReDroid proof.

### Current live VPS snapshot

The production release and post-deploy checks completed on 2026-08-02:

| Check | Current result |
| --- | --- |
| Host and edge | `virtroid-cp`, HAProxy on public TCP 443 |
| Public endpoint | `/healthz` and `/readyz` report database ready and one approved ready node |
| Private services | control on loopback 8080; node on loopback 8090 |
| Running services | edge, PostgreSQL, control, node, Prometheus, and Alertmanager |
| Deployed offline source | `e8d4a97e2bea3b945ad080c9949e1b1136df3f2b` |
| Corresponding development source | `8c505a9` plus its parent acceptance-probe commit |
| Deployed image ID | `sha256:02bef4dfc26bfc5ecca2222387b05c8fa4cdb546e45d03f145bd68c309222db9` |
| Deployed schema | `2026080201` |
| Idle-runtime reaper | no-session guest advanced from running generation 2 to stopped generation 3 after the configured threshold |
| Runtime inventory after acceptance | zero runtimes, sessions, capabilities, or managed guest containers/networks |
| Monitoring | both scrape targets up, seven alert rules loaded, no firing alerts |
| Public metrics exposure | `/metrics` returns 404; Prometheus and Alertmanager bind to loopback |

The final release gate also wrote the consistent backup
`/var/backups/virtroid/daily-20260802T084553Z`. Earlier retained backups remain
available; a backup path is recovery evidence, not proof of restore.

## Capability matrix

| Capability | Repository status | Evidence boundary |
| --- | --- | --- |
| Invite-gated signed bootstrap | Deployed | Invite-gated production configuration plus live disposable onboarding |
| Trusted-device list and revoke | Implemented | Backend and Android flows exist |
| Runtime create/start/connect/stop | Deployed | Disposable production guest start, viewer session, stop, snapshot, and deletion passed |
| Idle-runtime cleanup | Deployed and live-proved | Node heartbeats no longer refresh user activity; a no-session guest was reaped after three minutes |
| Persona restart and factory reset | Implemented, more fault testing needed | Cleanup semantics exist; exhaustive orphan proof remains |
| Runtime/account deletion | Implemented, more fault testing needed | Relational cleanup covered; repeated failure injection remains |
| Viewer transport | Implemented | TLS and session encryption do not hide plaintext from the node |
| Viewer audio | Deployed, handset playback pending | Encrypted production relay emitted an audio packet; audible Android-client playback still needs a physical-phone test |
| Active-runtime file import | Deployed and live-proved | Signed session import reached `/sdcard/Download` with matching size and SHA-256 |
| Physical-camera photo import | Deployed, handset capture pending | JPEG reached `/sdcard/Pictures/Virtroid` with matching size/SHA; real-phone Camera2 capture remains pending |
| Encrypted local snapshots | Implemented | Same-VPS stopped-runtime persistence |
| Snapshot rollback protection | Implemented | Monotonic generation checks plus PostgreSQL integration |
| Metrics | Deployed | Both Prometheus scrape targets are up; labels remain bounded |
| Trace context | Deployed, storage backend pending | Public sampled trace propagated into structured control logs; no OTLP collector or trace store |
| Alerts | Deployed locally, paging pending | Seven rules loaded with no firing alerts; no external notification receiver is configured |
| Node-aware health/readiness | Deployed | Public readiness currently requires and sees one ready approved node |
| Multi-node scheduling | Implemented in control logic, not live-proved | Current known deployment remains single-node |
| Confidential runtime VM | Not implemented | ReDroid remains in the privileged host boundary |
| Attestation-bound key release | Not implemented | Design work only |
| Operator-blind persistence | Not implemented | Node handles active state and restore material |
| Sia/renterd production storage | Deferred | Intentionally inactive |
| Operator dashboard | Not implemented | Control plane is API/service infrastructure |

## Media-path safety boundaries

### Audio

The runtime's `audio_enabled` setting is preserved, including explicit `false`,
and passed through the control response, Android session store, node launch
arguments, vendored server, synchronized multiplexed transport, and Android
decoder. A disposable production guest proved TLS relay upgrade, viewer-key
pinning, encrypted frame decryption, and receipt of an actual audio packet from
the pinned ReDroid image. It did not audibly exercise `AudioTrack` on a physical
phone; that remains the final client playback test.

### File import

File import requires a live viewer session and a capability bound to the same
runtime. Request paths, session identifiers, content, and the encoded filename
are covered by signed requests. The node limits the upload to 32 MiB, stages it
with mode `0600`, rejects Android packages, DEX, and JAR-like executables by
name and content, pushes permitted files to `/sdcard/Download`, requests a
media scan, and removes the host temporary file.

This is a document-import path, not arbitrary package sideloading. Disposable
production acceptance imported a 37-byte text file to
`/sdcard/Download/virtroid-live-import.txt`; the response byte count and
SHA-256 matched the submitted content.

### Physical-camera photo import

The live viewer top bar exposes a camera icon when the runtime uses
`photo-import`. Pressing it opens an app-internal Camera2 screen rather than an
external camera application. After the user takes a picture, the client sends
one JPEG through a signed capability bound to the active session and runtime.
Both control and node enforce a 16 MiB limit, JPEG structure, and a 64-megapixel
dimension ceiling.

The node stages the image with mode `0600`, pushes it to
`/sdcard/Pictures/Virtroid`, requests an Android media scan, and removes the
host temporary file. This design intentionally has no VPS camera device,
kernel module, V4L2 loopback, `ffmpeg` feeder, ReDroid camera HAL, or camera-slot
scheduler dependency. Disposable production acceptance imported a JPEG to
`/sdcard/Pictures/Virtroid/virtroid-live-photo.jpg` with matching byte count and
SHA-256. Pressing the Camera2 UI and taking that picture on a physical phone is
the remaining camera acceptance boundary.

## Observability boundary

Prometheus and Alertmanager are deployed loopback-only Compose services. Public
HAProxy access to `/metrics` is denied. Metric labels are bounded to registered
services/routes, normalized methods, status classes, and known outbound target
classes.

Trace context is propagated with W3C headers. Sampled spans are structured log
events; trace identifiers are reduced before logging. There is no OTLP exporter,
collector, or trace-query backend yet.

Alert rules cover service down/not-ready, missing or stale node heartbeats, and
elevated 5xx rates. Both scrape targets are up, seven rules are loaded, and no
alerts fired in the post-deploy check. The committed Alertmanager receiver
intentionally has no email, webhook, or pager destination. The deployment must
not be described as paged until an operator installs and tests a secret-backed
receiver.

## Highest-priority remaining work

1. Install the Android client on a physical phone and accept the Camera2
   capture-to-live-guest flow plus audible audio playback through `AudioTrack`.
2. Configure and test an external Alertmanager receiver, then add a trace
   backend if cross-service trace search is required.
3. Complete interruption, disk-pressure, rollback/fork/corruption, network-loss,
   and lifecycle cleanup fault injection.
4. Decide and document the root-project license. Vendored upstream licenses are
   retained, but they do not license original Virtroid code.
5. Reduce the runtime trust boundary with an isolated/confidential VM design,
   measurement verification, runtime leases, and attestation-bound key release.

### Deferred by current decision

- renterd wallet funding and production Sia cutover;
- public claims of anonymity, trustlessness, host blindness, or end-to-end
  confidentiality; and
- a graphical operator control panel.

Those claims remain prohibited until the architecture and evidence support
them.
