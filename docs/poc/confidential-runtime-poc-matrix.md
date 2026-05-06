# Confidential Runtime POC Matrix

## Purpose

This matrix turns the attested runtime operator architecture into concrete proof-of-concept work. The goal is to answer two separate questions before combining them:

1. Can Virtdroid's actual ReDroid runtime stack run inside a confidential/full-VM environment with acceptable performance?
2. Can a user device verify attestation and release only lease-scoped runtime material to the selected node?

The POCs should be evaluated independently first. Do not make ReDroid, attestation, key release, decentralized operators, and viewer streaming all one first milestone.

Related architecture brief:

- `docs/architecture/attested-runtime-operator-network.md`

## Current Runtime Constraints

Virtdroid currently depends on host-level behavior that many confidential container platforms may not expose.

Required runtime capabilities:

- install or load binder support
- expose binder/hwbinder/vndbinder devices
- support ashmem or memfd path required by ReDroid
- run Docker or container runtime with enough privilege for ReDroid
- keep ADB private
- expose viewer traffic through Virtdroid relay/viewer stack
- persist encrypted runtime data
- restart from encrypted snapshot

These constraints make a full confidential VM the first runtime-feasibility target.

## POC Summary

| POC | Target | Primary Question | Expected Risk | Priority |
|---|---|---|---|---|
| A | OpenMetal TDX-capable full VM or equivalent full SEV-SNP/TDX VM | Can ReDroid run in a confidential/full-VM host? | Kernel/device/runtime friction | P0 |
| B | Phala/dstack dummy `virtnoded-attest` | Can client-side attestation/key release work? | Protocol and verifier integration | P0 |
| C | Phala/dstack ReDroid | Can dstack host the actual Android runtime? | Privileged runtime and binder access | P1 |
| D | OpenMetal plus Contrast | Can confidential Kubernetes host the runtime? | Kata/pod security restrictions | P1 |
| E | Aleph CVM | Can a decentralized CVM marketplace host the runtime? | Attestation and device support unknowns | P1 |
| F | Marlin Oyster | Can Oyster support confidential sidecar services? | Not intended for Android runtime | P2 |

## Shared Evaluation Questions

Every runtime-host candidate should answer:

| Question | Required Evidence |
|---|---|
| Can it run a full VM or only a container? | Platform docs, instance type, kernel access, operator access model |
| Can Virtdroid run Docker/ReDroid? | ReDroid container boots; `adb devices` sees private target |
| Can it expose binder/hwbinder/vndbinder or equivalent? | Device nodes present; ReDroid services boot |
| Can it use ashmem or memfd? | ReDroid boot logs and kernel feature checks |
| Can the client verify attestation? | Quote or attestation document verified outside the platform dashboard |
| Can key release be bound to lease and measurement? | Secret denied on changed image/key/lease/expiry |
| Can independent operators run nodes? | Operator registration/deployment docs or working node setup |
| Can viewer traffic work with acceptable latency? | Viewer connects, input works, latency measured |
| Can snapshots persist and restore? | Encrypted snapshot created, stored, restored into fresh runtime |

## POC A: ReDroid Feasibility On Full VM

### Target

OpenMetal TDX-capable bare metal with a self-managed TDX guest, or another full TDX/SEV-SNP VM where Virtdroid controls the guest kernel.

### Purpose

Prove that a confidential/full-VM environment can host the actual Android runtime stack.

### Scope

This POC does not need decentralized operators, client-side quote verification, or final key-release logic. It should focus on kernel, container, Android boot, viewer traffic, and snapshot restore.

### Setup Tasks

- Provision TDX/SEV-SNP-capable host or VM.
- Install supported Linux guest image.
- Confirm confidential VM status from inside guest.
- Install Docker or compatible container runtime.
- Run Virtdroid host preparation.
- Install/load binder support.
- Start `virtnoded` against a test control plane.
- Start a single ReDroid runtime.

### Pass Criteria

- Binder support can be installed or loaded.
- Required binder devices exist.
- ReDroid can use memfd or ashmem path.
- Docker can run ReDroid with required privileges.
- Android boots to completed state.
- ADB is reachable only privately.
- Virtdroid viewer can stream display.
- Input latency is acceptable for basic navigation.
- Encrypted runtime snapshot can be created.
- Snapshot can restore into a fresh runtime.

