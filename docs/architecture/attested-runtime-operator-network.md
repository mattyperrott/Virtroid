# Attested Runtime Operator Network

## Purpose

This brief defines the target architecture for moving Virtdroid from a single-VPS control-plane/runtime deployment toward independently operated runtime nodes with user-authorized leases, client-side key release, encrypted portable state, and optional confidential-computing attestation.

The design goal is not to make the control plane "decentralized" for its own sake. The control plane should become boring, weak infrastructure: it can coordinate, suggest, meter, and record, but it must not be able to unilaterally choose a malicious runtime node or obtain long-lived user secrets.

The security-critical authority should move to:

- user-device signatures
- user-selected node/operator policy
- lease-scoped runtime authorization
- client-verified attestation
- client-side release of short-lived runtime material
- encrypted user data stored outside the runtime host

## Current Baseline

The current deployed Virtdroid shape is a single VPS containing:

- `virtdroidd` control plane
- `virtnoded` node/relay
- Postgres metadata
- privileged ReDroid runtime containers
- local runtime data under `/srv/virtdroid/runtimes`
- local blob chunks under `/srv/virtdroid/runtimes/_blobstore/local`

The existing code already has the beginning of a node model: nodes heartbeat to the control plane, advertise capabilities, and fetch runtime assignments. The target architecture keeps that shape but changes the trust model.

## Target Product Model

Virtdroid should be modeled as:

> Portable encrypted user state plus user-authorized runtime execution.

The durable asset is user-controlled encrypted state. The runtime is disposable compute. Independent runtime operators may provide compute, but they do not automatically become trusted with account-wide secrets.

There are two supported runtime trust tiers.

### Trusted Operator Runtime

In this mode, the user chooses a known operator or a user policy chooses one from an approved set.

Properties:

- works on ordinary VPS or bare-metal nodes
- does not require confidential computing hardware
- user data remains encrypted at rest
- live runtime plaintext is visible to the chosen operator
- operator trust must be surfaced honestly in the client
- suitable for early independent-node support

This mode is decentralized by operator choice, not by hardware-enforced confidentiality.

### Attested Runtime

In this mode, the runtime node must prove it is running an approved measured workload before the user device releases lease-scoped runtime material.

Properties:

- requires TDX, SEV-SNP, Nitro, dstack, Contrast, or equivalent attestation
- client verifies attestation locally or through a verifier whose output is independently checkable
- quote binds the lease, node key, runtime image, software version, challenge nonce, and viewer/session key
- control plane cannot forge the proof
- runtime material is not released if measurements, node identity, lease, or policy do not match
- suitable for the long-term trust-minimized runtime tier

## Non-Goals

The first implementation should not attempt:

- live migration of running Android runtimes
- permissionless anonymous node registration
- global multi-master control-plane writes
- on-chain real-time scheduling
- releasing master user blob keys to runtime nodes
- promising private live execution on ordinary VPS/container nodes

## Threat Model

### Trusted

- User device local key store and policy engine
- User-approved recovery flow
- Signed Virtdroid client releases

### Partially Trusted

- Control plane for availability, coordination, and metadata integrity
- Chosen trusted operators during live runtime execution
- Sia/renterd for durable encrypted blob availability, not plaintext privacy

### Untrusted By Default

- Independent runtime node operators
- Runtime host operating systems
- Public network paths
- Control plane if compromised
- Storage hosts
- Non-attested runtime images

### Primary Threats

- Control plane silently routes a user to a malicious node
- Node operator reads snapshot keys or live runtime plaintext
- Node lies about capabilities, software version, or region
- Node replays an old attestation quote
- Node uses a valid quote for a different user, lease, or session
- Node swaps runtime image after lease approval
- Node steals long-lived blob credentials
- Operator keeps snapshots or logs beyond policy
- Viewer traffic is redirected or terminated by the wrong endpoint

## Core Principle

The control plane may propose. The user device authorizes.

The control plane can return candidate nodes, record leases, meter usage, and help route sessions. It must not be able to make the user device release runtime material unless the selected node, lease, policy, challenge, and attestation all match.

## High-Level Components

