# Virtroid Current Status and Roadmap

Authoritative status date: 2026-08-01

This report separates repository implementation, local validation, the last
recorded live deployment snapshot, and work that remains unproved. Historical
audits under `docs/` remain provenance rather than current-state claims.

## Executive assessment

Virtroid is a substantial single-VPS remote Android release candidate, not yet
a production-proven private computing service.

The current repository implements signed invite-gated onboarding, runtime and
persona lifecycle operations, an encrypted viewer, encrypted stopped-runtime
snapshots, approved node/operator identities, active-runtime file import,
viewer audio, experimental camera passthrough, node-aware readiness, metrics,
trace-context propagation, and alert rules.

The strongest remaining boundary is unchanged: ReDroid and the node agent run
inside a trusted, privileged VPS boundary. The node can observe active Android
plaintext and participates in snapshot restoration. Virtroid is not
confidential computing, host-blind, operator-blind, or end-to-end encrypted
against the runtime host.

The media and observability additions in this repository have local build,
unit, and PostgreSQL transaction evidence. They have not yet been deployed to
the VPS or proved through a live ReDroid guest. Camera support is therefore
experimental and fail-closed, not a production camera guarantee.

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
- explicit client camera capture, a signed bounded JPEG channel, node-side
  rate limiting and idle cleanup, one-slot V4L2 allocation, and capability-aware
  scheduling;
- `/healthz`, `/readyz`, and `/metrics` on control and node services;
- control readiness that includes database health and fresh approved-node
  readiness;
- W3C `traceparent` propagation and sampled structured trace logs;
- bounded Prometheus metrics plus reviewed Prometheus/Alertmanager rules; and
- a source-vendored, reproducibly built scrcpy server with retained upstream
  notices.

### Locally verified for this candidate

Passed locally on 2026-08-01:

- the full Go unit suite and race detector, `go vet`, and every command build;
- `govulncheck`, with no reachable Go vulnerabilities;
- schema and lifecycle integration against an actual PostgreSQL 18 server;
- concurrent PostgreSQL proof that two camera runtimes cannot claim one camera
  slot;
- Android unit tests, debug/release lint, debug assembly, and the release
  security-manifest gate;
- byte-for-byte comparison between a clean vendored scrcpy build and the
  embedded node asset;
- deployment shell syntax, environment-safety, and Compose checks;
- a `linux/amd64` backend image build with the expected health check and
  `ffmpeg` support;
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

### Last recorded live VPS snapshot

The latest retained read-only production observation is still the 2026-07-25
snapshot. At that point:

| Check | Last recorded result |
| --- | --- |
| Host and edge | `virtroid-cp`, HAProxy on public TCP 443 |
| Public endpoint | `https://virtroid.network/healthz` returned `{"ok":true}` |
| Private services | control on loopback 8080; node on loopback 8090 |
| Core containers | edge, PostgreSQL, control, and node running |
| Deployed source | `ee7ee673c7f7916a53cd07a7728649adf3eddf88` |
| Deployed schema | `2026072102` |
| Runtime inventory | no running guests or live sessions during observation |
| Relational orphan audit | zero orphan devices, runtimes, sessions, or logs |

This is historical evidence, not confirmation of the server's state on
2026-08-01. The current repository schema is `2026080101`. No claim in this
report establishes that the repository candidate has been deployed.

## Capability matrix