### Failure Criteria

- Binder support cannot be loaded or exposed.
- Platform blocks required privileged container behavior.
- ReDroid boots but viewer cannot attach.
- ADB must be exposed publicly for the stack to work.
- Snapshot restore requires plaintext outside the runtime lease.

### Evidence To Collect

- instance type and hardware attestation capability
- kernel version and command line
- binderfs mount output
- Docker info
- ReDroid container inspect
- Android boot logs
- `adb devices` output showing private target
- viewer connection logs
- latency measurement notes
- snapshot manifest and restore logs

## POC B: Attestation And Key Release With Dummy Runtime

### Target

Phala/dstack running a tiny `virtnoded-attest` container.

### Purpose

Prove the security protocol before attempting ReDroid in dstack.

### Scope

This POC should not boot Android. It should implement the smallest possible workload that:

- owns a node key
- receives a lease
- produces an attestation quote
- binds the quote to the canonical lease payload
- exposes an ephemeral viewer/session public key
- receives a dummy secret only after client verification

### Dummy Service

`virtnoded-attest` should expose:

- `GET /healthz`
- `POST /leases/{id}/challenge`
- `GET /leases/{id}/attestation`
- `POST /leases/{id}/secret`

### Pass Criteria

- Node boots known image.
- Node produces quote.
- Quote binds image hash.
- Quote binds node public key.
- Quote binds lease ID.
- Quote binds user-device challenge nonce.
- Quote binds ephemeral viewer/session public key.
- Client verifies quote locally.
- Client releases dummy secret only after verification.
- Secret is unavailable if image hash changes.
- Secret is unavailable if node key changes.
- Secret is unavailable if lease ID changes.
- Secret is unavailable if challenge nonce changes.
- Secret is unavailable if lease expires.

### Failure Criteria

- Quote only proves "some TDX machine" without binding lease-specific data.
- Verification depends entirely on trusting a platform dashboard.
- Node can replay a quote from a different lease.
- Node can receive secret before attestation verification.
- Secret remains valid after lease expiry.

### Evidence To Collect

- Docker image digest
- dstack app/config manifest
- raw quote or attestation document
- canonical payload JSON
- canonical payload hash
- verifier output
- Android/local client verification result
- negative test logs for changed hash/key/lease/expiry

## POC C: ReDroid Inside dstack

### Target

Phala/dstack running ReDroid or an equivalent Android runtime workload.

### Purpose

Determine whether dstack can move from attestation/key-release protocol POC to actual runtime hosting.

### Entry Condition

POC B passes.

### Pass Criteria

- Can run privileged ReDroid container or equivalent.
- Can expose binder/hwbinder/vndbinder.
- Can use memfd or ashmem path.
- Can attach viewer traffic.
- Can produce quote bound to lease payload.
- Can receive secrets only after quote verification.
- Can keep snapshot keys unavailable to the host/operator under the platform's claimed threat model.
- Can restart from encrypted snapshot.

### Failure Criteria

- dstack cannot expose needed devices.
- dstack cannot run privileged nested container workload.
- viewer traffic cannot be exposed with acceptable latency.
- quote cannot bind the lease payload.
- key release requires trusting the operator outside attestation.

### Evidence To Collect

- dstack runtime configuration
- container privilege/device configuration
- binder/memfd evidence
- ReDroid boot logs
- viewer connection logs
- quote payload
- key-release logs
- snapshot restore logs

## POC D: ReDroid Inside Contrast

### Target

OpenMetal plus Edgeless Contrast.

### Purpose

Determine whether confidential Kubernetes can host Virtdroid runtimes.

### Entry Condition

POC A proves ReDroid on a full VM.

### Pass Criteria

- ReDroid or equivalent Android runtime can run in a Contrast workload.
- Required devices can be exposed safely.
- Persistent encrypted runtime state works.
- Workload secrets or Vault/OpenBao integration can supply lease-scoped material.
- Viewer traffic works without breaking the confidential boundary.
- Attestation policy can bind runtime image and workload manifest.

### Failure Criteria

