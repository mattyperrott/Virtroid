# Virtroid Current Status and Remaining Work

> **Superseded:** This 2026-05-10 snapshot is historical and must not be used for
> current security, privacy, feature, or release claims. See
> [`remediation-status-2026-07-19.md`](./remediation-status-2026-07-19.md) for
> the current release decision, verified remediations, and remaining blockers.

Generated: 2026-05-10

This document records where the project is now after reviewing every file under `docs/`, comparing the old claims with the current codebase, and factoring in the intended direction:

- Sia/renterd for durable user blobs
- confidential-compute VMs for runtime nodes
- a central control plane/control panel server
- fresh runtime per user session

## Documents Assessed

| File | Current assessment | Still useful | Outdated or misleading |
| --- | --- | --- | --- |
| `docs/Audit.md` | Historical security snapshot. Many findings have since been fixed or partially fixed. | Good source of hardening categories and old gap names. | Its severity table is stale. It still describes missing signed auth, release minification, account deletion gaps, and local-vault issues that have changed. |
| `docs/Full-Specification.txt` | Early product spec. | Runtime/persona/state/client separation, destructive action semantics, file/camera import, trusted-device management. | Generic "distributed shard storage" should be replaced by Sia/renterd encrypted blob storage. Broad reconnect language conflicts with the current "fresh runtime per session" direction. |
| `docs/Markdown-Specification.txt` | Markdown version of the early product spec. | Same as above. It is the most readable old target-state source. | Same as above. It does not include the central control plane, renterd-specific storage, confidential VM runtime nodes, or lease-bound key release. |
| `docs/Virtroid Rundown.md` | Whitepaper-style early concept. | Useful product narrative around disposable runtime plus durable state. | Overstates target guarantees before the renterd, lease, and confidential runtime work exists. |
| `docs/Whitepaper-Specification.txt` | Older whitepaper/spec text. | Same conceptual model as the rundown. | Same storage/reconnect/trust issues as the other early specs. |
| `docs/android-security-privacy-audit-2026-05-09.md` | Historical Android audit. | Good dynamic test checklist and residual hardening list. | Several Android findings have been remediated since the audit. APK metadata/hash/signing facts are time-specific and stale. |
| `docs/runtime-poc/attested-runtime-operator-network.md` | Closest to the intended future architecture. | Strong target model for leases, operators, attestation, trust labels, key release, and renterd storage. | Some referenced paths are stale after docs were moved. It describes architecture, not implemented code. |
| `docs/runtime-poc/confidential-runtime-poc-matrix.md` | Still valid as an execution matrix. | Good next-step POC breakdown: ReDroid in full CVM and dummy attestation/key-release should be separate first milestones. | Not a product spec. It needs follow-through implementation and evidence. |
| `docs/.DS_Store` | Finder metadata, not documentation. | None. | Should be removed eventually. |

## Current Implementation Snapshot

### Android Client

Implemented:

- account bootstrap and device enrollment
- Android Keystore-backed device signing key
- signed device API requests with nonce/timestamp/body hash through backend contract
- app lock with PIN/passphrase and local secure vault
- biometric vault-key support
- identity/blob password setup and cached-session state
- runtime list/create/update/start/stop/wipe/delete controls
- session viewer with TLS relay requirement in release
- viewer public key passed from backend session preparation
- inner viewer encryption
- foreground viewer service for background session continuity
- relay/session heartbeat visibility in the session UI
- stale active-session cleanup when backend reports a session is gone
- account identity/status screen improvements
- system log viewer clear-view action
- secure-window protection on sensitive screens
- encrypted active-session/log/settings stores with vault/Keystore-backed handling
- release manifest gates for backup, cleartext, debug/test, data extraction rules, and preview activity leakage
- release signing is fail-closed and requires externally supplied signing credentials; generated APKs are not committed

Still missing or partial:

- trusted-device list and revoke UX
- explicit restart action that is defined as end-current-session then start fresh runtime
- persona reset UX and backend semantics
- full factory reset semantics
- file import into active runtime
- camera/media import into active runtime
- Play Integrity or equivalent risk signal for high-risk operations
- attestation/lease verification UI
- operator/trust-tier display
- complete dynamic QA matrix on a connected device for the newest APK

### Backend Control Plane

Implemented:

