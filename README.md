<h1 align="center">Virtroid</h1>

<p align="center">
  <strong>Your Android apps, running remotely. Your phone, in control.</strong>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Android-9%2B-3DDC84?style=flat-square&logo=android&logoColor=white" alt="Android 9 or later">
  <img src="https://img.shields.io/badge/Compute-AMD%20SEV--SNP-111827?style=flat-square" alt="AMD SEV-SNP confidential computing">
  <img src="https://img.shields.io/badge/Storage-Sia-00C58E?style=flat-square" alt="Encrypted storage on Sia">
  <img src="https://img.shields.io/badge/Status-Active%20development-F59E0B?style=flat-square" alt="Under active development">
</p>

<p align="center">
  <a href="#why-virtroid">Why Virtroid</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#get-started">Get started</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#security">Security</a> ·
  <a href="#latency">Latency</a> ·
  <a href="#progress">Progress</a> ·
  <a href="#building-from-source">Building</a>
</p>

---

Virtroid runs Android apps in a remote confidential virtual machine. Your phone verifies the VM’s hardware attestation, displays its encrypted video stream, and sends your touches and keystrokes.

Your apps, accounts, messages and files live in the remote Android environment. When it stops, its data is encrypted inside the VM and saved across independent Sia storage hosts. The VM can then be deleted without losing your saved environment.

A **runtime** is your saved Android environment. A **VM** is the machine that runs it.