- Kata/confidential pod restrictions prevent required devices.
- Required mounts or persistent volumes are blocked by policy.
- Network path breaks viewer latency.
- Workload secrets cannot be bound to the lease.

### Evidence To Collect

- Kubernetes RuntimeClass
- Contrast manifest
- pod spec
- attestation result
- workload secret delivery logs
- ReDroid boot logs
- viewer performance notes
- persistent volume/snapshot notes

## POC E: Aleph CVM Compatibility

### Target

Aleph confidential VM.

### Purpose

Evaluate whether decentralized CVMs can host Virtdroid runtimes and support client-verifiable attestation/key release.

### Pass Criteria

- Can run custom VM image or sufficiently customized guest.
- Can run Docker.
- Can run ReDroid.
- Can expose required kernel modules/devices.
- Can verify attestation from a Virtdroid client.
- Can bind key release to measured runtime image and user lease.
- Can expose viewer traffic with acceptable latency.
- Can persist and restore encrypted snapshots.

### Failure Criteria

- CVM is too constrained for binder/ReDroid.
- Attestation cannot be verified by the client.
- Key release cannot bind lease, node key, and image hash.
- Network model cannot support viewer traffic.

### Evidence To Collect

- CVM configuration
- guest/kernel control notes
- Docker/ReDroid logs
- attestation document
- client verifier result
- ingress/egress configuration
- viewer latency notes
- snapshot restore logs

## POC F: Marlin Oyster For Non-Runtime Services

### Target

Marlin Oyster.

### Purpose

Evaluate Oyster as a confidential service substrate, not as the first Android runtime host.

### Candidate Services

- confidential license service
- confidential billing metering
- confidential reputation oracle
- confidential policy evaluation
- confidential signing/attestation bridge

### Pass Criteria

- Service can run with measured image.
- Service can expose attestation evidence.
- Service can hold scoped signing or policy material.
- Service can publish auditable outputs.
- Service can be operated independently from the Virtdroid control plane.

### Failure Criteria

- Service cannot expose useful attestation to Virtdroid clients.
- Key material must be trusted to a single operator without attestation.
- Networking model does not support the service use case.

### Evidence To Collect

- service manifest
- attestation evidence
- signed output sample
- key-release or signing policy
- operator/deployment notes

## Minimum Test Runtime

Before testing full ReDroid, create a minimal runtime workload:

- `virtnoded-attest`
- static image digest
- node keypair
- lease parser
- canonical payload builder
- platform quote fetcher
- attestation endpoint
- dummy secret receiver

This workload should be small enough to deploy on dstack, Contrast, Aleph, and local Docker.

## Negative Tests

Every attestation/key-release POC must include negative tests.

Required negative cases:

- changed image hash
- changed node public key
- changed operator ID
- changed lease ID
- changed challenge nonce
- expired lease
- missing viewer public key
- unsupported capability set
- revoked node key
- stale/replayed quote

Expected result:

- client refuses to release secret
- failure reason is logged
- control plane cannot override the refusal

## Decision Gates

### Gate 1: Runtime Feasibility

Proceed only if POC A can boot Android, stream viewer, and restore encrypted snapshot.

### Gate 2: Attestation Protocol

Proceed only if POC B proves quote-bound key release with negative tests.

### Gate 3: Combined Runtime

Proceed only if at least one target can run ReDroid and bind key release to attestation.

### Gate 4: Operator Pilot

Proceed only after:

- node identity is per-node, not shared secret
- capability documents are signed
- leases are user-device signed
- client has explicit operator/trust-tier UI
- runtime keys are lease-scoped

## Output Format

Each POC should produce a short report with:

- target platform
- date
- exact instance/hardware type
- image/kernel versions
- commands run
- pass/fail table
- evidence artifacts
- blocker list
- recommendation

Recommended report path:

- `docs/poc/results/YYYY-MM-DD-<platform>-<poc>.md`

## Current Recommendation

Build POC A and POC B in parallel:

- POC A proves whether the real Android runtime can work in a confidential/full-VM host.
- POC B proves whether Virtdroid can enforce client-side attestation and lease-scoped key release.

Do not make the control plane the core decentralization target. Keep it weak. Runtime authority should come from user-device signatures, node/operator policy, attested leases, and lease-scoped key release.