- Go control plane service
- PostgreSQL schema for accounts, devices, storage settings, entitlements, hosts, runtimes, sessions, logs, security events, and runtime start events
- public bootstrap flow with invite/rate-limit controls
- signed `/api/v1/me/...` device API for runtimes, storage, identity, sessions, logs, and account deletion
- ECDSA device request verification with nonce replay protection
- node signed internal API
- runtime create/list/get/update/start/stop/wipe/delete
- session create/heartbeat/close
- session expiry/reaper support
- active blob key handoff vault reduced to short in-memory TTL
- account deletion now queues assigned runtime cleanup instead of directly orphaning containers
- entitlement and start-rate enforcement
- storage settings model for `local-disk` and `sia-renterd`
- security event ingestion with rate limits/retention

Still missing or partial:

- central control panel/admin UI
- operator registry
- runtime lease table
- client-signed runtime lease records
- attestation evidence records
- node/operator policy registry
- transparency or approval log for runtime images and operator keys
- raw blob key transit removal
- final retention policy for logs, sessions, storage metadata, security events, and blobs
- SBOM/SCA/secrets scanning release gates
- production-grade multi-node scheduler policy

### Runtime Node

Implemented:

- `virtnoded` node agent
- node heartbeat and reconciliation loop
- ReDroid Docker runtime creation
- privileged runtime container with binderfs and runtime data mount
- encrypted viewer bridge preparation
- viewer public key extraction
- runtime status/log reporting
- runtime stop/save/delete/wipe cleanup
- encrypted userdata snapshot support
- local and renterd blob-store interfaces
- renterd upload/download/delete-by-manifest paths
- blob preflight and smoke-test logic
- Falco/security-event pipeline in VPS profile

Still missing or partial:

- confidential VM packaging for runtime nodes
- proof that ReDroid runs reliably in a TDX/SEV-SNP/full confidential VM
- `virtnoded-attest` dummy service
- lease-bound node attestation
- lease-scoped key release
- viewer key bound to attestation payload
- runtime image measurement and approval flow
- complete remote renterd garbage collection for old snapshots
- production operator onboarding/deployment docs

### Storage

Implemented:

- runtime snapshot encryption uses gzip plus AES-CTR and HMAC-SHA256
- snapshots are chunked and represented by manifests
- local disk blob store works as a dev fallback
- renterd blob store can put/get/delete manifest-known chunks
- account storage status supports `sia-renterd` in operator-managed mode; user
  wallet and seed mutation are deliberately disabled
- deployment compose includes a renterd profile
- renterd seed and API/database passwords use root-only mounted files instead of
  Docker environment metadata; profile activation requires a recorded offline
  seed-copy and funding ceremony
- the production policy is pinned to renterd's mainnet 10-of-30 redundancy
  default and requires at least 30 active contracts before smoke testing
- the renterd profile uses an isolated MySQL database, a private bucket gate,
  disabled S3/third-party explorer paths, and trusted smoke/cutover helpers
- new manifests use authenticated version-3 metadata and opaque object namespaces
- failed manifest-known deletes are durably journaled and retried
- local-to-renterd migration retains a fallback until a successful remote restore

Still missing or partial:

- renterd is not yet the default production path
- no verified live renterd production smoke result in this current review
- no operator-confirmed physical offline seed copies, mainnet SC funding, active
  contract set, or off-machine encrypted renterd metadata backup yet
- bounded prefix garbage collection is implemented for known runtime namespaces;
  a fleet-wide reconciler for lost journals/unknown historical objects is still missing
- version-3 manifests are HMAC-authenticated; legacy version-1/2 manifests remain
  readable for migration
- long-lived blob key custody is not final
- the old generic "distributed storage node" spec should be replaced with renterd-specific behavior

### Confidential Runtime Direction

Implemented:

- architecture and POC docs exist under `docs/runtime-poc/`
- current code has enough runtime/snapshot/viewer functionality to run feasibility tests

Not implemented:

- no lease protocol in code
- no attestation verifier in backend or Android
- no CVM runtime packaging
- no KBS/verifier-assisted release
- no client-side quote verification
- no operator/trust-tier UX
- no confidential runtime production mode

## Requirements Recovered From Old Docs

The following useful requirements were present in older docs and are not fully implemented yet:

