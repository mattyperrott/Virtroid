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
  <a href="#architecture">Architecture</a> ·
  <a href="#security-boundary">Security</a> ·
  <a href="#progress">Progress</a> ·
  <a href="https://virtroid.network/demo/">Browser Emulated App Demo</a>
</p>

---

## Why Virtroid

### Your phone holds none of your data

Apps, messages, photos, accounts and files never land on the handset. A
forensic extraction device (the kind used at borders, by police or by a thief
with the right tools) that images your phone finds the Virtroid client and its
keys, but none of your runtime data, because it was never there.

The keys don't help either:

- **The device key** lives in Android's hardware-backed Keystore and cannot be exported.
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

1. **Before it leaves the VM,** it is encrypted with your account key (AES-256-GCM), with a manifest that authenticates every chunk and its generation.
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

- **Device persona.** Each runtime presents its own device persona (model and fingerprint) and can be restarted with a fresh one.
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
2. **Start.** The runtime gets its own fresh confidential VM. Its data is restored from Sia and decrypted inside the VM before Android boots. Selected apps are installed once.
3. **Use.** The phone attests the VM, then streams its screen over an end-to-end encrypted channel.
4. **Close.** The runtime keeps running for its chosen period. Apps keep receiving messages and notifications reach your phone.
5. **Stop.** After that period with the viewer closed, or straight away if you choose **Stop and save now**, Android shuts down. Its data is encrypted, sharded across Sia hosts, and the plaintext removed.
6. **Destroy.** The VM is deleted with its encrypted disk. The next start begins again on a fresh VM.

The diagram's source is [`assets/architecture/virtroid-lifecycle.archify.json`](assets/architecture/virtroid-lifecycle.archify.json).

### Starting a session

1. **Start.** The phone sends a request signed with its Android Keystore key to the edge, the only public entry point.
2. **Verify.** The edge passes it to `virtroidd`, which checks the signature, the account and its credit.
3. **Provision.** The provisioner reuses the account's VM, takes a warm spare, or has Aleph Cloud create a new SEV-SNP VM in the account's region. It releases the VM's data-disk key and node secrets only after the VM's attestation matches an approved image.
4. **Restore.** `/data` is fetched from Sia and decrypted inside the VM. Each session starts a fresh Android container.
5. **Attest, then stream.** The phone checks the VM's attestation report (AMD's signature, the image measurement against the signed list, and the VM's TLS key bound into the report). Then it streams straight from the VM over QUIC. Without a direct route, the stream goes through the edge, still end-to-end encrypted.
6. **Save.** On stop, changed data is encrypted inside the VM and saved back to Sia.

---

## Architecture

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-target-dark.png">
  <img alt="Virtroid architecture: the phone sends a signed start through the edge; virtroidd checks credit and the provisioner has Aleph Cloud boot the account's own SEV-SNP VM from a measured image, releasing its disk key after attestation; the phone verifies the attestation, then streams from the VM; user data is restored from and saved to independent Sia hosts through the indexer; notifications are relayed to the phone" src="assets/architecture/virtroid-target-light.png">
</picture>

The diagram's source is [`assets/architecture/virtroid-target.archify.json`](assets/architecture/virtroid-target.archify.json).

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
| Sia network | Independent hosts | Stores each account's encrypted snapshots as 30 shards, any 10 of which rebuild it |

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

## Security boundary

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
- **The strength of your identity password.**

### Implemented controls

