<!-- markdownlint-disable MD013 MD033 MD041 -->

<p align="center">
  <img src="android-client/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png" width="96" alt="Virtroid logo">
</p>

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

<p align="center">
  <a href="#what-virtroid-does">Overview</a> ·
  <a href="#capabilities">Capabilities</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#security-boundary">Security</a> ·
  <a href="#development">Development</a> ·
  <a href="https://virtroid.network/operator/">Operator console</a>
</p>

---

## What Virtroid does

Virtroid runs Android separately from the user's physical phone. The phone is
an authenticated controller and encrypted viewer; applications, storage,
network identity, and lifecycle state remain inside a remotely hosted ReDroid
runtime.

<table>
  <tr>
    <td width="33%" valign="top">
      <strong>Remote by design</strong><br><br>
      Android workloads run on server infrastructure, not inside the controller phone.
    </td>
    <td width="33%" valign="top">
      <strong>Explicit lifecycle</strong><br><br>
      Create, start, stop, restore, restart, reset, or permanently delete each runtime.
    </td>
    <td width="33%" valign="top">
      <strong>Security focused</strong><br><br>
      Device-signed requests, scoped capabilities, encrypted sessions, and authenticated snapshots.
    </td>
  </tr>
</table>

> [!IMPORTANT]
> Virtroid is currently a **trusted-operator, single-VPS release candidate**.
> It does not yet protect an active runtime from a compromised host or
> privileged infrastructure operator.

## Current status

The Android client, Go control plane, runtime node, ReDroid guests, PostgreSQL,
HAProxy edge, operator console, and host sensors are deployed on the active VPS.
Physical-device acceptance has covered runtime lifecycle, encrypted viewing and
reconnection, audio, demand-activated microphone input, physical-camera photo
import, file delivery, idle cleanup, readiness, and security-event delivery.

This is working release-candidate evidence—not a claim of complete production
hardening or hostile multi-tenant isolation.

### Runtime lifecycle

```mermaid
flowchart LR
    A[Create identity] --> B[Create runtime]
    B --> C[Start and connect]
    C --> D[Stop and save]
    D --> E{Next action}
    E -->|Restore| C
    E -->|New persona| B
    E -->|Factory reset| B
    E -->|Delete| F[Remove resources]
```

## Capabilities

| Area | Status | Included |
| :--- | :---: | :--- |
| Identity and recovery | ✅ Deployed | Invite-gated bootstrap, device signing, trusted-device management, account deletion, and replacement-phone recovery with live acceptance pending |
| Runtime lifecycle | ✅ Deployed | Create, start, stop, restore, persona restart, factory reset, idle cleanup, and permanent deletion |
| Remote viewer | ✅ Deployed | Encrypted live control, reconnect handling, runtime audio, and demand-activated physical microphone input |
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
| Multi-node scheduling | 🟡 Candidate | Capability-aware control logic exists; live multi-node acceptance remains pending |
| Confidential host isolation | ⛔ Not implemented | ReDroid currently executes inside the trusted VPS boundary |
| Hardware attestation | ⛔ Not implemented | Design and proof-of-concept work only |

> [!NOTE]
> Camera support is deliberate photo/video capture and media import. It is not
> a live physical-camera device injected into Android's camera HAL.

## Architecture

```mermaid
flowchart LR
    subgraph Phone[Android client]
        ID[Device identity]
        UI[Runtime controls]
        VIEW[Encrypted viewer]
    end

    EDGE[HAProxy<br/>HTTPS and TLS relay]
    CP[virtroidd<br/>Control plane]
    NODE[virtnoded<br/>Runtime node]
    DB[(PostgreSQL)]
    SNAP[(Encrypted snapshots)]

    subgraph Guest[ReDroid guest]
        ANDROID[Android system and apps]
        AGENT[Notification agent]
    end

    ID --> EDGE
    UI --> EDGE
    VIEW --> EDGE
    EDGE --> CP
    EDGE --> NODE
    CP --> DB
    CP --> NODE
    NODE --> ANDROID
    NODE --> SNAP
    ANDROID --> AGENT
    AGENT -->|Metadata only| EDGE
```