| Capability | Repository status | Evidence boundary |
| --- | --- | --- |
| Invite-gated signed bootstrap | Implemented | Unit/API/PostgreSQL evidence; deployment upgrade pending |
| Trusted-device list and revoke | Implemented | Backend and Android flows exist |
| Runtime create/start/connect/stop | Implemented | Repository tests plus earlier single-node live evidence |
| Persona restart and factory reset | Implemented, more fault testing needed | Cleanup semantics exist; exhaustive orphan proof remains |
| Runtime/account deletion | Implemented, more fault testing needed | Relational cleanup covered; repeated failure injection remains |
| Viewer transport | Implemented | TLS and session encryption do not hide plaintext from the node |
| Viewer audio | Release candidate | Full code/build path exists; live ReDroid audio capture is unproved |
| Active-runtime file import | Release candidate | Signed session path and node/ADB logic tested; live guest import is unproved |
| Camera passthrough | Experimental | Client-to-V4L2 path exists; exact ReDroid camera HAL behavior is unproved |
| Encrypted local snapshots | Implemented | Same-VPS stopped-runtime persistence |
| Snapshot rollback protection | Implemented | Monotonic generation checks plus PostgreSQL integration |
| Metrics | Implemented | Bounded service/route/status counters, histograms, and readiness gauges |
| Trace context | Implemented, backend pending | W3C propagation and sampled logs; no OTLP collector or trace store |
| Alerts | Partial | Rules evaluate locally; no external notification receiver is committed |
| Node-aware health/readiness | Implemented | Liveness is separate from readiness; control readiness requires a ready node |
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
decoder. A reproducible build proves which server payload is embedded. It does
not prove that the pinned production ReDroid image exposes a working audio
capture API; that requires a live listening test.

### File import

File import requires a live viewer session and a capability bound to the same
runtime. Request paths, session identifiers, content, and the encoded filename
are covered by signed requests. The node limits the upload to 32 MiB, stages it
with mode `0600`, rejects Android packages, DEX, and JAR-like executables by
name and content, pushes permitted files to `/sdcard/Download`, requests a
media scan, and removes the host temporary file.

This is a document-import path, not arbitrary package sideloading. Live ADB and
Android storage behavior still require disposable-guest acceptance testing.

### Camera

The Android client captures Camera2 JPEG frames only while the user has enabled
the session control. The signed, session-bound route limits frame size; the
node limits the feed to 25 frames per second, converts frames with `ffmpeg`,
writes only to the configured V4L2 device, and stops the feeder after five
seconds idle. The Linux decoder child runs without the node's environment,
root identity, or Docker-group authority. Runtime stop/delete, taint, process
shutdown, client background, or user disable also stop the path.

Scheduling locks the host row and counts active camera runtimes before
allocating a camera slot. A node advertises camera capability only when its
configured device is ready. Without `NODE_CAMERA_DEVICE`, camera runtimes fail
closed at scheduling.

The missing proof is guest-side: the exact pinned ReDroid image must enumerate
the mounted V4L2 device through Camera2 and pass preview, rotation, reconnect,
cross-runtime isolation, and cleanup tests. Until then, camera passthrough is an
experimental operator-enabled feature.

## Observability boundary

Prometheus and Alertmanager are optional loopback-only Compose services. Public
HAProxy access to `/metrics` is denied. Metric labels are bounded to registered
services/routes, normalized methods, status classes, and known outbound target
classes.

Trace context is propagated with W3C headers. Sampled spans are structured log
events; trace identifiers are reduced before logging. There is no OTLP exporter,
collector, or trace-query backend yet.

Alert rules cover service down/not-ready, missing or stale node heartbeats, and
elevated 5xx rates. The committed Alertmanager receiver intentionally has no
email, webhook, or pager destination. Alerts are evaluated and visible, but the
deployment must not be described as paged until an operator installs and tests
a secret-backed receiver.

## Highest-priority remaining work

1. Create a reviewed VPS release, apply schema `2026080101`, and re-run public,
   control, node, database, and orphan checks. Deployment is a separate
   operator-authorized action.
2. Run disposable ReDroid acceptance tests for audio playback, document import,
   and the exact V4L2 camera HAL before enabling camera in production.
3. Configure and test an external Alertmanager receiver, then add a trace
   backend if cross-service trace search is required.
4. Complete interruption, disk-pressure, rollback/fork/corruption, network-loss,
   and lifecycle cleanup fault injection.
5. Decide and document the root-project license. Vendored upstream licenses are
   retained, but they do not license original Virtroid code.
6. Reduce the runtime trust boundary with an isolated/confidential VM design,
   measurement verification, runtime leases, and attestation-bound key release.

### Deferred by current decision

- renterd wallet funding and production Sia cutover;
- public claims of anonymity, trustlessness, host blindness, or end-to-end
  confidentiality; and
- a graphical operator control panel.

Those claims remain prohibited until the architecture and evidence support
them.
