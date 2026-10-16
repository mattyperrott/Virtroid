<!-- markdownlint-disable MD013 MD033 MD041 -->

<h1 align="center">Virtroid</h1>

<p align="center">
  <strong>Your Android, kept off your phone.</strong><br>
  Private Android runtimes in confidential cloud VMs, controlled from a phone that holds none of your data.
</p>

<p align="center">
  <img alt="Release candidate" src="https://img.shields.io/badge/status-release%20candidate-AFD135?style=for-the-badge&labelColor=18221B">
  <img alt="Android 9+" src="https://img.shields.io/badge/client-Android%209%2B-AFD135?style=for-the-badge&labelColor=18221B">
  <img alt="AMD SEV-SNP" src="https://img.shields.io/badge/runtime-AMD%20SEV--SNP-F0F3EE?style=for-the-badge&labelColor=18221B">
  <img alt="Sia storage" src="https://img.shields.io/badge/storage-Sia-F0F3EE?style=for-the-badge&labelColor=18221B">
</p>

<img width="1788" height="944" alt="ezgif-8d1cadf478e6fb6d" src="https://github.com/user-attachments/assets/e9cace37-5f98-4384-a01a-dddc96e5b4d2" />

---

## What Virtroid does

Virtroid runs your Android apps on a remote Android runtime instead of on your
phone. Each runtime lives in its own confidential virtual machine (AMD SEV-SNP)
that the cloud provider cannot look inside. Your phone is only a controller and
an encrypted screen: it checks the VM's hardware attestation, streams its
display, and sends your touches. Your apps, accounts, messages and files stay
in the runtime. When you stop using it, they are encrypted, split into shards
and stored across independent Sia storage hosts, and the VM is destroyed.

<p align="left">
  <a href="#why-virtroid">Why</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#using-virtroid">Using Virtroid</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#security-model">Security</a> ·
  <a href="#latency">Latency</a> ·
  <a href="#progress">Progress</a> ·
  <a href="#building-from-source">Building</a> ·
</p>

[Contributing](CONTRIBUTING.md) · [Code of conduct](CODE_OF_CONDUCT.md) ·
[Security](SECURITY.md) · [Accessibility](ACCESSIBILITY.md) ·
[License and commercial use](LICENSING.md)

---

## Why Virtroid

### Your phone holds none of your data

Apps, messages, photos, accounts and files never land on the handset. A
forensic extraction device (the kind used at borders, by police, or by a thief
with the right tools) that images your phone finds the Virtroid client and its
keys, but none of your runtime data, because it was never there.

The keys don't help either:

- **The device key** lives in Android's hardware-backed Keystore, cannot be exported, and signs only while the phone is unlocked.
- **The account key** that decrypts your data is wrapped by your identity password, which is never stored on the phone or sent to the service.
- **The app's own logs, settings and session handles** sit in an encrypted vault behind its PIN or biometric lock.
- **Notifications,** if you allow them, show only an app's name, the time and the title, never the message itself.

### Sessions are ephemeral

A runtime runs only while you need it:

- **Close the viewer** and it keeps running for the period you chose, from 30 minutes to a week, so messages still arrive and you're notified.
- **When that period ends,** Android shuts down. Its data is encrypted inside the VM, saved to Sia, and the plaintext is removed.
- **The VM is then destroyed,** with its encrypted disk.

None of your data stays at rest on the machine that ran it. Your next session
starts on a fresh VM and restores your data into it.

### No single place holds your data

At rest, your data exists only on the Sia network:

1. **Before it leaves the VM,** it is encrypted with a key derived from your account key, and a manifest authenticates every chunk and the snapshot's generation.
2. **Sia then splits it into 30 shards** and places them with 30 independent storage hosts, run by separate operators around the world. Any 10 of them rebuild it, so losing a host loses nothing.
3. **Each host holds only an encrypted fragment** that means nothing on its own.

To even attempt to read your data, an adversary would first need at least 10
of those shards from 10 separate hosts. Even then they'd hold only ciphertext:
decrypting it needs your account key, and that needs your identity password.
Your data is separated from your handset and from any single server.

### The cloud provider cannot look inside

Each runtime VM is an AMD SEV-SNP guest:

