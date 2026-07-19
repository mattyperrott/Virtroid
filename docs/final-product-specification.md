# Virtroid Final Product Specification

Generated: 2026-05-10

This document is the target product specification for the finished Virtroid system. It supersedes the older early-stage whitepaper/spec drafts in this repository while preserving the useful parts: disposable Android runtimes, durable encrypted state, trusted enrolled devices, explicit reset semantics, and honest trust labels.

## Product Outcome

Virtroid gives a user a personal Android environment that is started on demand, streamed to the Android client, saved as encrypted user state, and destroyed after use.

The finished system has three primary production components:

1. A central Virtroid control plane for accounts, devices, runtime orchestration, leases, billing/storage metadata, relay routing, logs, and the control panel.
2. Runtime nodes running inside confidential-compute VMs where possible. Each session runs a fresh Android runtime and receives only short-lived, lease-scoped material.
3. Sia/renterd-backed user blob storage for durable encrypted runtime snapshots and related user state.

The central server coordinates. It must not be the final authority that can cause user snapshot keys or long-lived user blob keys to be released to arbitrary runtime nodes.

## Product Principles

- Every new user session should get a fresh runtime instance.
- Backgrounding the client may keep the current active session alive, but a gone or ended backend session is not resurrected.
- Persistent user data lives in encrypted snapshots/blobs, not in the runtime container.
- The Android client is an enrolled device and the primary user-side policy/key-release actor.
- Runtime operators are not automatically trusted with long-lived account secrets.
- Sia/renterd provides durable encrypted blob availability. It is not the privacy boundary by itself.
- Confidential compute strengthens runtime-node trust, but the app must label non-attested trusted-operator mode honestly.
- User-facing copy must not claim privacy, verification, directness, or security properties that are not implemented.

## Core Components

| Component | Final responsibility |
| --- | --- |
| Android client | Enroll device, hold device signing key, authenticate app user locally, manage blob identity/password, start/stop sessions, verify lease/attestation policy, release lease-scoped runtime material, display viewer stream, show relay/session health, and perform destructive actions only after confirmation. |
| Control plane | Account/device registry, signed request verification, entitlement/rate limits, storage settings, runtime catalog, node/operator registry, lease records, relay metadata, log/security-event intake, account deletion orchestration, control panel/admin API. |
| Runtime node | Run one or more fresh Android runtimes, restore encrypted user snapshot into a session runtime, launch encrypted viewer bridge, snapshot state at shutdown, destroy runtime-local plaintext, report status/logs/security events. |
| Confidential VM layer | Provide measured runtime environment and attestation evidence for the runtime node. Bind measurement, node key, image hash, lease, nonce, and viewer key before key release. |
| Sia/renterd storage | Store encrypted runtime snapshot chunks/objects. Enforce configured redundancy/contract policy. Support restore, manifest-known deletion, and eventually full remote garbage collection. |
| Relay/viewer path | Route client to active runtime viewer endpoint over TLS plus inner viewer encryption. Bind viewer public key to the session/lease/attestation. |

## Trust Model

### Trusted

- The user's enrolled Android device while unlocked and uncompromised.
- The Android Keystore for local device signing and local sensitive-state protection.
- The signed Virtroid Android app release distributed through the intended release channel.
- The central control plane for account records, scheduling, rate limits, and metadata availability.

### Partially Trusted

- Runtime operators in trusted-operator mode. They may see live runtime plaintext and must be labeled as such.
- The control plane for relay/session metadata. It should not receive long-lived user blob keys.
- Sia/renterd for encrypted blob availability. It must not be treated as plaintext-private storage.

### Untrusted By Default

- Non-attested runtime nodes.
- Network relays and intermediaries.
- Individual Sia hosts or object chunks.
- Old/stale runtime containers.

### Confidential Runtime Target

For confidential runtime mode, the selected node must provide attestation evidence that binds:

- lease ID
- account/device/runtime IDs
- selected node and operator IDs
- runtime image hash and node software measurement
- challenge nonce
- viewer/session public key
- attestation type and policy version
- expiry/lease lifetime

The Android client may use a verifier service, but the final release decision must still be bound to the selected lease, node, policy, and attestation result.

