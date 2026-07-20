# Virtroid remediation status — 2026-07-19

This document separates verified production observations from code-only claims.
Runtime checks, database state, immutable release identity, and reproducible
tests are the evidence sources. It is a remediation ledger, not an independent
security certification.

## Release decision

Virtroid is suitable for development and tightly controlled trusted-operator
testing with disposable data. It is **not ready for public beta, hostile
multi-tenancy, production use with irreplaceable data, or high-threat/OPSEC
use**.

Do not describe the current system as end-to-end encrypted or confidential.
Snapshot encryption protects stored blobs, and the viewer has an inner
encrypted channel, but the selected runtime node receives usable material and
controls the live execution environment.

## Verified production boundary

| State | Evidence | Meaning |
| --- | --- | --- |
| VPS deployment | Deployment and database checks on `virtroid-cp`, 2026-07-19 | Schema `2026071903`, the approved production operator/node/key registry, healthy services, and a fresh approved-node heartbeat were verified. |
| Release custody | Root-owned source, bundles, release state, secrets, and backups on the VPS | Production builds and releases run only on the VPS. The production checkout has no Git remote or `.github` directory. |
| Android client | HTTPS endpoint and certificate pin | The client communicates with the VPS; it has no role in building or administering the backend. |
| Development Mac and GitHub | Development source and passive source safekeeping | Neither is a production runner, registry, secret store, release archive, rollback store, or backup target. |
| Target architecture | `docs/final-product-specification.md` and `docs/runtime-poc/` | CVM isolation, runtime attestation, lease-scoped key release, and related claims remain design targets. |

The production release helper accepts only a clean, root-owned offline source
commit; rejects Git remotes, `.github`, unsafe ownership, writable or special
paths; builds the `linux/amd64` backend image on the VPS; stores a checksummed
root-only bundle; installs the reviewed deployment tree; takes a consistent
backup; verifies the trusted node fingerprint and fresh heartbeat; and opens
HTTPS ingress last.

## Verified security and operational controls

- Signed device requests bind method, path, body hash, timestamp, nonce, and
  replay state.
- Session, runtime, envelope, capability, lifecycle, cleanup, and deletion
  ownership checks are enforced.
- Snapshot creation and restoration reject unsafe size, count, path, chunk,
  link, special-file, sparse-file, and cross-runtime states.
- Production runtime images require immutable digests and configured resource
  limits; preinstalled apps require pinned artifacts and package verification.
- Public bootstrap, development node enrollment, unpinned catalog updates, and
  unsafe password-verifier replacement fail closed.
- Schema `2026071903` requires active operator-approved node keys and records
  append-only registry audit actions.
- Android release signing fails closed, v1 signing is disabled, sensitive local
  material is cleared more aggressively, and release manifest/privacy gates
  are enforced.
- SSH uses a key-only administrative account with direct root login and password
  authentication disabled. Fail2ban, bounded logs, unattended security updates,
  atomic certificate renewal, and Compose network separation are installed.
- Local backups drain ingress, refuse active sessions or managed guests, stop
  writers, capture PostgreSQL, `/srv`, release state and the reviewed deploy
  tree, and verify portable checksums under a shared maintenance lock.

The root-only release bundle and same-host backups provide rollback from
deployment or data mistakes. By explicit isolation choice, they are not copied
to a Mac, GitHub, or external backup provider. Total VPS or provider loss is
therefore an accepted and documented availability risk.

## Verification evidence

Recorded checks for the remediation include backend unit and race tests,
`go vet`, command builds, PostgreSQL registry/lifecycle integration, Android
unit/lint/build/security-manifest checks, deployment shell/configuration tests,
SSH hardening, certificate renewal, immutable image inspection, schema and
registry inspection, local backup validation, node identity/heartbeat checks,
and public HTTPS health.