- **Encrypted memory.** The processor encrypts the VM's memory with a key the host never sees. The hosting provider and its staff cannot read your running apps, even with full control of the server.
- **A measured image.** The VM boots a measured, read-only image whose integrity is checked block by block.
- **A locked data disk.** The VM's data disk is encrypted with a key that is released only after the VM proves which image it is running.

### Verify before you connect

Before your phone shows a single frame, it checks the VM's attestation report:

- **Signed by AMD's hardware,** the report proves the exact image the VM booted.
- **It also binds the VM's TLS key,** so the stream cannot be intercepted on the way.

The phone accepts only images on a list signed with Virtroid's offline release
key, whose public half is built into the app. Images are built reproducibly, so
anyone can rebuild one and compare its measurement.

### Stay reachable without carrying your apps

A closed runtime keeps running for the period you pick, and you can change that
period at any time from the runtime's settings, where a countdown shows when it
will stop. Meanwhile messaging apps keep receiving. The runtime forwards each
notification's app, time and title to your phone, encrypted end to end and
never with the message body, so you know when to open the viewer.

### Separate device, network and identity

- **A fresh device persona on every start.** Each start presents a new device model and fingerprint, so apps can't link one session to the next.
- **Network.** Apps see the VM's datacenter IP address, never your phone's IP, carrier or location.
- **Isolation.** Every runtime is its own VM, so a work profile, a personal one and a throwaway one share nothing.

### No Google, no personal details

- **No Google services.** Runtimes run a debloated Android (AOSP) build without them.
- **Apps from F-Droid.** The catalogue follows F-Droid's signed index, every app is pinned by its SHA-256, and F-Droid on the runtime can install more.
- **No personal details.** An account is a key pair and an identity password: no email, phone number or name.
- **Pay as you go.** You pay in USDT, per hour a runtime runs and per GB stored.

### Lose your phone, not your runtimes

Your runtimes belong to your account, not your handset. On a new phone, recover
with your account ID and identity password, and revoke the lost phone from any
linked one.

---

## How it works

### A runtime's life

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-lifecycle-dark.png">
  <img alt="A runtime's life: created; starting on a fresh confidential VM, attested, with its data restored from Sia; running with the viewer open; running with the viewer closed for the chosen period while notifications are relayed; then stopping, with data encrypted and sharded across Sia hosts; stopped, with the VM destroyed; and starting again on a new VM. A wipe clears the saved data and a delete removes the runtime." src="assets/architecture/virtroid-lifecycle-light.png">
</picture>

1. **Create.** Choose a name, how long the runtime keeps running when closed, and any F-Droid apps to pre-install.
2. **Start.** The runtime gets its own fresh confidential VM and a fresh device persona. Its data is restored from Sia and decrypted inside the VM before Android boots. Selected apps are installed once.
3. **Use.** The phone attests the VM, then streams its screen over an end-to-end encrypted channel.
4. **Close.** The runtime keeps running for its chosen period. Apps keep receiving messages and notifications reach your phone.
5. **Stop.** After that period with the viewer closed, or straight away if you choose **Stop and save now**, Android shuts down. Its data is encrypted, sharded across Sia hosts, and the plaintext removed.
6. **Destroy.** The VM is deleted with its encrypted disk. The next start begins again on a fresh VM.


### Starting a session

1. **Start.** The phone sends a request signed with its Android Keystore key to the edge, the only public entry point.
2. **Verify.** The edge passes it to `virtroidd`, which checks the signature, the account and its credit.
3. **Provision.** The provisioner reuses the account's VM, takes a warm spare, or has Aleph Cloud create a new SEV-SNP VM in the account's region. It releases the VM's data-disk key and node secrets only after the VM's attestation matches an approved image.
4. **Restore.** `/data` is fetched from Sia and decrypted inside the VM. Each session starts a fresh Android container.
5. **Attest, then stream.** The phone checks the VM's attestation report: AMD's signature, the image measurement against the signed list, and the VM's TLS key bound into the report. Then it streams straight from the VM over QUIC. Without a direct route, the stream goes through the edge, still end-to-end encrypted.
6. **Save.** On stop, the data is encrypted inside the VM and saved back to Sia.

### Saving and restoring

A snapshot is the runtime's whole `/data`: apps, their data, accounts and files.