### Android Client

Responsibilities:

- owns user-device signing key
- stores user policy preferences
- requests runtime candidates
- chooses node/operator or applies policy
- signs runtime leases
- verifies attestation
- releases only short-lived runtime material
- verifies viewer/session public key before connecting
- shows truthful trust status to the user

### Control Plane

Responsibilities:

- account and device metadata
- node registry
- operator registry
- candidate-node discovery
- lease coordination
- session metadata
- metering and billing hooks
- public audit log production
- relay discovery

The control plane must not store:

- master user blob key
- cross-device recovery key
- long-lived Sia/renterd credential
- node-wide plaintext snapshot key
- global runtime decryption credential

### Runtime Node

Responsibilities:

- runs `virtnoded`
- registers node identity and capabilities
- accepts lease-scoped assignments
- boots runtime workload
- produces attestation when supported
- exposes viewer/session endpoint
- receives only lease-scoped runtime material
- pulls encrypted blobs from Sia/renterd or a scoped proxy
- snapshots encrypted state at lease end
- enforces retention policy locally

### Operator

Responsibilities:

- owns operator identity
- runs one or more nodes
- publishes pricing, region, jurisdiction, support policy, and trust tier
- signs node registrations or authorizes node keys
- accepts audit and reputation consequences

### Storage Layer

Responsibilities:

- stores encrypted user snapshots/blobs
- supports distributed storage through Sia/renterd
- exposes scoped read/write paths or tokens
- does not require trusting storage hosts with plaintext

## Runtime Start Protocol

1. User device asks the control plane for runtime candidates.

2. Control plane returns candidate nodes:

   - node public key
   - operator identity
   - region
   - price
   - reputation
   - attestation type
   - software version
   - capabilities
   - current availability
   - policy flags

3. User device chooses a node or applies local policy:

   - only attested nodes
   - only trusted operators
   - cheapest available
   - same jurisdiction
   - GPU required
   - no experimental runtimes
   - no operator with metadata logging

4. User device creates a runtime lease request containing:

   - account ID
   - device ID
   - runtime ID
   - selected node public key
   - selected operator ID
   - required capability set
   - trust tier
   - expiry timestamp
   - client challenge nonce

5. User device signs the lease request.

6. Control plane records the pending lease and forwards the assignment to the selected node.

7. Node boots the runtime environment.

8. Node creates an ephemeral viewer/session keypair.

9. Node returns an attestation package:

   - hardware quote or platform-specific attestation document
   - canonical attestation payload
   - runtime image hash
   - `virtnoded` binary hash
   - guest/kernel/init measurement where available
   - node public key
   - lease ID
   - user-device challenge nonce
   - ephemeral viewer/session public key
   - expiry timestamp

10. User device verifies:

    - lease signature matches local device key
    - node public key matches selected node
    - operator ID matches selected operator
    - attestation type satisfies policy
    - attestation quote is valid for the hardware/platform
    - quote binds the canonical payload hash
    - image and software hashes are approved
    - nonce matches the current request
    - lease ID matches the active lease
    - expiry is valid
    - capabilities satisfy the selected policy

11. User device releases only lease-scoped runtime material.

12. Runtime starts and pulls encrypted user blobs from Sia/renterd using scoped access.

13. Viewer session starts using the attested viewer/session public key.

14. On shutdown, the runtime snapshots state, encrypts it, stores it through Sia/renterd, and destroys local plaintext.

## Canonical Attestation Payload

The attestation quote should bind one canonical payload hash, not a loose set of fields.

Example payload:

```json
{
  "version": 1,
  "lease_id": "uuid",
  "account_id_hash": "base64url",
  "runtime_id": "uuid",
  "user_device_id": "uuid",
  "user_device_challenge_nonce": "base64url",
  "node_public_key": "base64url",
  "operator_id": "operator-slug-or-key-id",
  "ephemeral_viewer_public_key": "base64url",
  "runtime_image_hash": "sha256:...",
  "virtnoded_binary_hash": "sha256:...",
  "guest_measurement": "platform-specific",
  "capabilities_hash": "sha256:...",
  "attestation_type": "tdx",
  "software_version": "x.y.z",
  "issued_at": "2026-05-06T00:00:00Z",
  "expires_at": "2026-05-06T00:10:00Z"
}
```