## Identity and Device Model

- Account creation registers an account and one enrolled Android device.
- Each device has an Android Keystore-backed signing key.
- Signed device requests include account ID, device ID, timestamp, nonce, method, path, and body hash.
- Device signing keys authorize API requests, not local vault decryption.
- App-lock protects local sensitive state and is backed by the local secure vault.
- Blob identity/password unlocks user snapshot material and produces or unwraps key material used for runtime snapshot operations.
- Future trusted-device management must allow listing enrolled devices, revoking devices, rotating device keys, and adding recovery devices.

## Runtime Lifecycle

### Create Runtime Record

The user creates a runtime profile in the control plane. This is metadata and policy, not a permanent running phone.

Runtime profile data includes:

- name
- Android image/version/profile
- display size/density
- audio/camera/file capabilities
- blob snapshot policy
- selected storage mode
- selected operator/trust policy

### Start Session

A session start creates a fresh runtime execution environment.

1. Android client authenticates locally if required.
2. Client signs a start/session request.
3. Control plane checks entitlement, account/device ownership, rate limits, and runtime policy.
4. Control plane returns candidate runtime nodes or selects one according to user policy.
5. Client creates or approves a runtime lease.
6. Node boots the fresh runtime environment.
7. Node produces viewer key and, in confidential mode, attestation evidence.
8. Client verifies node/lease/attestation policy where available.
9. Client releases only lease-scoped runtime material.
10. Runtime restores encrypted snapshot from Sia/renterd or starts fresh if no snapshot exists.
11. Viewer session starts with relay TLS and inner viewer encryption.
12. Client shows stream health and relay heartbeat state.

### Background Current Session

The client may move to the background without ending the session.

- The viewer transport should remain active through a foreground service.
- Rendering can pause when the surface is destroyed and resume when a new surface is attached.
- Heartbeat/status visibility must show whether the relay/session is healthy, retrying, or gone.
- If the backend reports that the session no longer exists, the client must clear the stale active-session handle.

### End Session

Ending a session saves state, closes the viewer, and destroys the runtime.

1. Client or timeout requests close/stop.
2. Runtime snapshots `/data` or equivalent persistent user data.
3. Snapshot is compressed, encrypted, chunked, and stored through Sia/renterd.
4. Manifest and metadata are reported to the control plane.
5. Runtime-local plaintext is removed.
6. Container/VM runtime artifacts are removed.
7. Session token and active local handle are cleared.

### Restart

Restart means end the current runtime and start a fresh runtime from the latest valid encrypted snapshot. It is not a reconnect to an old ended runtime.

### Persona Reset

Persona reset replaces the virtual device identity while preserving user data where technically safe.

The finished version must define which identifiers are part of persona versus state, which app data survives, and how reset is surfaced to the user.

### Factory Reset

Factory reset deletes or invalidates:

- current runtime
- active sessions
- runtime manifests
- snapshot references
- encrypted blobs where possible
- local active-session handles
- persona/state continuity metadata

After factory reset, ordinary restore of the previous cloud device must fail.

## Storage and Blob Model

### Final Storage Pipeline

1. Runtime data is collected into a snapshot source.
2. Snapshot is serialized and compressed.
3. Snapshot is encrypted before leaving the trusted runtime/key-release boundary.
4. Encrypted output is split into chunks.
5. Chunks are uploaded to Sia/renterd.
6. Manifest records chunk keys, sizes, hashes, encryption suite, snapshot ID, store kind, and bucket.
7. Manifest is signed or otherwise integrity-protected.
8. Control plane stores only metadata required to find and validate encrypted blobs.

### Key Custody

The final design must avoid raw long-lived blob keys passing through the control plane.

Runtime nodes may receive:

- short-lived runtime snapshot key
- short-lived read token
- short-lived write token
- lease-scoped persona/runtime material

Runtime nodes must not receive:

- master user blob key
- global Sia/renterd credential
- permanent operator credential
- reusable account-wide snapshot secret

### Sia/renterd

Sia/renterd is the production blob storage target.