- **Encryption.** Inside the VM, the snapshot is compressed and encrypted with AES-256 in CTR mode under an HMAC-SHA256 tag (encrypt-then-MAC). The keys are derived from a per-runtime key, itself derived from your account key, with a fresh salt for each snapshot.
- **The manifest.** It lists the snapshot's 4 MiB chunks and their hashes, and it is authenticated with your account key, so a storage host or the control plane cannot swap, reorder or truncate chunks.
- **No rollback.** Every snapshot has a generation number. Your phone remembers the newest generation it has seen for each runtime and refuses an older one, so nobody can quietly serve you yesterday's data. A wipe or a lost snapshot is recorded explicitly, so a deliberate fresh start is accepted.
- **Storage.** The Sia indexer gives each account its own keys and places every chunk as 30 shards on independent hosts; any 10 rebuild it. The indexer sees only opaque object names and sizes.
- **Restore.** On start, the VM fetches the latest snapshot, checks every tag, and only then decrypts it into the new Android container.

---

## Using Virtroid

### Set up

1. **Install** the Virtroid client on any Android 9 or later phone.
2. **Create your identity.** The app makes your account's key pair and asks for an **identity password** of at least 14 characters. Write down your **account ID** and keep the password safe: together they recover your account on another phone, and nothing else can.
3. **Lock the app** (recommended). In **Privacy & Security**, set an 8-digit PIN, optionally with biometric unlock, to put the app's local state in an encrypted vault.

### Create a runtime

Tap **+** and choose:

- **Session name.**
- **Keep running:** slide between 30 minutes and 1 week. This is how long the runtime keeps running after you close the viewer.
- **Pre-install apps:** browse or search the F-Droid catalogue and pick up to 32 apps.
- **Audio passthrough** and **host physical camera** access.

The storage card's **ⓘ** explains how the runtime's data is kept.

### Use it

- **Start Session** gets the runtime a VM, attests it and opens the viewer. A first start takes a minute or two while the VM is built.
- **In the viewer** you touch and type as on a phone. Audio plays through, the microphone turns on only when a runtime app asks for it, and you can import a photo from your phone's camera or a file into the runtime.
- **Close the viewer** and the runtime keeps running for its period. Notifications arrive on your phone with the app's name, the time and the title.

### Manage a runtime

Each runtime's **Controls** offer:

- **Apps:** change which F-Droid apps it has.
- **Keep running when closed:** change the period, see the countdown to its stop, or **Stop and save now**.
- **Restart with New Persona**, **Factory Reset Runtime** (clears its saved data), and **Wipe & Delete Runtime**.
- **Display & Output**, **Console Logs** and **Build Identity**.

### Pay

**Wallet** shows your own USDT (TRC20) deposit address and your balance. New
accounts get one runtime and an hour of running time free. After that,
running time is charged per hour and storage per GB-month from the balance.

### Phones

- **Linked devices** (in **Account Identity**) lists every phone on your account. Revoke one you no longer have.
- **A new or replacement phone:** install the app, choose **Recover Account**, and enter your account ID and identity password. The new phone makes its own signing key; the old one isn't needed.
- **Identity password:** change it from **Account Identity**. Your account key stays the same, so runtimes and their snapshots are unaffected.

### Privacy settings

In **Privacy & Security**:

- **Block Screen Capture** keeps screenshots and the recent-apps preview from showing the viewer.
- **Close Viewer When Idle** closes the viewer after a period without input. The runtime keeps running.
- **Auto-clear Clipboard** empties the phone's clipboard when a session ends.
- **Require unlock on app resume** and the **Auto-lock Timer** decide when the app asks for its PIN.

---

## Architecture

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-target-dark.png">
  <img alt="Virtroid architecture: the phone sends a signed start through the edge; virtroidd checks credit and the provisioner has Aleph Cloud boot the account's own SEV-SNP VM from a measured image, releasing its disk key after attestation; the phone verifies the attestation, then streams from the VM; user data is restored from and saved to independent Sia hosts through the indexer; notifications are relayed to the phone" src="assets/architecture/virtroid-target-light.png">
</picture>