The payload must be canonicalized before hashing. The resulting hash should be placed into the platform attestation binding field:

- TDX `REPORTDATA`
- SEV-SNP report data
- Nitro attestation user data or equivalent
- dstack attestation binding
- Contrast workload measurement policy

## Attestation Must Bind

Required fields:

- `lease_id`
- `user_device_challenge_nonce`
- `node_public_key`
- `operator_id`
- `ephemeral_viewer_public_key`
- `runtime_image_hash`
- `virtnoded_binary_hash`
- `guest_kernel_or_initramfs_hash` where available
- `attestation_type`
- `software_version`
- `allowed_capabilities`
- `expiry_timestamp`

Optional fields:

- region
- jurisdiction
- price quote
- GPU model
- kernel command line hash
- runtime base image signature
- transparency-log checkpoint
- operator policy hash

## Runtime Material

The user device may release:

- short-lived runtime snapshot key
- short-lived blob read token
- short-lived blob write token
- viewer/session encryption key
- runtime session token
- per-lease persona material needed by the runtime

The user device must not release:

- master user blob key
- long-lived account credential
- control-plane admin credential
- global Sia/renterd credential
- cross-device recovery key
- reusable device signing key
- permanent operator credential

## Node Capability Advertisement

Each node should register a signed capability document.

Example:

```json
{
  "node_pubkey": "base64url",
  "operator_id": "operator-id",
  "region": "eu-west",
  "jurisdiction": "NL",
  "price": {
    "currency": "USD",
    "runtime_hour": "0.18",
    "egress_gb": "0.02"
  },
  "reputation": {
    "uptime_30d": "99.3",
    "completed_sessions": 1042,
    "disputes": 0
  },
  "attestation": {
    "type": "none | tdx | sev-snp | nitro | dstack | contrast",
    "verifier": "url-or-verifier-id",
    "measurements": ["sha256:..."],
    "quote_binding": "canonical-payload-hash"
  },
  "runtime": {
    "virtnoded_version": "x.y.z",
    "redroid_supported": true,
    "binder_supported": true,
    "ashmem_supported": false,
    "memfd_supported": true,
    "privileged_container_supported": true,
    "gpu_mode": "none | host | virtio | nvidia",
    "max_resolution": "1080p",
    "max_fps": 60
  },
  "viewer": {
    "protocols": ["webrtc", "turn", "quic", "tcp-relay"],
    "udp_supported": true,
    "tcp_fallback": true,
    "max_bitrate_mbps": 12
  },
  "policy": {
    "tenancy": "single-user-runtime",
    "snapshot_retention": "ephemeral",
    "logs": "metadata-only",
    "operator_plaintext_access": "possible | constrained | attested-denied"
  }
}
```

The node must sign this document. The operator should also sign it or publish an operator-level authorization binding the node key to the operator identity.

## Candidate Selection Policy

The Android client should support policy presets:

- cheapest available
- known operator only
- attested nodes only
- local jurisdiction only
- low-latency preferred
- GPU-enabled runtime
- no experimental runtime support
- no metadata logging

Policy decisions should be local to the user device. The control plane may filter and rank, but the final lease decision should be signed by the user device.

## Operator Modes

### Curated Operators

Initial independent-node mode should use curated operators.

Requirements:

- operator identity is approved by Virtdroid maintainers or governance policy
- operator publishes support, retention, jurisdiction, and abuse policies
- node keys are linked to the operator identity
- client can show operator name and trust tier

### Permissionless Operators

Permissionless registration is a later phase.

Additional requirements:

- anti-Sybil mechanism
- reputation system
- dispute policy
- stake, bond, or rate-limited onboarding
- stronger abuse controls
- transparent operator history

## Control Plane Responsibilities

The control plane should expose APIs for:

- node registration
- node heartbeat
- node capability update
- operator registry lookup
- candidate-node discovery
- lease proposal
- lease recording
- session metadata
- relay discovery
- usage/metering events
- public audit log checkpoints

