<!-- markdownlint-disable MD013 MD033 MD041 -->

<h1 align="center">Virtroid</h1>

<p align="center">
  <strong>Private, remotely hosted Android environments.</strong><br>
  Create, control, persist, reset, and destroy cloud Android runtimes from one Android client.
</p>

<p align="center">
  <img alt="Release candidate" src="https://img.shields.io/badge/status-release%20candidate-AFD135?style=for-the-badge&labelColor=18221B">
  <img alt="Android 9+" src="https://img.shields.io/badge/client-Android%209%2B-AFD135?style=for-the-badge&labelColor=18221B">
  <img alt="Go backend" src="https://img.shields.io/badge/backend-Go-F0F3EE?style=for-the-badge&labelColor=18221B">
  <img alt="ReDroid runtime" src="https://img.shields.io/badge/runtime-ReDroid-F0F3EE?style=for-the-badge&labelColor=18221B">
</p>

<img width="1788" height="944" alt="ezgif-8d1cadf478e6fb6d" src="https://github.com/user-attachments/assets/e9cace37-5f98-4384-a01a-dddc96e5b4d2" /> 

---

## What VirtRoid Does 


Virtroid runs Android separately from the user's physical phone. The phone is an authenticated controller and encrypted viewer; applications, storage,
network identity, and lifecycle state remain inside a remotely hosted ReDroid
runtime.  

<p align="left">
  <a href="#what-virtroid-does">Overview</a> ·
  <a href="#capabilities">Capabilities</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#security-boundary">Security</a> ·
  <a href="https://virtroid.network/demo/">Browser Emulated App Demo</a> ·
</p>

<br>

> [!IMPORTANT]
> Virtroid is currently a **trusted-operator release candidate**: one VPS, plus
> a pilot runtime node on Aleph Cloud. It does not yet protect an active runtime
> from a compromised host or privileged infrastructure operator, including the
> operator of a remote node's machine.


---


## Capabilities


| Area | Status | Included |
| :--- | :---: | :--- |
| Identity and recovery | ✅ Deployed | Invite-gated bootstrap, device signing, trusted-device management, account deletion, and replacement-phone recovery with live acceptance pending |
| Runtime lifecycle | ✅ Deployed | Create, start, stop, restore, persona restart, factory reset, idle cleanup, and permanent deletion |
| Remote viewer | ✅ Deployed | Encrypted live control, reconnect handling, runtime audio, demand-activated physical microphone input, and 30 fps runtimes (about 0.3 s tap-to-screen on the VPS) |
| Camera and media | ✅ Deployed | Explicit physical-camera photo and video capture followed by guest media import |
| Applications | ✅ Deployed | Pinned F-Droid catalog entries with hash, package, size, transport, and compatibility validation |
| Notifications | ✅ Deployed | Encrypted forwarding of package, app label, timestamp, and title only; no message body or preview |
| Client protection | ✅ Deployed | Android Keystore, encrypted local state, application lock, biometric unlock, and secure-window handling |
| Snapshot persistence | ✅ Deployed | Encrypted local snapshots, authenticated manifests, monotonic generations, rollback rejection, quotas, and cleanup tracking |
| Control plane | ✅ Deployed | Signed requests, scoped capabilities, replay protection, policy limits, readiness, lifecycle state, and audit data |
| Operator console | ✅ Deployed | Authenticated read-only command centre with sanitized fleet, security, hygiene, and release telemetry |
| Host monitoring | ✅ Deployed | Falco HIDS, Suricata NIDS, bounded sensor ingestion, and sanitized client security notices |
| Reproducible deployment | ✅ Deployed | Protected VPS-local builds, immutable release identity, schema checks, hardening checks, and health gates |
| Active-runtime file import | ✅ Backend path | Signed and bounded delivery is live-proved; the generic client upload action is intentionally absent |
| Multi-node scheduling | 🟡 Pilot | Region-based placement; a second node on Aleph Cloud serves explicitly assigned accounts |
| Direct node streaming | 🟡 Pilot | Phones stream straight from a remote node over key-pinned TLS (IPv6), falling back through the VPS edge when there is no direct route |
| Automatic node provisioning | ⛔ Not implemented | Next: create and delete Aleph VMs per user on demand |
| Confidential host isolation | ⛔ Not implemented | ReDroid runs inside the trusted VPS or an ordinary, non-confidential Aleph VM; SEV-SNP VMs are next |
| Hardware attestation | ⛔ Not implemented | Planned: the phone verifies a node's SEV-SNP attestation before connecting |


---


## Architecture

| Component | Responsibility |
| :--- | :--- |
| Android client | Identity, onboarding, runtime controls, local security, viewer, media capture, and notification display |
| HAProxy | Public HTTPS termination, the API, and viewer ingress for VPS runtimes and the fallback path |
| `virtroidd` | Accounts, devices, policy, region-based placement, runtimes, capabilities, sessions, operator telemetry, and notification delivery |
| `virtnoded` | ReDroid lifecycle, media paths, viewer relay, application provisioning, snapshots, and cleanup, on the VPS and on remote nodes |
| Remote node | A `virtnoded` host outside the VPS (today an Aleph Cloud VM) that serves a public TLS endpoint authenticated by its pinned key |
| PostgreSQL | Authoritative control-plane and lifecycle state |
| ReDroid | Independently hosted Android runtime |
| Runtime agent | Allowlisted notification metadata collection inside the guest | 