| Component | Where | Responsibility |
| :--- | :--- | :--- |
| Android client | Your phone | Identity, wallet, runtime controls, local security, attestation checks, the encrypted viewer, media capture, and notification display. It stores no runtime data |
| Edge (HAProxy) | Control plane | The only public entry point: HTTPS, the API, the fallback stream path, and the Sia indexer's application API |
| `virtroidd` | Control plane | Accounts, devices, policy, billing, placement, runtimes, capabilities, sessions, the app catalogue, notification delivery, and the Observatory |
| PostgreSQL | Control plane | Authoritative account, billing and lifecycle state |
| Provisioner | Control plane | Creates each account's SEV-SNP VM on Aleph Cloud, releases its secrets only after verifying its attestation, keeps warm spares, and deletes VMs no longer needed |
| Payments service | Control plane | Holds the deposit wallet's seed alone, finds confirmed USDT deposits on TRON and checks them on a second node, sweeps them to a treasury, and converts them into Siacoin and Aleph credits through instant-swap services |
| Sia indexer (`indexd`) | Control plane | Gives each account its own keys on Sia and pays storage hosts in Siacoin. It sees only opaque metadata |
| Host sensors | Control plane | Falco and Suricata watch the control plane; events go to an append-only bucket off the host |
| Runtime VM | Aleph Cloud, one per account | An AMD SEV-SNP guest booting the measured image, running `virtnoded`, the viewer encryption proxy and Tetragon, with Android in a container |
| `virtnoded` | Inside each runtime VM | Android lifecycle, the viewer, app installs, snapshots to Sia, and cleanup |
| Android runtime | Inside each runtime VM | A debloated AOSP build (no Google services) with F-Droid and the Virtroid agent, which forwards notification metadata only |
| Sia network | Independent hosts | Stores each account's encrypted snapshots as 30 shards, any 10 of which rebuild them |

### The runtime VM image

The image is a whole Linux VM, not just Android:

- **Read-only root.** It is built with Nix and verified block by block with dm-verity. Its root hash sits inside the measured boot chain, so changing a single byte changes the measurement the phone checks.
- **Hardened kernel.** It has Android's binder driver and refuses to load modules.
- **What runs on it.** `virtnoded`, the attestation agent, the viewer encryption proxy, Tetragon (process monitoring that blocks executables not on its allowlist), and Docker running the Android container.
- **Writable storage.** Only the VM's own data disk, encrypted with a key held only in memory.
- **No user data.** None is baked in; it comes from Sia at each start.

Which images phones accept is decided by a list of measurements signed with an
offline key. A new image needs a new signed list; revoking one is signing a
list without it.

### The viewer

The runtime's screen is captured inside Android, encoded as H.264, and
encrypted by a proxy inside the VM for the phone's session key. The phone
decrypts and decodes it, and sends touches, keys and the clipboard back on the
same channel. Audio, the microphone and camera import ride the same encrypted
session. Screens that apps mark as protected, such as banking apps or
messengers with screenshot blocking, are captured too: the phone is the
runtime's screen, and the client blocks screenshots of its own viewer.

### Placement and regions

Each account has a region, and its VM is never shared with another account.
South-East Asia and Australia are served from a dedicated SEV-SNP server in the
region, for example in Singapore. Europe and the US use Aleph Cloud's SEV-SNP
nodes, paid per hour. Warm spare VMs make the first start fast.

### Apps

Each runtime has its own apps, chosen when it is created or later from its
controls:

- **The catalogue** follows F-Droid hourly. The control plane checks the signature on F-Droid's index against F-Droid's published repository key, and pins every app by its SHA-256.
- **Browsing** is a page at a time, with server-side search.
- **Installs.** The node installs each selected app once and records that in the runtime's own data. An app you uninstall stays uninstalled until you select it again.
- **F-Droid on the runtime** can install anything else, without a detour through Android's settings.

### Money

- **Deposits.** Each account deposits USDT (TRC20) to its own address. Deposits are credited once, after confirmation and a check on a second node, to a USDT balance.
- **Charges.** Running time is charged per hour and storage per GB-month. New accounts get one runtime and an hour of running time free.
- **Conversion.** The payments service sweeps deposits to a treasury and converts them through instant-swap services: into Siacoin for the indexer, and into ETH that buys Aleph credits and pays their gas. It tops each up when it runs low.

---

## Security model

### What Virtroid protects