> [!IMPORTANT]
> **Virtroid is under active development.** The current deployment runs in Europe, streams over TLS over TCP, and uses ReDroid 14. Regional expansion, a custom AOSP image and several security improvements remain open. See [Progress](#progress).

## Why Virtroid

| Benefit | How it works |
| :--- | :--- |
| **Keep runtime app storage off your phone** | Apps and their saved data stay in the remote runtime. The handset holds the client, its keys and local client state. |
| **Protect running data** | AMD SEV-SNP encrypts VM memory. The phone checks the VM’s attestation before opening the viewer. |
| **Distribute encrypted storage** | Snapshots are encrypted before leaving the VM, then stored with 10-of-30 erasure coding on Sia. |
| **Stay reachable after closing the viewer** | Keep a runtime running for **30 minutes to 1 week**, with encrypted notification metadata delivered to your phone. |
| **Create an identity without contact details** | Accounts use a key pair and an identity password. No email address, phone number or name is required. |
| **Recover on another phone** | Restore access with your account ID and identity password, then revoke a lost device. |
| **Choose apps from F-Droid** | Browse a signed catalogue, with APKs pinned by SHA-256, or install more through F-Droid inside the runtime. |
| **Pay for usage** | Fund your balance with USDT on TRON. Running time and stored data are charged separately. |

Each start presents a fresh device persona. Apps use the runtime’s datacenter network connection, and VMs are dedicated to an account rather than shared between accounts.

## How it works

```mermaid
flowchart LR
    A["Create runtime"] --> B["Provision VM<br/>Restore saved data"]
    B --> C["Verify attestation<br/>Open encrypted viewer"]
    C --> D["Close viewer<br/>Keep running for chosen period"]
    D --> C
    D --> E["Stop Android<br/>Encrypt and save to Sia"]
    C -->|"Stop and save now"| E
    E --> F["Retire and delete VM"]
    F -->|"Next start"| B
```

1. **Create.** Choose a name, a keep-running period and any F-Droid apps to pre-install.
2. **Start.** Virtroid provisions or reuses an account-dedicated VM. Saved runtime data is restored and decrypted inside it.
3. **Verify.** The phone checks AMD’s attestation signature, the approved image measurement and the VM’s bound TLS key.
4. **Use.** Stream the display and send input through an encrypted session.
5. **Close.** Android keeps running for your chosen period. Apps continue receiving messages, and notification metadata reaches your phone.
6. **Save.** When the period expires—or you choose **Stop and save now**—Android shuts down and its data is encrypted and saved to Sia.
7. **Delete the VM.** Saved runtime state survives VM replacement. A later session restores it into the next VM.

<details>
<summary><strong>How snapshots are encrypted and restored</strong></summary>

A snapshot contains the runtime’s whole `/data`: installed apps, app data, accounts and files.

| Stage | Protection |
| :--- | :--- |
| **Encrypt** | Inside the VM, data is compressed and encrypted with AES-256-CTR and HMAC-SHA256 using encrypt-then-MAC. |
| **Derive keys** | Snapshot keys derive from a per-runtime key under the account key, with a fresh salt for each snapshot. |
| **Authenticate** | An authenticated manifest records 4 MiB chunks, their hashes and the snapshot generation. |
| **Distribute** | Sia stores each chunk as 30 shards across independent hosts. Any 10 reconstruct the encrypted chunk. |
| **Restore** | The VM checks authentication tags and chunk integrity before decrypting the snapshot into Android. |

The indexer sees opaque object names and sizes rather than plaintext runtime contents.

The phone remembers the newest snapshot generation it has seen. Stronger rollback protection that does not depend on the server’s generation floor remains open.

</details>

## Get started

### 1. Create your identity

1. Install the Virtroid client on an **Android 9 or later** phone.
2. Create an identity and choose an **identity password of at least 14 characters**.
3. Save your **account ID** and identity password securely. Together, they recover your account on another phone.
4. In **Privacy & Security**, enable the app lock with an **8-digit PIN** and optional biometric unlock.

The app lock places local client state behind an encrypted vault.

### 2. Create a runtime

Tap **+** and configure:

| Setting | What to choose |
| :--- | :--- |
| **Session name** | A name for your Android environment |
| **Keep running** | Between 30 minutes and 1 week after closing the viewer |
| **Pre-install apps** | Up to 32 apps from the F-Droid catalogue |
| **Audio and camera** | Your preferred audio passthrough and camera-access settings |

### 3. Start and use it

Tap **Start Session**. Virtroid prepares the VM, verifies its attestation and opens the viewer. A first start can take a minute or two while the VM is prepared.

Inside the viewer, you can:

- Touch and type as you would on a phone.
- Play audio and use the microphone when a runtime app requests it.
- Import a photo from your phone’s camera or a file into the runtime.
- Close the viewer while keeping Android running.

Notifications contain the **app name, time and title**, with no message body. They are encrypted inside the VM for your linked phones.

### Manage a runtime

| Control | Action |
| :--- | :--- |
| **Apps** | Change the runtime’s selected F-Droid apps |
| **Keep running when closed** | Adjust the period and view the remaining countdown |
| **Stop and save now** | Shut down Android and save its data |
| **Restart with New Persona** | Restart with a fresh device persona |
| **Factory Reset Runtime** | Clear the runtime’s saved data |
| **Wipe & Delete Runtime** | Remove the runtime and its saved data |
| **Display & Output** | Adjust viewer settings |
| **Console Logs / Build Identity** | Inspect runtime diagnostics and build information |

<details>
<summary><strong>Privacy settings and account recovery</strong></summary>

In **Privacy & Security**:

- **Block Screen Capture** hides the viewer from screenshots and the recent-apps preview.
- **Close Viewer When Idle** closes the viewer after inactivity; the runtime continues running.
- **Auto-clear Clipboard** empties the phone’s clipboard when a session ends.
- **Require unlock on app resume** and **Auto-lock Timer** control when the app requests its PIN.

In **Account Identity**:

- **Linked devices** lists the phones connected to your account. Revoke devices you no longer have.
- On a replacement phone, choose **Recover Account** and enter your account ID and identity password. The new phone creates its own signing key.
- Changing your identity password preserves the account key, so existing runtimes and snapshots remain accessible.

</details>

### Pricing

*Rates recorded in the October 2026 project update.*

| Resource | Rate |
| :--- | ---: |
| Runtime compute | **0.25 USDT per running hour** |
| Snapshot storage | **0.05 USDT per GB-month** |

New accounts receive **one runtime and one hour of running time free**.

**Wallet** shows your account’s USDT deposit address on **TRON / TRC20** and your available balance.

## Architecture

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-target-dark.png">
  <img alt="Virtroid architecture: the phone sends a signed start through the edge; virtroidd checks credit and the provisioner has Aleph Cloud boot the account's own SEV-SNP VM from a measured image, releasing its disk key after attestation; the phone verifies the attestation, then streams from the VM; user data is restored from and saved to independent Sia hosts through the indexer; notifications are relayed to the phone" src="assets/architecture/virtroid-target-light.png">
</picture>

The viewer connects directly to the VM when possible. When it needs the edge fallback, the relay carries encrypted traffic.

Android runs in a container **inside the confidential VM**. Each VM belongs to one account and is not shared with other accounts.

<details>
<summary><strong>Components and responsibilities</strong></summary>

| Component | Location | Responsibility |
| :--- | :--- | :--- |
| **Android client** | Phone | Identity, wallet, runtime controls, attestation, encrypted viewer, media import and notifications |
| **HAProxy edge** | Control plane | Public API entry point, viewer fallback and indexer application API |
| **`virtroidd`** | Control plane | Accounts, devices, policy, billing, runtime state, sessions, catalogue and notification delivery |
| **PostgreSQL** | Control plane | Authoritative account, billing and lifecycle state |
| **Provisioner** | Control plane | VM creation and deletion, placement, and secret release after attestation |
| **Payments service** | Control plane | Deposit verification, wallet custody and treasury operations; conversion currently runs in dry-run mode |
| **`indexd`** | Control plane | Account-scoped Sia access, storage contracts and encrypted object management |
| **Falco / Suricata** | Control plane | Host and network monitoring; off-host append-only event storage remains planned |
| **Runtime VM** | Aleph Cloud | Measured Linux guest containing Android and runtime services |
| **`virtnoded`** | Runtime VM | Android lifecycle, viewer, app installation, snapshots and cleanup |
| **Android runtime** | Runtime VM | ReDroid 14, F-Droid and the Virtroid notification agent |
| **Sia hosts** | Independent operators | Erasure-coded encrypted snapshot storage |

</details>

<details>
<summary><strong>The VM image, viewer and app catalogue</strong></summary>

**VM image**

- Built with Nix, with a read-only root verified by `dm-verity`.
- Root integrity is tied to the measured boot chain.
- Includes Android’s Binder driver and refuses kernel module loading.
- Uses an encrypted writable data disk.
- Rebuilds the Docker store from the measured image at boot.
- Loads user data from encrypted snapshots rather than embedding it in the image.

Phones accept image measurements from a list signed with Virtroid’s offline release key.

**Viewer**

The VM captures Android’s display, encodes it as H.264 and encrypts it for the phone’s session. Touches, keys, clipboard traffic, audio and supported media operations use the encrypted session.

Capture runs as Android’s system user, allowing protected app screens to appear in the remote viewer. Screenshot blocking on the handset is controlled through **Block Screen Capture**.

**Apps**

The catalogue follows F-Droid’s signed index hourly and pins APKs by SHA-256. Search and browsing are paginated.

Selected apps are installed once. An app removed inside Android stays uninstalled until selected again. F-Droid inside the runtime can install additional apps.

</details>

## Security

### What is protected

| Layer | Protection |
| :--- | :--- |
| **Runtime storage** | App data stays in the remote environment; saved snapshots are encrypted before leaving the VM. |
| **Running memory** | AMD SEV-SNP provides hardware memory encryption, subject to its firmware and security assumptions. |
| **Viewer traffic** | Encryption terminates inside the attested VM. The edge fallback carries ciphertext. |
| **Control requests** | Signed requests include timestamp, nonce, body-integrity and replay checks. |
| **VM identity** | The phone verifies attestation and checks the measurement against the offline-signed image list. |
| **Notifications** | App, time and title are encrypted for linked phones; message bodies are excluded. |
| **Local client state** | Device signing keys use Android Keystore. Enabling the app lock encrypts local state behind the PIN or biometric lock. |

### What you still trust

- **The approved image and its signing process.** Reproducible builds support independent comparison; someone must still review and approve what runs.
- **AMD SEV-SNP and host firmware.** Confidential computing remains subject to implementation flaws and relevant side channels.
- **The provisioner.** It holds VM data-disk keys and releases them after attestation.
- **The control plane.** It manages placement and sees account, runtime, timing and connection metadata.
- **Your phone while in use.** Malware on an unlocked handset may observe the viewer or input.
- **Your identity password.** The wrapped account key uses PBKDF2-HMAC-SHA256 with 600,000 iterations. A weak password remains vulnerable to offline guessing.

> [!NOTE]
> **Virtroid is not an anonymity network.** The control plane and runtime VM can see the phone’s connecting IP address, and USDT deposits are public on TRON. A fresh device persona also does not prevent linkage through persistent app accounts or saved app data.

<details>
<summary><strong>Implemented security controls</strong></summary>

- P-256 signing for devices, nodes, capabilities and callbacks.
- Timestamp, nonce, body-integrity and replay validation.
- Expiring capabilities scoped to runtimes and sessions.
- Operator-allowlisted node addresses and pinned TLS.
- Attestation before VM secret release and before viewer access.
- Offline-signed approved image measurements.
- Runtime-scoped snapshot keys sealed to an attested VM.
- Viewer access requiring proof of possession of the runtime key.
- Photos and files sealed for the destination VM.
- Authenticated snapshot encryption, manifests and generation tracking.
- Recovery-key signatures for identity password changes.
- A separate notification relay key restricted to notification delivery while the phone is locked.
- F-Droid index verification and SHA-256-pinned APKs.
- Isolated payment signing and wallet-seed custody.
- Resource quotas and lifecycle cleanup obligations.
- Loopback-bound services, default-deny firewalling, AppArmor, Auditd, Fail2ban and unattended security updates.
- Falco and Suricata monitoring.
- Protected release builds, image and deployment-tree verification, and CI checks rejecting committed binaries.

Remaining work includes signed image-list expiry, stronger rollback protection, migration of older device keys and finer attestation TCB checks on the phone.

</details>

## Latency

Distance to the runtime is the main limit on responsiveness: your touch must reach the VM, and the updated screen must travel back.

| Route | Approximate round trip | Availability |
| :--- | ---: | :--- |
| Bangkok → Europe | **230 ms** | Current deployment |
| Bangkok → Singapore | **30–50 ms** | Projected regional option; not deployed |

Encoding and decoding add roughly **30–60 ms at 30 fps**. CPU rendering can also reduce frame rates in heavy apps and animations because the current confidential VMs have no GPU.

The current stream uses **TLS over TCP**, with:

- BBR congestion control.
- Small buffers and bounded queues.
- Direct reads from the viewer socket.
- Adaptive bitrate that reduces picture quality when the connection falls behind.
- A session-bar indicator when the displayed picture is delayed.

These measures reduce queue buildup. A closer region is still needed to reduce geographic round-trip latency. QUIC streaming is not currently planned.

## Progress

**Project update: 15 October 2026**

### Working

| Area | Status | Notes |
| :--- | :---: | :--- |
| Identity and recovery | ✅ Live | Account creation, linked devices, password changes, deletion and recovery tested on a real phone |
| Confidential VMs | ✅ Live | Account-dedicated Aleph Cloud SEV-SNP VMs in Europe using measured image 13 |
| Attestation | ✅ Live | Phone verification against AMD’s root and the offline-signed image list |
| Sia storage and restore | ✅ Live | Mainnet storage, encrypted snapshots and restoration after VM replacement |
| Runtime lifecycle | ✅ Live | Create, start, stop, restore, new persona, reset, deletion and lost-snapshot handling |
| Keep-running controls | ✅ Live | 30 minutes to 1 week, countdown and immediate stop-and-save |
| Notifications | ✅ Live | Encrypted inside the VM and delivered while the phone is locked |
| App catalogue | ✅ Live | Approximately 4,400 F-Droid apps, signed-index verification and per-runtime selection |
| Viewer and media | ✅ Live | Encrypted control, audio, microphone, camera import and direct streaming with edge fallback |
| Confidential handoffs | ✅ Live | Runtime-key viewer authorization, attested key delivery and sealed media imports |
| Streaming improvements | ✅ Live | Image 13 and app 0.4.3: BBR, bounded queues and adaptive bitrate |
| Wallet and billing | ✅ Live | USDT deposits, trial allowance and usage charging |
| Observatory | ✅ Live | Fleet, VM, payments, storage, security and release telemetry |
| Control-plane hardening | ✅ Live | Firewalling, AppArmor, Falco, Suricata and protected releases |
| Database backups | ✅ Live | Nightly sealed control-plane and indexer backups stored on Aleph; recovery uses the indexer recovery phrase |

<details>
<summary><strong>Observed performance examples</strong></summary>

These are recorded test results, not timing guarantees.

- **VM preparation:** a new VM passed attestation and unlocked in **30–90 seconds** once its compute node had the image.
- **Snapshot save:** approximately **590 MB** of runtime data saved to Sia in **30 seconds**.
- **Restore after VM deletion:** Android was running **27 seconds after the start began**; the viewer opened approximately **1 minute 40 seconds after VM creation**.

The measurements use different starting points and should not be treated as one combined startup benchmark.

</details>

### Remaining work

| Area | Status | Next step |
| :--- | :---: | :--- |
| Security review follow-up | 🟡 Partial | Image-list expiry, independent rollback protection, older device-key migration and finer phone TCB checks |
| South-East Asia region | ⏳ Not deployed | Add regional SEV-SNP capacity to reduce latency |
| Custom AOSP image | ⏳ Not built | Replace the current stock ReDroid 14 base |
| Tetragon inside runtime VMs | ⏳ Not built | Add runtime process monitoring and enforcement |
| Deposit conversion | 🧪 Dry run | Enable live treasury sweeps and swaps; storage and compute credits are currently topped up manually |
| Off-host append-only sensor logs | ⏳ Not built | Move sensor events beyond the control-plane host |
| QUIC streaming | — Not currently planned | Revisit alongside reconnect and network-switching requirements |

> [!WARNING]
> Virtroid’s security properties, interfaces and deployment procedures may change. Before using it for high-risk or production-sensitive workloads, independently review the source, deployed configuration, threat model, recovery design, storage limitations and open security work.

## Building from source

### Requirements

| Component | Requirements |
| :--- | :--- |
| Backend | Go 1.27; PostgreSQL 18 for integration tests |
| Android client | JDK 17 and Android SDK, compile SDK 36 |
| Operator console | Node.js and npm |
| Runtime VM image | Nix on Linux with `/dev/kvm` |

Commands below start from the repository root unless stated otherwise.

### Backend

```sh
cd backend
go build ./cmd/...
go test -p 1 ./...
```

Database tests run when `VIRTROID_TEST_DATABASE_URL` points to an empty PostgreSQL database; otherwise, they are skipped.

Keep **`-p 1`** because the test packages share the database schema.

<details>
<summary><strong>Run a local development control plane</strong></summary>

With PostgreSQL running and the development database available:

```sh
cd backend

APP_ENV=development \
BIND_ADDR=127.0.0.1:8080 \
BOOTSTRAP_REQUIRE_INVITE=false \
PUBLIC_BASE_URL=http://10.0.2.2:8080 \
APP_CATALOG_SYNC_ENABLED=false \
DATABASE_URL='postgres://virtroid:virtroid@127.0.0.1:5432/virtroid?sslmode=disable' \
go run ./cmd/virtroidd
```

From the Android emulator, use `http://10.0.2.2:8080` as the debug control-plane URL.

</details>

### Android client

```sh
cd android-client
./gradlew :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
```

Debug builds use the production control plane by default. To build for the local emulator setup:

```sh
VIRTROID_DEBUG_CONTROL_PLANE_URL=http://10.0.2.2:8080 \
./gradlew :app:assembleDebug
```

Release builds require the maintainers’ release signing key. Gradle rejects releases without that key, an HTTPS control plane or a manifest that passes the release security checks.

### Operator console

```sh
cd operator-console
npm ci
npm run build
```

### Deployment guides

| Guide | Covers |
| :--- | :--- |
| [Runtime VM image](deploy/aleph/README.md) | Building, measuring, publishing and approving the confidential image |
| [Control plane](deploy/vps/README.md) | Deployment, hardening and releases |
| [Sia indexer](deploy/vps/indexd.md) | Storage configuration and operations |
| [Payments](deploy/vps/payments.md) | Deposit and treasury operations |
| [Screen-capture source](third_party/scrcpy-server/) | Vendored source and provenance |
| [Backend third-party material](backend/third_party/) | Supporting source and rebuild information |

<details>
<summary><strong>Repository map</strong></summary>

| Path | Purpose |
| :--- | :--- |
| `android-client/` | Android client, runtime agent, viewer integration, tests and signing gates |
| `operator-console/` | The Observatory operator interface |
| `backend/cmd/` | Service, administration, signing and sensor entry points |
| `backend/internal/` | Identity, policy, billing, persistence, lifecycle, catalogue, storage and security logic |
| `deploy/vps/` | Control-plane deployment, hardening, monitoring and release tooling |
| `deploy/aleph/` | Confidential VM image, kernel and provisioning guide |
| `deploy/provisioner/` | Provisioner image and Aleph CLI |
| `assets/` | Architecture diagrams and editable sources |
| `third_party/` | Vendored source, provenance and upstream notices |
| `.github/workflows/` | Backend, Android, deployment and code-scanning checks |

</details>

---

<p align="center">
  <img src="android-client/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png" width="128" alt="Virtroid logo">
</p>