| Requirement | Current status | Decision |
| --- | --- | --- |
| Runtime is disposable and recreated per session | Partially implemented. Runtime containers can be started/stopped and sessions can be created, but final semantics should enforce fresh runtime per session. | Keep and strengthen. |
| Persistent state survives by encrypted snapshot, not by long-lived runtime | Partially implemented. Snapshot save/restore exists. | Keep. Move production storage to renterd. |
| Persona is separate from state | Partial. Persona JSON/version exists, but user-facing reset semantics are not complete. | Keep as future work. |
| Factory reset makes old cloud device unrecoverable | Partial. Wipe/delete/account-delete cleanup exists, but final blob/manifest semantics are incomplete. | Keep and implement explicitly. |
| File import into runtime | Not implemented. UI placeholders were hidden/disabled in practice. | Future feature after core session/storage trust is stable. |
| Camera/media import into runtime | Not implemented. | Future feature after file import. |
| Trusted device management/revoke | Not implemented as full UX/API. | Keep as required account/security feature. |
| Distributed encrypted storage | Partially implemented as encrypted chunked snapshots. | Replace generic distributed node wording with Sia/renterd encrypted blob storage. |
| Reconnect to active runtime | Direction changed. Backgrounding current active session is allowed, but ended/gone sessions should not be resurrected. | Replace with "keep current session alive; next session is fresh runtime." |
| Operator trust labels | Not implemented. | Required before non-single-node or confidential runtime modes are exposed. |
| Attested runtime leases | Not implemented. | Required for confidential runtime mode. |

## Security Audit Residue

Closed or substantially improved since old audit docs:

- signed device API is the main client API path
- release cleartext is blocked
- viewer session now requires TLS in release
- viewer public key is supplied by session prepare and validated by client
- release manifest has backup/data-extraction controls
- sensitive screens use secure-window protection
- active sessions/logs/settings moved toward vault/Keystore-backed storage
- local vault is bound back to app-lock plus Keystore protection
- account deletion queues runtime cleanup instead of directly orphaning assigned runtimes
- viewer foreground service and heartbeat visibility were added

Still open:

- raw blob access key still transits through the backend for runtime/node handoff
- control plane can still mediate active blob key availability during current dev flow
- runtime nodes are not confidential VMs yet
- no attestation-bound viewer key or runtime material release
- no client-side lease verification
- no trusted-device revoke UX
- no Play Integrity/risk scoring
- no SBOM/SCA/release provenance gates confirmed
- dynamic device QA for the latest release artifact still needs a connected Pixel/emulator pass

## Recommended Build Order From Here

### 1. Stabilize the Current Dev Baseline

Status: mostly done.

Keep the current working baseline committed before changing runtime/storage trust semantics again. The current baseline includes working backend, Android client, viewer, identity password flow, log viewer, switches, account deletion cleanup, foreground viewer service, and heartbeat visibility.

### 2. Make Sia/renterd the Real Storage Path

Goal: turn the existing renterd code from optional/dev-tested support into the primary production storage path.

Tasks:

- run live renterd preflight and blob smoke test
- configure `NODE_BLOB_STORE_KIND=sia-renterd` in the dev VPS profile when renterd is ready
- expose storage readiness clearly in Android
- make snapshot restore/save failures visible and recoverable
- add manifest-known remote delete tests
- add plan for remote old-snapshot garbage collection
- document renterd bucket, contract set, min/total shard settings, and failure modes

### 3. Run Runtime Hosting POC A

Goal: prove the actual ReDroid runtime can run inside a full confidential VM target.

Use the existing POC matrix:

- TDX-capable OpenMetal or equivalent full SEV-SNP/TDX VM
- install Docker, binderfs, ADB, ReDroid dependencies
- run `virtnoded` or a minimal equivalent
- boot Android
- connect viewer
- save encrypted snapshot
- restore snapshot into a fresh runtime
- collect evidence: VM attestation availability, kernel/device notes, logs, latency, snapshot restore logs

Do not combine this with lease/key-release work yet.

### 4. Run Runtime Hosting POC B

Goal: prove lease-bound attestation/key-release with a dummy runtime service before mixing in ReDroid complexity.

Tasks:

- add `backend/cmd/virtnoded-attest`
- define canonical lease payload
- expose dummy endpoints for challenge, attestation, and secret release
- implement Android/client-side verification interface or a CLI verifier first
- test negative cases: changed image hash, changed node key, changed operator ID, changed lease ID, expired lease, wrong nonce, stale quote

### 5. Add Runtime Lease Protocol

Goal: make every session start an explicit short-lived lease.

Backend work:

- add lease table
- bind lease to account, device, runtime, node, operator, policy, viewer key, and expiry
- expose lease proposal/recording APIs
- make node assignments lease-aware

Android work:

- sign lease request
- store current active lease locally
- show lease/trust status
- refuse stale or mismatched lease evidence

### 6. Remove Raw Blob Key Transit