- **Data on the handset.** There is none. The phone holds non-exportable keys and an encrypted vault.
- **Data in use.** The runtime VM's memory is encrypted by the CPU. The phone streams only after verifying the VM's attestation and image.
- **Data in transit.** The stream is encrypted between the phone and the viewer proxy inside the VM, so the edge and any relay see ciphertext. Control traffic is signed and replay-protected.
- **Data at rest.** It is encrypted inside the VM with your account key and stored only as shards on independent Sia hosts. The VM that ran it is destroyed.
- **Notifications.** They are encrypted for your phone alone and carry metadata only, no message bodies.

### What you still trust

- **The image.** Whoever builds and signs it. The build is reproducible, so its measurement can be checked independently.
- **The provisioner.** It holds each VM's data-disk key, though it releases it only to a VM that attests to an approved image.
- **The control plane.** It decides where runtimes run and sees metadata: which account, which runtime, when, and from which IP address the phone connects.
- **AMD's SEV-SNP and the firmware on the host,** against side channels and firmware flaws.
- **Your phone while it is unlocked and in use.** Malware on it can see what the viewer shows.
- **The strength of your identity password.** It is stretched with PBKDF2-HMAC-SHA256 at 600,000 iterations, but a weak password is still guessable offline by anyone holding the wrapped key.

### Implemented controls

- P-256 device, node, capability, and callback signing
- Timestamp, nonce, body-integrity, and replay validation
- Runtime- and session-scoped capabilities with expiry
- TLS viewer transport with session-bound relay credentials
- Remote nodes reached only at operator-allowlisted IPs, over TLS pinned to each node's reported key, for both control-plane calls and phone streams
- SEV-SNP attestation of runtime VMs, checked by the provisioner before it releases a VM's secrets and by the phone before it streams
- Approved images listed by measurement and signed with an offline key whose public half ships in the app
- Android Keystore-backed client secrets that sign only while the phone is unlocked, and encrypted local state
- AES-256-CTR with HMAC-SHA256 snapshot encryption, authenticated manifests and monotonic generations, under an account key wrapped by the user's identity password
- Identity password changes accepted only with a signature from the account's recovery key
- A notification relay key that works while the phone is locked but is accepted only for the notification stream
- App catalogue verified against F-Droid's signed index, with every APK pinned by SHA-256
- Deposit addresses derived from a public key, with the wallet seed held only by the isolated payments service, which signs its reports
- Bounded resource quotas and explicit lifecycle cleanup obligations
- Loopback-bound services, deny-by-default firewalling, AppArmor, Auditd, Fail2ban, and unattended security updates
- Falco and Suricata event collection with sanitized, account-scoped client delivery
- Protected, offline release builds with immutable image and deployment-tree verification, and CI that rejects committed binaries

<br>

> [!CAUTION]
> Virtroid separates your data from your handset and from any single server,
> but it is not an anonymity network. The control plane and the runtime VM see
> your phone's IP address when it connects, and your deposits are public on
> TRON. Use a VPN or Tor on the phone if that matters.

---

## Latency

What you feel as lag in the viewer is mostly the network round trip between
your phone and the VM, so Virtroid places each account's runtimes in its own
region:

- **Distance.** A touch travels to the VM and the changed screen travels back, so every interaction pays one full round trip. Light in fibre adds about 1 ms of round trip per 100 km of route, and real routes are longer than the straight line. A VM on the other side of the world costs a quarter of a second before anything else happens; one in your region costs a few tens of milliseconds.
- **Video.** Encoding a frame in the VM and decoding it on the phone adds roughly one or two frames, about 30–60 ms at 30 frames a second.
- **Transport.** Over TCP, one lost packet stalls everything behind it for a round trip, which shows as a hitch rather than constant lag. Streaming over QUIC removes that.
- **Rendering.** Confidential VMs have no GPU, so Android draws on the CPU. Heavy apps and animations can drop frames.

---

## Progress

As of 14 October 2026.

### Working