The control plane should not be trusted to make final key-release decisions.

## Lease Model

A runtime lease is a short-lived, user-device-signed authorization for one node to run one runtime session.

Lease fields:

- lease ID
- account ID or account hash
- device ID
- runtime ID
- selected node public key
- selected operator ID
- required capability set
- attestation requirement
- storage scope
- viewer/session public key requirement
- issued timestamp
- expiry timestamp
- user challenge nonce
- user-device signature

Lease lifetime should be short. Long sessions can be renewed through heartbeat-based lease extension, but renewal should preserve user policy and node identity unless the user explicitly approves migration.

## Key-Release Model

### Phase 1: Client-Direct Release

The Android client verifies node identity and policy, then encrypts short-lived runtime material directly to the node/session public key.

This is the first implementable model.

### Phase 2: Verifier-Assisted Release

The Android client verifies a quote through a verifier service, but still checks the returned claim locally and still performs final release decision locally.

### Phase 3: Attestation-Based KBS

A KBS releases lease-scoped secrets only when attestation and policy pass. The Android client should still verify that the KBS response corresponds to the selected lease and node.

### Phase 4: Threshold Release

Multiple independent release authorities or maintainers are required for sensitive policy updates, runtime-image approval, or operator admission.

## Storage Model

User runtime state should be encrypted before distribution through Sia/renterd.

Rules:

- storage providers never receive plaintext
- runtime nodes receive only lease-scoped snapshot material
- long-lived blob keys stay on user devices or recovery devices
- node access to storage is constrained by runtime ID, lease ID, and expiry
- snapshots are content-addressed and versioned
- snapshot manifests are signed
- deletion/retention policy is recorded and auditable

## Viewer Model

Viewer connection setup must be tied to the lease and attestation.

Requirements:

- node generates ephemeral viewer/session keypair
- attestation payload binds the viewer public key
- client verifies viewer public key before releasing session material
- relay endpoint is selected after node selection
- viewer traffic should support low-latency UDP where possible
- TCP fallback should exist for restricted networks
- ADB should remain private and not publicly exposed

## POC Plan

### POC 1: ReDroid Feasibility On Full VM

Target:

- OpenMetal TDX-capable bare metal or another full TDX/SEV-SNP VM where Virtdroid controls the guest kernel

Goal:

- prove a trust-minimized full VM can run the actual Android runtime

Pass criteria:

- can install/load binder support
- can use ashmem or memfd
- can run Docker privileged enough for ReDroid
- can boot Android
- can attach ADB only privately
- can stream display through Virtdroid viewer
- can handle input latency acceptably
- can persist encrypted runtime snapshot
- can restart from snapshot

### POC 2: Attestation And Key Release With Dummy Runtime

Target:

- Phala/dstack

Goal:

- prove the lease and key-release protocol without ReDroid complexity

Pass criteria:

- node boots known image
- node produces quote
- quote binds image hash
- quote binds node public key
- quote binds lease ID
- quote binds user-device challenge nonce
- client verifies quote locally
- client releases dummy secret only after verification
- secret is unavailable if image hash changes
- secret is unavailable if node key changes
- secret is unavailable if lease expires

### POC 3: ReDroid Inside dstack

Target:

- Phala/dstack

Goal:

- determine whether dstack can host the actual Android runtime workload

Pass criteria:

- can run privileged ReDroid container or equivalent
- can expose binder/hwbinder/vndbinder
- can use ashmem or memfd
- can attach viewer traffic
- can receive secrets only after quote verification
- can prevent host/operator from reading snapshot keys

### POC 4: ReDroid Inside Contrast

Target:

- OpenMetal plus Edgeless Contrast

Goal:

- determine whether confidential Kubernetes can support the runtime workload

Pass criteria:

- can run ReDroid or equivalent Android runtime in a Contrast workload
- can expose required devices safely
- can handle persistent encrypted runtime state
- can use Contrast workload secrets or Vault/OpenBao integration
- can expose viewer traffic without breaking the confidential boundary

### POC 5: Aleph CVM Compatibility