Passing development tests do not by themselves prove production state. The
authoritative running source SHA, image identities, schema label, deployment
tree digest, and checksums live in the root-owned VPS release state and bundle.

## 2026-07-20 local-disk hardening release

The active storage policy is now explicit: runtime-userdata blobs stay on VPS
`local-disk`, and Sia/renterd remains inactive. Sia activation, wallet funding,
seed custody, contracts, and cutover are not release prerequisites for this
phase.

This release closes or substantially narrows the following findings:

- cumulative trial runtime is derived from durable session history, exposed as
  used/remaining seconds, enforced at start and session creation, and reaped at
  exhaustion
- account storage usage is derived from validated encrypted manifest chunk
  sizes; byte quotas are enforced both before node commit and under a locked
  control-plane transaction to prevent concurrent over-allocation
- explicit Android restart-with-new-persona and factory-reset controls now
  expose the implemented lifecycle semantics
- the shared control-plane callback secret is retired; callbacks are signed by
  a dedicated P-256 private key and verified by the node against a pinned public
  key with request-context, timestamp, nonce, body-hash, and replay checks
- version-3 snapshot manifests carry authenticated monotonic generations;
  control-plane commits reject rollback, fork, and skipped generations, and the
  Android client maintains a device-local high-water mark
- the Android entitlement and account surfaces report real trial-time and
  encrypted-storage usage instead of development or Sia-provisioning copy

Release-candidate evidence on 2026-07-20 includes `go test -race ./...`,
`go vet ./...`, builds of every backend command, Android unit/lint/debug-build
and release-manifest checks, all VPS deployment regression scripts, and an
emulator APK install/launch smoke with no app crash or ANR. Live deployment
identity and service state remain authoritative only after the VPS release
state records the new immutable source and image.

## Unresolved production and architecture blockers

- **Isolation:** replace privileged shared-kernel ReDroid containers and
  Docker-socket node authority with VM/CVM-grade tenant isolation. Add enforced
  disk quotas and deny-by-default metadata/private-network egress without
  blocking intended internet access.
- **Runtime attestation and key release:** implement measured runtime images,
  a verifier, client-approved policy, replay-resistant leases, and
  lease-scoped release of runtime/viewer/snapshot material. The trusted node
  operator can still observe live plaintext and usable keys.
- **Account key architecture:** replace device-derived snapshot-key custody
  with a versioned account DEK wrapped to authorized devices, plus tested
  recovery, rotation, and transactional rewrap flows.
- **Internal transport hardening:** node requests and control-plane callbacks
  are now mutually authenticated at the application layer, but same-host
  service traffic is not separately encrypted. Require mTLS, independent key
  rotation/revocation, and network identity before any cross-host node rollout.
- **App supply chain:** validate F-Droid signed metadata roots and APK signer
  lineage. Operator-pinned hashes and package-name checks are containment, not
  complete publisher identity.
- **Abuse and resource enforcement:** stored-byte and cumulative trial-time
  quotas are now enforced, but host-filesystem capacity/reservation controls,
  durable invite or billing controls, and production scheduler policy remain.
  Public bootstrap must remain off.
- **Rollback assurance:** manifest monotonicity and a device-local Android
  high-water mark now block ordinary rollback/fork responses. Cross-device or
  malicious-control-plane rollback resistance still requires an external
  transparency witness or attested key-release design.
- **Availability:** same-VPS backups cannot recover from total VPS or provider
  loss. Changing that requires an explicit future decision to relax the current
  isolation boundary.
- **Operational assurance:** obtain Android instrumentation and physical-device
  evidence, full lifecycle/viewer/local-storage fault tests,
  multi-host/multi-replica staging, and an independent penetration/privacy
  review. renterd evidence is required only if Sia is later re-approved.

No public claim of confidential execution, end-to-end encryption, anonymity,
unlinkability, forensic erasure, or resistance to a malicious infrastructure
operator is justified until the relevant architecture and independent evidence
exist.