| Component | Responsibility |
| :--- | :--- |
| Android client | Identity, onboarding, runtime controls, local security, viewer, media capture, and notification display |
| HAProxy | Public HTTPS termination and controlled viewer ingress |
| `virtroidd` | Accounts, devices, policy, runtimes, capabilities, sessions, operator telemetry, and notification delivery |
| `virtnoded` | ReDroid lifecycle, media paths, viewer relay, application provisioning, snapshots, and cleanup |
| PostgreSQL | Authoritative control-plane and lifecycle state |
| ReDroid | Independently hosted Android runtime |
| Runtime agent | Allowlisted notification metadata collection inside the guest |

## Security boundary

Virtroid protects the client-to-service path and stopped-runtime persistence,
but the runtime host remains trusted. A sufficiently privileged VPS
administrator, compromised node agent, Docker controller, or host-level tool
can inspect or alter a live Android runtime.

### Implemented controls

- P-256 device, node, capability, and callback signing
- Timestamp, nonce, body-integrity, and replay validation
- Runtime- and session-scoped capabilities with expiry
- TLS viewer transport with session-bound relay credentials
- Android Keystore-backed client secrets and encrypted local state
- AES-GCM snapshot encryption with authenticated manifests and monotonic generations
- Bounded resource quotas and explicit lifecycle cleanup obligations
- Loopback-bound services, deny-by-default firewalling, AppArmor, Auditd, Fail2ban, and unattended security updates
- Falco and Suricata event collection with sanitized, account-scoped client delivery
- Protected, offline VPS release builds with immutable image and deployment-tree verification

> [!CAUTION]
> Virtroid must not currently be described as trustless, host-blind,
> operator-blind, anonymous by architecture, confidential computing, or fully
> end-to-end encrypted. Transport encryption protects data in transit and
> snapshot encryption protects stopped-runtime files; neither makes an active
> guest confidential from its host.

## Repository map

| Path | Purpose |
| :--- | :--- |
| [`android-client/`](android-client/) | Android controller app, runtime agent, scrcpy integration, tests, and signing gates |
| [`operator-console/`](operator-console/) | React operator interface and production UI bundle |
| [`backend/cmd/`](backend/cmd/) | Control plane, node agent, administration, viewer encryption, and sensor entry points |
| [`backend/internal/`](backend/internal/) | Identity, policy, persistence, lifecycle, security, and operator API logic |
| [`deploy/vps/`](deploy/vps/) | Reproducible deployment, hardening, HAProxy, Falco, Suricata, and release tooling |
| [`docs/android-client-ui-elements.md`](docs/android-client-ui-elements.md) | Screen-by-screen Android UI inventory |
| [`third_party/`](third_party/) | Reviewable vendored source, provenance, and upstream notices |

## Development

### Requirements

- Go 1.26.6
- Node.js 24 and npm
- JDK 17
- Android SDK 36
- Docker for backend images and integration environments

### Backend

```bash
cd backend
go test ./...
go vet ./...
go build ./cmd/...
```

### Operator console

```bash
npm --prefix operator-console ci
npm --prefix operator-console run typecheck
npm --prefix operator-console run build
```

Run the local UI preview with:

```bash
npm --prefix operator-console run dev
```

### Android client

```bash
cd android-client
./gradlew --no-daemon \
  :app:testDebugUnitTest \
  :app:lintDebug \
  :app:assembleDebug \
  :runtime-agent:testDebugUnitTest
```

Release signing is fail-closed and requires the approved local signing
configuration. Generated APKs, keys, production secrets, database exports, and
VPS release bundles must not be committed.

### Production deployment

Production releases are built and applied from a root-owned, offline source
ledger on the VPS. GitHub is not a release runner and has no production
credentials. See [`deploy/vps/README.md`](deploy/vps/README.md) for the guarded
release process and operational checks.

## Roadmap

- Live multi-node scheduling and acceptance
- Searchable, durable trace storage and reporting
- Broader lifecycle, cleanup, and storage fault injection
- Stronger runtime isolation beyond the trusted ReDroid/VPS boundary
- Hardware-backed attestation research

## Disclaimer

Virtroid is under active development. Security properties, interfaces, schemas,
and deployment procedures may change. Do not use it for high-risk or
production-sensitive workloads without independently reviewing the source,
deployed configuration, threat model, recovery design, runtime-host trust, and
storage limitations.

---

<p align="center">
  <strong>Virtroid</strong><br>
  Remote Android environments with explicit lifecycle control.
</p>