- P-256 device, node, capability, and callback signing
- Timestamp, nonce, body-integrity, and replay validation
- Runtime- and session-scoped capabilities with expiry
- TLS viewer transport with session-bound relay credentials
- Remote nodes reached only at operator-allowlisted IPs, over TLS pinned to each node's reported key, for both control-plane calls and phone streams
- SEV-SNP attestation of runtime VMs, checked by the provisioner before it releases a VM's secrets and by the phone before it streams
- Approved images listed by measurement and signed with an offline key whose public half ships in the app
- Android Keystore-backed client secrets and encrypted local state
- AES-GCM snapshot encryption with authenticated manifests and monotonic generations, under an account key wrapped by the user's identity password
- Identity password changes accepted only with a signature from the account's recovery key
- A notification relay key that works while the phone is locked but is accepted only for the notification stream
- App catalogue verified against F-Droid's signed index, with every APK pinned by SHA-256
- Deposit addresses derived from a public key, with the wallet seed held only by the isolated payments service, which signs its reports
- Bounded resource quotas and explicit lifecycle cleanup obligations
- Loopback-bound services, deny-by-default firewalling, AppArmor, Auditd, Fail2ban, and unattended security updates
- Falco and Suricata event collection with sanitized, account-scoped client delivery
- Protected, offline release builds with immutable image and deployment-tree verification

<br>

> [!CAUTION]
> Virtroid separates your data from your handset and from any single server,
> but it is not an anonymity network. The control plane and the runtime VM see
> your phone's IP address when it connects, and your deposits are public on
> TRON. Use a VPN or Tor on the phone if that matters.

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

---

## Progress

| Area | Status | Notes |
| :--- | :---: | :--- |
| Identity and recovery | ✅ Live | Invite-gated accounts, device signing, linked phones, identity password changes, replacement-phone recovery, and account deletion |
| Confidential runtime VMs | ✅ Live | Per-account SEV-SNP VMs on Aleph Cloud (Europe), created on demand from the measured, verified-root image and deleted when no longer needed |
| Attestation and signed images | ✅ Live | The phone verifies each VM against AMD's root and the offline-signed image list (app 0.3.7 and later) |
| Encrypted storage on Sia | ✅ Live | 50 storage contracts on mainnet; a 64 MiB snapshot saves at about 5 MiB/s and restores at about 18 MiB/s |
| Runtime lifecycle | ✅ Live | Create, start, stop, restore, persona restart, factory reset, deletion, and lost-snapshot handling |
| Keep running when closed | ✅ Live | Per-runtime period from 30 minutes to a week, with a countdown (app 0.4.0) |
| Notifications | ✅ Live | Metadata-only, end-to-end encrypted, and delivered while the phone is locked (app 0.3.8 and later) |
| Apps | ✅ Live | Per-runtime selection, a paged catalogue that follows F-Droid's signed index, installs that happen once, and F-Droid allowed to install |
| Remote viewer | ✅ Live | Encrypted live control, audio, demand-activated microphone, camera import, and direct streaming over pinned TLS with an edge fallback |
| Wallet and billing | ✅ Live | USDT (TRC20) deposits, a trial, and per-hour runtime and per-GB storage charges |
| Operator console | ✅ Live | The Observatory: fleet, runtime VMs, payments, Sia, security, and release telemetry |
| Control-plane hardening | ✅ Live | Falco, Suricata, firewalling, AppArmor, and protected releases |
| QUIC streaming | ⏳ Not built | Streams use TLS over TCP today |
| Custom AOSP image | ⏳ Not built | Runtimes use the stock ReDroid 14 image; F-Droid is installed at first start |
| Tetragon in the VM | ⏳ Not built | |
| South-East Asia region | ⏳ Not built | A dedicated SEV-SNP server near users; Europe only today |
| Deposit conversion | ⏳ Not built | Treasury sweeps and automatic swaps; SimpleSwap and ChangeNOW are chosen and Siacoin and Aleph credits are topped up by hand |
| Off-host append-only logs | ⏳ Not built | Sensor events stay on the control plane |
| Phone end-to-end on Sia | 🟡 Testing | A stopped runtime restarting from Sia on a new VM, on a real phone |

> [!NOTE]
> **Disclaimer:**
> Virtroid is under active development. Security properties, interfaces, schemas, and deployment procedures may change.
> Do not use it for high-risk or production-sensitive workloads without independently reviewing the source,
> deployed configuration, threat model, recovery design, runtime-host trust, and storage limitations.

---

<p align="center">
  <img src="android-client/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png" width="128" alt="Virtroid logo">
</p>