Goal: make the central server unable to read long-lived blob material.

Target design:

- Android client keeps or derives long-lived user blob key material locally
- session material is lease-scoped
- release package is encrypted to node/session public key after policy/attestation passes
- backend stores verifiers and metadata, not decryptable long-lived keys
- active key handoff vault disappears or is reduced to encrypted opaque packages

### 7. Integrate Confidential Runtime Mode

Goal: real runtime node receives material only after attestation passes.

Tasks:

- package `virtnoded` for confidential VM deployment
- bind viewer public key to attestation payload
- bind runtime image and node binary hash to attestation payload
- verify quote locally or via verifier-assisted flow
- fail closed when measurements or policy do not match
- label non-attested nodes as trusted-operator mode

### 8. Build Operator and Trust-Tier UX

Goal: make node trust visible before users choose a runtime.

Tasks:

- operator registry API
- node capability advertisement
- Android trust-tier labels
- policy choices: attested only, known operator, dev/local
- logs/security event surfaces for failed attestation or policy mismatch

### 9. Complete Remaining User-Facing Product Features

After runtime/storage trust is stable:

- trusted-device list and revoke
- explicit restart flow
- persona reset flow
- factory reset flow
- file import
- camera/media import
- retention/export/delete controls for logs and metadata
- control panel/admin UI

### 10. Production Hardening

Before production release:

- CI backend tests
- Android release build/lint
- manifest diff gate
- SBOM/SCA
- secrets scanning
- reproducible or controlled signing provenance
- dynamic Android QA on physical device
- backend/node failure tests
- renterd failure tests
- confidential runtime attestation negative tests
- deployment hardening guide

## What Not To Prioritize

- Do not build a general "reconnect to old ended sessions" product model. The intended model is fresh runtime per session.
- Do not bandaid old running containers in dev. Stop and delete stale dev containers when they block progress.
- Do not claim confidential live execution until runtime nodes actually run inside verified confidential VMs.
- Do not claim end-to-end encrypted persistence while raw blob material can still transit the backend.
- Do not expand camera/file import before storage, lease, and runtime lifecycle are reliable.

## Immediate Next Engineering Tasks

The next realistic sequence is:

1. Run a renterd-backed snapshot smoke test on the dev VPS.
2. Flip one dev node to `NODE_BLOB_STORE_KIND=sia-renterd` once renterd is funded/ready.
3. Add tests around renterd save/restore/delete-by-manifest.
4. Start POC A: ReDroid inside a full confidential VM.
5. Start POC B separately: dummy `virtnoded-attest` lease/attestation/key-release.
6. Draft `docs/runtime-poc/runtime-lease-protocol.md` once POC B proves the payload shape.

## Current Definition of Done For The Next Milestone

The next milestone is complete when:

- Android can start a fresh runtime session from a runtime profile.
- The session can background/foreground without dropping the viewer transport under normal conditions.
- Session heartbeat shows healthy/retrying/gone states.
- Runtime stop saves an encrypted snapshot to renterd.
- New session restores from renterd snapshot into a fresh runtime.
- Runtime delete/account delete removes node artifacts and manifest-known renterd chunks.
- Backend and Android tests/builds pass.
- The UI copy labels the current mode as development/trusted-operator unless confidential evidence exists.

## Superseded Old Claims

These old-doc statements should not be repeated as current product claims until implemented:

- "Distributed storage" without saying Sia/renterd and encrypted blob chunks.
- "No single node can reconstruct data" unless the renterd redundancy/key-custody setup is actually proven.
- "End-to-end encrypted persistence" while raw blob key transit exists.
- "Verified" runtime execution before lease-bound attestation exists.
- "Private" live runtime execution on ordinary trusted-operator nodes.
- "Reconnect" as a general product behavior after a session is gone.
- Upload/camera availability before end-to-end import exists.

## Source Touchpoints Checked

Current code facts were checked against:

- `backend/internal/httpapi/api.go`
- `backend/internal/store/schema.sql`
- `backend/internal/store/store.go`
- `backend/internal/config/config.go`
- `backend/cmd/virtnoded/main.go`
- `backend/cmd/virtnoded/blobstore.go`
- `backend/cmd/viewercrypt/main.go`
- `deploy/vps/docker-compose.yml`
- `android-client/app/src/main/AndroidManifest.xml`
- `android-client/app/src/main/java/io/virtroid/client`
- `android-client/app/src/main/java/org/client/scrcpy`
- `android-client/app/build.gradle.kts`