<br>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-deployment-dark.png">
  <img alt="Virtroid deployment today: the Android client talks to the HAProxy edge and virtroidd on the VPS; runtimes run on the VPS node or on a remote node on Aleph Cloud, which phones reach directly over pinned TLS with a fallback through the VPS" src="assets/architecture/virtroid-deployment-light.png">
</picture>

- **Control** always goes through the VPS: the phone's signed requests reach `virtroidd` through the edge, and nodes send heartbeats and fetch their work the same way.
- **Placement** follows the account's region. Accounts default to the VPS; an operator assigns an account to a remote region with `virtroid-admin set-account-region`. A runtime stays on the node that holds its data.
- **Streams** from VPS runtimes pass through the edge. Streams from a remote node go straight from the phone to the node, which proves its identity with a TLS key pinned in the session. When the phone has no direct route (for example no IPv6), the VPS node forwards the stream unchanged.
- On every path the viewer stream is encrypted between the phone and the viewer proxy inside the runtime, so the edge and a forwarding node only relay ciphertext. The runtime's own host can still see the runtime.

The diagram's source is [`assets/architecture/virtroid-deployment.archify.json`](assets/architecture/virtroid-deployment.archify.json).


---


## Security Boundary

Virtroid protects the client-to-service path and stopped-runtime persistence, but the runtime host remains trusted. 
A sufficiently privileged VPS administrator, compromised node agent, Docker controller, or host-level tool can inspect or alter a live Android runtime.  

The same applies to a remote node: whoever operates the machine under an Aleph Cloud VM can inspect the runtimes on it, because today's pilot node is an ordinary, non-confidential VM. Only explicitly assigned accounts are placed there.

### Implemented controls

- P-256 device, node, capability, and callback signing
- Timestamp, nonce, body-integrity, and replay validation
- Runtime- and session-scoped capabilities with expiry
- TLS viewer transport with session-bound relay credentials
- Remote nodes reached only at operator-allowlisted IPs, over TLS pinned to each node's reported key, for both control-plane calls and phone streams
- Android Keystore-backed client secrets and encrypted local state
- AES-GCM snapshot encryption with authenticated manifests and monotonic generations
- Bounded resource quotas and explicit lifecycle cleanup obligations
- Loopback-bound services, deny-by-default firewalling, AppArmor, Auditd, Fail2ban, and unattended security updates
- Falco and Suricata event collection with sanitized, account-scoped client delivery
- Protected, offline VPS release builds with immutable image and deployment-tree verification

<br>

> [!CAUTION]
> Virtroid must not currently be described as trustless, host-blind,
> operator-blind, anonymous by architecture, confidential computing, or fully
> end-to-end encrypted. Transport encryption protects data in transit and
> snapshot encryption protects stopped-runtime files; neither makes an active
> guest confidential from its host.


---


## Repository map

| Path | Purpose |
| :--- | :--- |
| [`android-client/`](android-client/) | Android controller app, runtime agent, scrcpy integration, tests, and signing gates |
| [`operator-console/`](operator-console/) | React operator interface and production UI bundle |
| [`backend/cmd/`](backend/cmd/) | Control plane, node agent, administration, viewer encryption, and sensor entry points |
| [`backend/internal/`](backend/internal/) | Identity, policy, persistence, lifecycle, security, and operator API logic |
| [`deploy/vps/`](deploy/vps/) | Reproducible deployment, hardening, HAProxy, Falco, Suricata, and release tooling |
| [`assets/`](assets/) | Architecture diagram images and their Archify source |
| [`third_party/`](third_party/) | Reviewable vendored source, provenance, and upstream notices | 


---


## Current status

The Android client, Go control plane, runtime node, ReDroid guests, PostgreSQL,
HAProxy edge, operator console, and host sensors are deployed on the active VPS.
Physical-device acceptance has covered runtime lifecycle, encrypted viewing and
reconnection, audio, demand-activated microphone input, physical-camera photo
import, file delivery, idle cleanup, readiness, and security-event delivery.

A second runtime node on an Aleph Cloud VM is live as a pilot for one assigned
account. Phones stream from it directly over IPv6; measured tap-to-screen was
about 0.42 s from South-East Asia to the node in Finland, against about 0.3 s
on the VPS.

This is working release-candidate evidence—not a claim of complete production
hardening or hostile multi-tenant isolation. 

### Next

1. A provisioner that creates and deletes Aleph VMs per user, with spending caps and a warm spare.
2. SEV-SNP confidential VMs, with the phone verifying attestation before it releases any key.
3. QUIC for the stream, and encrypted user data on Sia.

> [!NOTE]
> **Disclaimer:**
> Virtroid is under active development. Security properties, interfaces, schemas, and deployment procedures may change.
> Do not use it for high-risk or production-sensitive workloads without independently reviewing the source,
> deployed configuration, threat model, recovery design, runtime-host trust, and storage limitations.

---

<p align="center">
  <img src="android-client/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png" width="128" alt="Virtroid logo">
</p>