- Local disk is a development fallback only.
- User-funded Sia/renterd mode is the long-term product path.
- Storage settings must expose whether renterd is configured, funded, synced, and ready.
- Snapshot chunks should use renterd redundancy settings where available.
- Deletion must remove manifest-known chunks and later support full remote garbage collection for old snapshots.
- The client UI must not imply Sia provides plaintext privacy; encryption and key custody provide privacy.

## Viewer and Session Security

- Release builds must reject non-TLS relay metadata.
- Viewer traffic uses inner authenticated encryption.
- The viewer public key must come from a signed/session-bound backend response today and from lease/attestation-bound evidence in the final model.
- Relay tokens are high sensitivity and must be stored only in encrypted local state or memory.
- Session heartbeat must be visible in the client.
- Session expiry and stale-session cleanup must be deterministic.

## Android Client Requirements

The Android app must provide:

- onboarding/account creation
- app-lock and local secure vault
- identity/blob password setup and unlock states
- runtime list and creation
- runtime start/stop/delete/wipe
- live viewer with background service support
- relay/session heartbeat visibility
- runtime logs and system logs
- account deletion and local identity cleanup
- storage funding/configuration UI
- clear status for identity created, blob password configured, password cached, and password required again
- truthful operator/trust tier UI once multiple node modes exist

Future client features:

- trusted device list/revoke
- explicit restart flow
- persona reset flow
- factory reset flow
- file import into active runtime
- camera/media import into active runtime
- attestation evidence display and failure handling
- Play Integrity or equivalent risk signal for high-risk session/blob operations

## Control Plane Requirements

The central server must provide:

- signed device API
- nonce replay protection
- account/device registry
- runtime profile registry
- entitlement and rate limits
- storage settings and preflight status
- session creation, heartbeat, close, and expiry
- runtime node assignment
- node heartbeat and signed internal API
- runtime logs and security events
- account deletion orchestration that queues node cleanup before final hard deletion
- operator registry
- lease table
- attestation evidence records
- control panel/admin views
- retention and erasure policy enforcement

The control plane must not become the final key-release authority for confidential runtime material.

## Runtime Node Requirements

A production runtime node must:

- run inside a confidential VM when offering confidential mode
- have a node signing identity
- advertise runtime capabilities and attestation type
- accept only signed/lease-scoped assignments
- produce attestation evidence when supported
- run fresh Android runtime instances
- restore encrypted snapshots
- launch viewer bridge and expose viewer public key
- report health, logs, and security events
- save encrypted snapshots at stop
- remove runtime containers, local data, and temporary material after stop/delete

## Trust Labels

The UI must distinguish:

| Label | Meaning |
| --- | --- |
| Local development | Single-node/dev mode. No production privacy claim. |
| Trusted operator | User or Virtroid-selected operator can see live runtime plaintext. Storage blobs remain encrypted. |
| Attested runtime | Runtime material released only after quote/policy verification. Operator plaintext access is constrained by the confidential-compute model. |
| Unavailable/unknown | No current evidence for runtime trust. Do not start unless user explicitly allows. |

## Release and Assurance Requirements

Before production release:

- backend tests pass
- Android release build and lint pass
- release manifest gates pass
- APK signing provenance is controlled
- SBOM/SCA/secrets scans run in CI
- dynamic Android QA covers background viewer, stale session, wrong blob password, app restart, runtime delete, backend/node restart, and storage failure
- renterd smoke test passes in deployment
- confidential runtime POC evidence is captured
- user-facing privacy/security copy is reviewed against actual implementation

## Final Acceptance Criteria

Virtroid is finished when:

- a user can create an account, configure blob storage, create a runtime profile, start a fresh session, use the Android viewer, background/foreground the app without losing the active session, end the session, and later start a new fresh session from restored encrypted state
- production snapshots are stored through Sia/renterd, with local disk only as a development fallback
- the control plane cannot read long-lived user snapshot keys
- runtime nodes receive only lease-scoped material
- confidential runtime mode verifies lease-bound attestation before key release
- non-attested operator mode is labeled honestly
- deletion and reset paths clean up backend records, node artifacts, local app state, manifests, and blob references according to policy
- all remaining old-doc claims are either implemented, intentionally removed, or clearly marked as future work