Target:

- Aleph confidential VM

Goal:

- determine whether decentralized CVMs can run the Android runtime and expose attestation in a client-verifiable way

Pass criteria:

- can run custom VM image
- can run Docker
- can run ReDroid
- can expose required kernel modules/devices
- can verify attestation from a Virtdroid client
- can release runtime key only after verification
- can expose viewer traffic with acceptable latency

### POC 6: Marlin Oyster For Non-Runtime Services

Target:

- Marlin Oyster

Goal:

- evaluate confidential sidecar/service use cases, not full Android runtime hosting

Candidate services:

- confidential license service
- confidential billing metering
- confidential reputation oracle
- confidential policy evaluation
- confidential signing/attestation bridge

## Preferred Implementation Sequence

### Phase 0: Document And Freeze Trust Claims

- update product/security docs
- avoid claiming private live execution on non-attested nodes
- label existing single-VPS mode as trusted-operator mode

### Phase 1: Node Identity

- replace shared node secret with per-node public keys
- add operator registry
- sign node heartbeats
- sign capability advertisements
- add node revocation

### Phase 2: Runtime Lease Records

- create lease table
- require user-device signatures for runtime start
- bind lease to runtime, device, node, operator, policy, and expiry
- expose candidate-node API

### Phase 3: Client Policy And Selection

- add Android policy presets
- show operator identity and trust tier
- make node selection explicit or policy-driven
- record selected node in the signed lease

### Phase 4: Client-Side Key Release

- stop releasing active blob keys from the control plane
- implement lease-scoped runtime material
- encrypt release package to node/session public key
- refuse release if node, lease, policy, or expiry does not match

### Phase 5: Dummy Attestation

- build `virtnoded-attest` dummy service
- implement canonical attestation payload
- implement Android-side quote verification interface
- test dstack quote verification and failure cases

### Phase 6: Attested Runtime Support

- integrate attestation into real runtime startup
- bind viewer public key to attestation
- bind runtime image and `virtnoded` hash to attestation
- release secrets only after quote verification

### Phase 7: Independent Operator Program

- add curated operators
- publish node/operator registry
- add audit log
- add reputation metadata
- add billing/metering hooks

### Phase 8: Confidential Runtime Marketplace

- support attested independent operators
- add transparency log for runtime image approvals
- add threshold governance for operator admission and image signing
- expand beyond curated operators only after abuse controls are in place

## Repository Artifacts To Add

Recommended follow-up implementation artifacts:

- `docs/poc/confidential-runtime-poc-matrix.md`
- `docs/architecture/runtime-lease-protocol.md`
- `docs/architecture/node-operator-registry.md`
- `backend/cmd/virtnoded-attest`
- `backend/internal/attestation`
- `backend/internal/leases`
- `android-client/.../security/AttestationVerifier.kt`
- `android-client/.../runtime/RuntimeLeaseSigner.kt`

## Open Questions

- Which hardware attestation target should become the first production-supported tier: TDX, SEV-SNP, or dstack-specific?
- Can ReDroid run reliably inside a confidential VM without breaking binder, networking, or viewer latency?
- Can dstack expose enough host/kernel functionality for ReDroid, or should it remain the attestation/key-release POC only?
- Should the first release support curated independent operators only?
- What transparency-log mechanism should be used for runtime image and operator-key publication?
- What is the minimum acceptable user-visible trust label for ordinary operator nodes?
- How should billing disputes be handled when users claim a node did not satisfy advertised capabilities?

## Success Criteria

This architecture is successful when:

- user data is encrypted and portable across runtime hosts
- the control plane cannot decrypt user snapshots
- the control plane cannot silently redirect runtime secrets to an unapproved node
- the user device signs every runtime lease
- runtime nodes receive only short-lived, lease-scoped material
- node capabilities are signed and visible to the user
- attested nodes bind quote evidence to lease, nonce, node key, image hash, and viewer key
- ordinary trusted-operator nodes are labeled honestly
- independent operators can run runtime nodes without becoming global trust roots
- a failed runtime host can be replaced from encrypted state without account-wide key exposure