| Area | Status | Notes |
| :--- | :---: | :--- |
| Identity and recovery | ✅ Live | Sign-up with automatically issued invitations, device signing, linked phones, identity password changes, account deletion, and recovery on a replacement phone, done on a real phone |
| Confidential runtime VMs | ✅ Live | Per-account SEV-SNP VMs on Aleph Cloud (Europe), created on demand from the measured, verified-root image (image 13) and deleted 30 minutes after last use. A new VM passes attestation and is unlocked in 30–90 seconds once its compute node has the image |
| Attestation and signed images | ✅ Live | The phone verifies each VM against AMD's root and the offline-signed image list (app 0.3.7 and later) |
| Encrypted storage on Sia | ✅ Live | 50 storage contracts on mainnet. A real runtime's 590 MB of data saves in about 30 seconds |
| Restart from Sia on a new VM | ✅ Live | Done on a real phone: after the old VM was deleted, a new one restored the runtime and Android was up 27 seconds after the start began, with the viewer open about 1 minute 40 seconds after the VM was created |
| Runtime lifecycle | ✅ Live | Create, start, stop, restore, a fresh persona on every start, factory reset, deletion, and lost-snapshot handling |
| Keep running when closed | ✅ Live | Per-runtime period from 30 minutes to a week, set with a slider, with a countdown and Stop and save now (app 0.4.0) |
| Notifications | ✅ Live | Metadata-only, encrypted inside the VM for the account's phones so the control plane sees only ciphertext, and delivered while the phone is locked |
| Apps | ✅ Live | Per-runtime selection from about 4,400 F-Droid apps, a paged catalogue that follows F-Droid's signed index, installs that happen once, and F-Droid allowed to install |
| Remote viewer | ✅ Live | Encrypted live control, audio, on-demand microphone, camera import, and direct streaming over pinned TLS with an edge fallback |
| Wallet and billing | ✅ Live | USDT (TRC20) deposits, a trial, and charges of 0.25 USDT per running hour and 0.05 USDT per GB-month stored |
| Operator console | ✅ Live | The Observatory: fleet, runtime VMs, payments, Sia, security, and release telemetry |
| Control-plane hardening | ✅ Live | Falco, Suricata, firewalling, AppArmor, and protected releases |
| Database backups | ✅ Live | Every night the control plane's and indexd's databases are sealed with a key from the indexd recovery phrase and stored on Aleph; [restoring](deploy/vps/indexd.md#backups) needs only the phrase |

### What's left

| Area | Status | Notes |
| :--- | :---: | :--- |
| Confidential handoffs and viewer | ✅ Live | Images 12 and 13 with app 0.4.2 and later: capture as Android's `system` user, so apps that block screenshots (SimpleX Chat) show in the viewer; the viewer's hops inside Android on private sockets; snapshot keys sealed only to a VM that has just proved itself, and only that runtime's keys; notifications encrypted inside the VM; viewers that must prove they hold the runtime's key, so the control plane cannot watch or control a runtime; photos and files sealed for the VM, so it can neither read nor plant them; and a Docker store rebuilt from the measured image at every boot. Done on a real phone on 15 October 2026 |
| Low-latency streaming | ✅ Live | Image 13 and app 0.4.3: BBR congestion control, the node reading the viewer's socket directly, small buffers along the whole path, and an encoder that lowers its bitrate while the connection falls behind and raises it again after, so video drops quality instead of queueing. The session bar shows how far the picture is behind when it is |
| Security review fixes | 🟡 Mostly done | Two reviews (14 October 2026) found 18 and 28 issues. Fixed on the server: crash-safe VM creation and deletion, node keys revoked before a deletion is recorded, standard key derivation for snapshots, a fresh database that initialises, and the backups above. Fixed in images 12 and 13 and app 0.4.2: the confidential handoff and viewer items above, Milan chips refused, and attestation required by release builds. Still open: an expiring signed image list, rollback protection that does not trust the server's floor, migrating older device keys, and finer TCB checks on the phone |
| South-East Asia region | ⏳ Not built | Europe only today. From Bangkok a Europe VM is about 230 ms away per round trip; a Singapore server would be about 30–50 ms |
| QUIC streaming | ⏳ Not planned yet | Streams use TLS over TCP, now with BBR and bounded queues. QUIC would mainly shorten reconnecting and survive switching networks; it would not shorten the 230 ms round trip from Bangkok, which only a closer region removes |
| Custom AOSP image | ⏳ Not built | Runtimes use the stock ReDroid 14 image; F-Droid is installed at first start |
| Tetragon in the VM | ⏳ Not built | |
| Deposit conversion | 🧪 Dry run | Treasury sweeps and SimpleSwap swaps into Siacoin, ETH for Aleph credits, and TRX for fees are built and log what they would do; Siacoin and Aleph credits are topped up by hand until they are switched to live |
| Off-host append-only logs | ⏳ Not built | Sensor events stay on the control plane |

> [!NOTE]
> **Disclaimer:**
> Virtroid is under active development. Security properties, interfaces, schemas, and deployment procedures may change.
> Do not use it for high-risk or production-sensitive workloads without independently reviewing the source,
> deployed configuration, threat model, recovery design, runtime-host trust, and storage limitations.

---

## Building from source

### Requirements

| For | You need |
| :--- | :--- |
| Backend | Go 1.27; PostgreSQL 18 for the integration tests |
| Android client | JDK 17 and the Android SDK (compile SDK 36) |
| Operator console | Node.js and npm |
| Runtime VM image | Nix on a Linux machine with `/dev/kvm` |

### Backend

```bash
cd backend
go build ./cmd/...
go test -p 1 ./...
```

Tests that need a database run when `VIRTROID_TEST_DATABASE_URL` points at an
empty PostgreSQL database; without it they are skipped. Keep `-p 1`: the test
packages share the database's schema.

To run a local control plane for a debug build of the app:

```bash
cd backend
APP_ENV=development BIND_ADDR=127.0.0.1:8080 BOOTSTRAP_REQUIRE_INVITE=false \
  PUBLIC_BASE_URL=http://10.0.2.2:8080 APP_CATALOG_SYNC_ENABLED=false \
  DATABASE_URL='postgres://virtroid:virtroid@127.0.0.1:5432/virtroid?sslmode=disable' \
  go run ./cmd/virtroidd
```

### Android client

```bash
cd android-client
./gradlew :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
```

A debug build talks to the production control plane unless
`VIRTROID_DEBUG_CONTROL_PLANE_URL` is set. For the local control plane above,
from the Android emulator:

```bash
VIRTROID_DEBUG_CONTROL_PLANE_URL=http://10.0.2.2:8080 ./gradlew :app:assembleDebug
```

A release build needs Virtroid's release signing key, which only the
maintainers hold. The Gradle build refuses a release without it, without an
HTTPS control plane, or with a manifest that fails its security checks.

### Operator console

```bash
cd operator-console
npm ci
npm run build
```

### Runtime VM image and deployment

- **The runtime VM image:** building, measuring, publishing and approving it are in [`deploy/aleph/README.md`](deploy/aleph/README.md).
- **The control plane:** deployment, hardening and releases are in [`deploy/vps/README.md`](deploy/vps/README.md), with the Sia indexer in [`deploy/vps/indexd.md`](deploy/vps/indexd.md) and payments in [`deploy/vps/payments.md`](deploy/vps/payments.md).
- **The screen-capture server:** its source, provenance and rebuild check are in [`third_party/scrcpy-server/`](third_party/scrcpy-server/) and [`backend/third_party/`](backend/third_party/).

---

## Repository map

| Path | Purpose |
| :--- | :--- |
| [`android-client/`](android-client/) | Android controller app, runtime agent, scrcpy integration, tests, and signing gates |
| [`operator-console/`](operator-console/) | The Observatory: the React operator console and its production bundle |
| [`backend/cmd/`](backend/cmd/) | Control plane, node agent, provisioner, payments service, measurement signing, administration, viewer encryption, and sensor entry points |
| [`backend/internal/`](backend/internal/) | Identity, policy, billing, persistence, lifecycle, app catalogue, Sia, TRON, security, and operator API logic |
| [`deploy/vps/`](deploy/vps/) | Reproducible control-plane deployment, hardening, HAProxy, Falco, Suricata, the Sia indexer and payments services, and release tooling |
| [`deploy/aleph/`](deploy/aleph/) | The confidential runtime VM image, its kernel, and the operator guide for provisioning |
| [`deploy/provisioner/`](deploy/provisioner/) | The provisioner image: `virtroid-provisioner` plus Aleph's `aleph` CLI |
| [`assets/`](assets/) | Architecture diagram images and their Archify source |
| [`third_party/`](third_party/) | Reviewable vendored source, provenance, and upstream notices |
| [`.github/workflows/`](.github/workflows/) | CI: backend, Android, deployment and code-scanning checks |

---

<p align="center">
  <img src="android-client/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png" width="128" alt="Virtroid logo">
</p>
