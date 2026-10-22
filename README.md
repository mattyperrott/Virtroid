<!-- markdownlint-disable MD013 MD033 MD041 -->

<h1 align="center">Virtroid</h1>

<p align="center">
  <strong>Your Android, kept off your phone.</strong><br>
  Private Android runtimes in confidential cloud VMs.<br>
  Your phone is the controller and encrypted viewer.
</p>

<p align="center">
  <img alt="Release candidate" src="https://img.shields.io/badge/status-release%20candidate-AFD135?style=for-the-badge&labelColor=131313">
  <img alt="Android 9+" src="https://img.shields.io/badge/client-Android%209%2B-AFD135?style=for-the-badge&labelColor=131313">
  <img alt="AMD SEV-SNP" src="https://img.shields.io/badge/runtime-AMD%20SEV--SNP-F4F4F5?style=for-the-badge&labelColor=131313">
  <img alt="Sia storage" src="https://img.shields.io/badge/storage-Sia-F4F4F5?style=for-the-badge&labelColor=131313">
  <img alt="Tor egress" src="https://img.shields.io/badge/egress-direct%20or%20Tor-F4F4F5?style=for-the-badge&labelColor=131313">
</p>

<p align="center">
  <a href="https://github.com/mattyperrott/Virtroid/releases/latest"><strong>Download the Android client</strong></a>
</p>

<p align="center">
  <a href="#why-virtroid">Why Virtroid</a> ·
  <a href="#using-virtroid">Get started</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#security-model">Security</a> ·
  <a href="#latency">Latency</a> ·
  <a href="#progress">Progress</a> ·
  <a href="#building-from-source">Build</a>
</p>

<img width="1788" height="944" alt="Virtroid preview" src="https://github.com/user-attachments/assets/e9cace37-5f98-4384-a01a-dddc96e5b4d2">

---

## Overview

Virtroid runs your Android apps remotely. Your phone is the controller and encrypted viewer: it verifies the VM’s hardware attestation, displays its screen and sends your input. Apps, accounts, messages and files stay in the runtime.

Each account has a dedicated confidential virtual machine protected by **AMD SEV-SNP**. When a runtime stops, its data is encrypted inside the VM and saved across independent **Sia storage hosts**. The provisioner deletes the account’s VM and its encrypted disk when the VM is no longer in use; a later start can restore your data into a replacement VM.

You choose when runtimes start, how long they keep running after you close the viewer, which apps they contain, and when to reset or delete them.

[Contributing](CONTRIBUTING.md) · [Code of conduct](CODE_OF_CONDUCT.md) ·
[Security policy](SECURITY.md) · [Accessibility](ACCESSIBILITY.md) ·
[License and commercial use](LICENSING.md)

---

## Why Virtroid

### Keep runtime data off your phone

Your remote apps, messages, accounts and files live in the runtime. The handset holds the Virtroid client, its credentials and local settings, rather than a local copy of the runtime’s app databases and files.

The client protects the information it does keep:

- **Device key.** Held in Android’s hardware-backed Keystore, non-exportable, and used for device signing only while the phone is unlocked.
- **Account key.** Wrapped by your identity password, which is never stored on the phone or sent to the service. Optionally, your fingerprint opens it on that phone instead of the password.
- **Local client state.** Logs, settings and session handles can be protected by an encrypted vault with an app PIN or biometric unlock.
- **Notifications.** Optional, encrypted metadata containing the app name, time and title, without the message body.

Your phone remains part of the security boundary while you use it. Its local credentials, metadata and displayed content still need protection.

### Run only when you need it

Close the viewer and the runtime keeps running for the period you choose, from **30 minutes to 1 week**. Messaging apps keep receiving, and encrypted notifications let you know when to reopen the viewer. You can change the period, check the shutdown countdown or choose **Stop and save now**.

When the runtime stops, Android shuts down, its data is encrypted and saved to Sia, and the plaintext is removed. VM retirement is a separate step: starts can reuse the account’s existing VM until it becomes unused and is deleted. A start after deletion restores the runtime onto a fresh VM.

### Store encrypted data across independent hosts

Your snapshot is encrypted before it leaves the VM. Sia splits each chunk into **30 shards** across independent storage hosts; **any 10** reconstruct the encrypted data. A single host holds an encrypted fragment, and reconstruction still leaves ciphertext that requires your runtime’s key.

An authenticated manifest protects the chunks, their order and the snapshot's generation. Each phone seals the newest generation it has seen together with the runtime's keys, and the VM refuses to restore anything older, so the control plane cannot roll a runtime back to an earlier snapshot.

### Verify before you connect

AMD SEV-SNP encrypts the VM’s memory with a key unavailable to the host. The VM boots a measured, read-only image, and the provisioner releases its node secrets only after verifying its attestation. The VM encrypts its working disk with a key it makes at each boot, which never leaves it.

Before showing the runtime’s screen, your phone checks AMD’s attestation signature, the image measurement against an **offline-signed approval list**, and the VM’s bound TLS key. Reproducible builds let others rebuild the image and compare measurements.

The approved software, release signer, hardware and other trust boundaries are described in the [security model](#security-model).

### Separate your handset from the runtime

- **Device persona.** Each start presents a fresh device model and build fingerprint. Apps keep their device ID across ordinary starts, so they stay signed in; **Restart with New Persona** gives every app a new one. Accounts and restored app data can still link sessions.
- **Network.** Runtime apps use the VM’s datacenter connection rather than the phone’s IP address or carrier connection. Optionally, a runtime’s own traffic goes through **Tor**, including `.onion` addresses, and nothing else is let out, even if Tor stops. Your phone’s link to the runtime stays direct.
- **Location.** Give each runtime a place: a city from a built-in list, or coordinates. The runtime takes that place’s timezone. The choice is sealed on your phone for the VM, so the service cannot read it. Apps will see the place itself as a GPS position once the custom Android image adds a location provider.
- **Account isolation.** Each account has its own VM. Runtimes belonging to that account can use the same VM; separate runtimes do not each have their own VM boundary.
- **Apps without Google services.** Browse a verified F-Droid catalogue or install more through F-Droid inside the runtime. Runtimes currently use stock ReDroid 14; a debloated image of our own, built from LineageOS, is planned ([plan](deploy/aosp/README.md)).

### No personal details to register

An account consists of a key pair and an identity password. You do not need to supply a name, email address or phone number. Fund your balance with **USDT** and pay for runtime hours and stored data. New accounts receive **one runtime and one free running hour**.

### Lose your phone, keep your runtimes

Runtimes belong to your account. On a replacement phone, recover with your **account ID** and **identity password**. The new phone creates its own signing key; revoke the lost phone from another linked device.

---

## Using Virtroid

### 1. Set up your identity

1. **[Download the Android client](https://github.com/mattyperrott/Virtroid/releases/latest)** and install it on an Android 9 or later phone.
2. **Create your identity.** The app generates your account’s key pair and asks for an **identity password of at least 14 characters**.
3. **Save your account ID and identity password.** Together, they recover your account on another phone. There is no alternative recovery method.
4. **Lock the app — recommended.** In **Privacy & Security**, set an **8-digit PIN**, optionally with biometric unlock, to protect local state in an encrypted vault.

### 2. Create a runtime

Tap **+** and configure:

| Setting | Options |
| :--- | :--- |
| **Session name** | Choose a name for the runtime. |
| **Keep running** | Set how long it remains active after closing the viewer: **30 minutes to 1 week**. |
| **Pre-install apps** | Browse or search F-Droid and select **up to 32 apps**. |
| **Location** | Pick a city or enter coordinates, or leave it off. Sealed for the VM; the runtime takes the place’s timezone. |
| **Route through Tor** | Send the runtime’s own internet traffic through Tor. Slower, and calls, video chat and some sites won’t work. |
| **Audio passthrough** | Configure runtime audio access. |
| **Host physical camera** | Configure access to the phone’s camera. |

The storage card’s **ⓘ** explains how runtime data is stored.

### 3. Start and use it

- **Start Session** assigns a VM, verifies its attestation and opens the viewer. A first start typically takes **a minute or two** while the VM is prepared.
- **Touch and type** as you would on a phone.
- **Play audio** through the handset. The microphone activates only when a runtime app requests it.
- **Import a camera photo or file** from your phone into the runtime.
- **Close the viewer** to leave the runtime running for its configured period.

While the viewer is closed, messaging apps keep receiving messages. Optional notifications reach your phone with the **app name, time and title**, encrypted end to end. Message bodies are excluded.

### Runtime controls

| Control | Purpose |
| :--- | :--- |
| **Apps** | Change the runtime’s F-Droid app selection. |
| **Keep running when closed** | Change the period and view the countdown until shutdown. |
| **Stop and save now** | Shut down Android and save its data immediately. |
| **Location** | Change or turn off the runtime’s place. |
| **Route through Tor** | Turn Tor on or off while the runtime is stopped; it applies from the next start. |
| **Restart with New Persona** | Save, then restart as a new device, with new app device IDs. Some apps may ask you to sign in again. |
| **Factory Reset Runtime** | Clear the runtime’s saved data. |
| **Wipe & Delete Runtime** | Wipe saved data and remove the runtime. |
| **Display & Output** | Configure display and output settings. |
| **Console Logs** | Inspect runtime logs. |
| **Build Identity** | View build identity information. |

### Wallet and billing

**Wallet** displays your account’s **USDT (TRC20) deposit address** and balance. Charges are deducted from that balance.

| Item | Rate or allowance |
| :--- | :--- |
| New-account trial | **1 runtime and 1 running hour free** |
| Runtime usage | **0.25 USDT per running hour** |
| Stored data | **0.05 USDT per GB-month** |

### Linked phones and recovery

Manage phones and credentials in **Account Identity**:

- **Linked devices** lists every phone connected to your account. Revoke devices you no longer have.
- **Recover Account** restores access on a new or replacement phone using your account ID and identity password. The new phone generates its own signing key; the old phone is unnecessary.
- **Change your identity password** without changing the underlying account key. Existing runtimes and snapshots remain unaffected.
- **Unlock with fingerprint** lets your fingerprint open the account key on this phone instead of typing the identity password. The key copy it uses is destroyed if a fingerprint is added to the phone, and dropped when you sign out.

### Privacy settings

In **Privacy & Security**:

| Setting | Behavior |
| :--- | :--- |
| **Block Screen Capture** | Prevents screenshots and the recent-apps preview from showing the viewer. |
| **Close Viewer When Idle** | Closes the viewer after a period without input. The runtime continues running. |
| **Auto-clear Clipboard** | Clears the phone’s clipboard when a session ends. |
| **Require unlock on app resume** | Requires the app’s PIN when returning to it. |
| **Auto-lock Timer** | Controls when the app locks and requests its PIN. |
| **Force Clear Local Cache Now** | Removes the app’s cached local data on the phone. |
| **What the operator can see** | Lists the metadata the service can and cannot see. |

---

## How it works

### Runtime lifecycle

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-lifecycle-dark.png">
  <img alt="Runtime lifecycle: create; start on a confidential VM and restore from Sia; verify and use; keep running after closing the viewer; stop and save encrypted data to Sia; destroy the unused VM; start again. Wipe clears saved data, and delete removes the runtime." src="assets/architecture/virtroid-lifecycle-light.png">
</picture>


1. **Create.** Choose a name, a keep-running period and any F-Droid apps to pre-install.
2. **Start.** Assign a confidential VM and a fresh device persona. Restore and decrypt saved data inside the VM before Android boots. Apply the runtime’s sealed location and, if chosen, route its traffic through Tor on fresh circuits. Selected apps are installed once.
3. **Use.** The phone verifies the VM, then streams its screen over an end-to-end encrypted channel.
4. **Close.** The runtime stays active for the selected period. Apps continue receiving messages and forwarding notification metadata.
5. **Stop and save.** When the period expires, or you select **Stop and save now**, Android shuts down. Its data is encrypted, saved across Sia hosts and removed in plaintext.
6. **Retire the VM.** Once the account’s VM is unused, the provisioner deletes it and its encrypted disk. The deployment described under [Progress](#progress) uses a **30-minute idle period**. A start before retirement can reuse the VM; a start after deletion provisions a replacement and restores the saved data.

Saved snapshots remain on Sia rather than on the compute machine that ran the runtime.

### Session startup

| Step | What happens |
| :--- | :--- |
| **1. Request** | The phone signs a start request with its Android Keystore key and sends it to the edge, the public API entry point. |
| **2. Validate** | The edge forwards the request to `virtroidd`, which verifies the signature, account and available credit. |
| **3. Provision** | The provisioner reuses the account’s existing VM, assigns a warm spare or requests a new SEV-SNP VM from Aleph Cloud in the account’s region. |
| **4. Unlock and restore** | Only after attestation matches an approved image does the provisioner release the data-disk key and node secrets. The VM retrieves `/data` from Sia, decrypts it internally and starts a fresh Android container. |
| **5. Attest and stream** | The phone verifies AMD’s signature, the image measurement against the signed allowlist, and the VM’s TLS key bound into the report. It then streams directly over pinned TLS, with an end-to-end encrypted edge fallback when a direct route is unavailable. |
| **6. Save** | On stop, the VM encrypts the runtime’s data and saves it back to Sia. |

**Transport:** streaming uses **TLS over TCP**, with BBR congestion control and bounded queues. QUIC remains an unscheduled option for faster reconnects and network changes.

### Snapshots, storage and restore

A snapshot contains the runtime’s entire **`/data`**: apps, app data, accounts and files.

| Layer | Mechanism |
| :--- | :--- |
| **Encryption** | Inside the VM, the snapshot is compressed and encrypted using **AES-256-CTR with HMAC-SHA256**, in an encrypt-then-MAC construction. Keys derive from a per-runtime key, which derives from the account key, with a fresh salt for every snapshot. |
| **Authenticated manifest** | The manifest records the snapshot’s **4 MiB chunks**, their hashes and its generation. Authentication with the account key protects against chunk substitution, reordering and truncation by storage hosts or the control plane. |
| **Generation tracking** | The phone records the newest generation it has seen for each runtime and seals it with the runtime's keys. The VM refuses older or unauthenticated snapshots and numbers new ones above it, so a wipe or a lost snapshot never brings an older one back. A phone with no record yet (new or recovered) accepts the newest authenticated snapshot. |
| **Distributed storage** | The Sia indexer gives each account its own keys. Each chunk becomes **30 shards on independent storage hosts**; any **10** reconstruct it. The indexer sees opaque object names and sizes. |
| **Restore** | The VM fetches the latest snapshot, verifies its authentication tags and only then decrypts it into the new Android container. |

Reconstructing a stored snapshot requires enough shards from separate hosts. Reconstruction yields ciphertext; decryption still requires the account key, which is protected by the identity password.

---

## Architecture

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-target-dark.png">
  <img alt="Virtroid architecture: the phone sends signed requests through the edge; virtroidd checks credit; the provisioner prepares an account-dedicated SEV-SNP VM and releases its disk key after attestation; the phone verifies the VM and streams from it; encrypted snapshots move through the indexer to independent Sia hosts; encrypted notification metadata is relayed to linked phones." src="assets/architecture/virtroid-target-light.png">
</picture>

The diagrams describe the architecture and intended lifecycle. VM retirement follows the account-level idle policy above. The component descriptions identify planned features.

### Components

| Component | Location | Responsibility |
| :--- | :--- | :--- |
| **Android client** | Your phone | Identity, wallet, runtime controls, local security, attestation verification, encrypted viewing, media capture and notification display. Runtime app data stays remote. |
| **Edge — HAProxy** | Control plane | Public HTTPS/API entry point, fallback streaming path and the Sia indexer’s application API. |
| **`virtroidd`** | Control plane | Accounts, devices, policy, billing, placement, runtimes, capabilities, sessions, app catalogue, notification delivery and the Observatory. |
| **PostgreSQL** | Control plane | Authoritative account, billing and lifecycle state. |
| **Provisioner** | Control plane | Creates account-dedicated SEV-SNP VMs on Aleph Cloud, verifies attestation before releasing secrets, manages warm spares and deletes unused VMs. |
| **Payments service** | Control plane | Sole holder of the deposit wallet seed. Detects confirmed TRON USDT deposits, checks them against a second node and signs reports. Sweeps deposits into a treasury and swaps them for what the service runs on, within per-swap and daily caps. |
| **Sia indexer — `indexd`** | Control plane | Assigns account-specific Sia keys and pays storage hosts in Siacoin. Sees opaque storage metadata. |
| **Host sensors** | Control plane | Falco and Suricata monitor the control plane. Off-host, append-only event storage is planned; events currently remain on the control plane. |
| **Runtime VM** | Aleph Cloud; one per account | SEV-SNP guest booting a measured image, running `virtnoded`, the viewer encryption proxy, a Tor client and Android in a container. Tetragon is planned. |
| **`virtnoded`** | Runtime VM | Android lifecycle, viewing, app installation, location and timezone, Tor routing, Sia snapshots and cleanup. |
| **Android runtime** | Runtime VM | Currently stock ReDroid 14, with F-Droid installed at first start and a Virtroid agent forwarding notification metadata. A debloated build of our own, based on LineageOS, is planned. |
| **Sia network** | Independent storage hosts | Stores encrypted snapshot chunks as 30 shards, any 10 of which reconstruct them. |

### Runtime VM image

The runtime image includes the entire Linux VM:

- **Verified, read-only root.** Built with Nix and checked block by block using **dm-verity**. The root hash is part of the measured boot chain, so changing the image changes the measurement checked by the phone.
- **Hardened kernel.** Includes Android’s Binder driver and refuses to load kernel modules.
- **Guest services.** Runs `virtnoded`, the attestation agent, the viewer encryption proxy, Docker for the Android container and Tor for runtimes that route through it.
- **Tor egress.** Each runtime that uses Tor gets its own network bridge. The VM’s firewall sends everything from it to Tor and drops what Tor cannot carry (UDP and IPv6), so nothing leaves directly even if Tor stops. Such a runtime reaches nothing else on the VM, and no runtime can reach `virtnoded`’s private port.
- **Planned process enforcement.** Tetragon will monitor processes and block executables outside its allowlist.
- **Encrypted writable storage.** Writable state is confined to the VM’s data disk, whose active encryption key is held in memory.
- **No embedded user data.** Runtime data is restored from Sia at startup.
- **Measured container storage.** The Docker store is rebuilt from the measured image on every boot.

The phone accepts only image measurements listed in an allowlist signed with Virtroid’s **offline release key**. Its public key is embedded in the app.

Images are reproducibly built so others can rebuild them and compare measurements. Approving a new image requires a new signed list; revoking an image requires a signed list that excludes it.

### Viewer and media

The screen is captured inside Android and encoded as **H.264**. A proxy inside the VM encrypts it for the phone’s session key; the phone decrypts and decodes it.

The same encrypted session carries:

- Touch and keyboard input
- Clipboard content
- Audio playback
- On-demand microphone input
- Camera-photo imports

Photos and imported files are sealed for the VM. The control plane cannot read them or substitute its own imports.

Capture runs as Android’s **`system` user**, allowing protected screens—including apps with screenshot blocking, such as SimpleX Chat—to appear in the remote viewer. The phone acts as the runtime’s screen, while **Block Screen Capture** protects the client’s own viewer from local screenshots and recent-app previews.

### Placement and regions

Each account has a region. Its VM is never shared with another account.

| Region | Deployment |
| :--- | :--- |
| **Europe** | Live SEV-SNP VMs on Aleph Cloud, paid per hour. |

The provisioner supports warm spare VMs to reduce first-start time.

### App catalogue and installation

Each runtime maintains its own app selection, chosen during creation or changed later through **Controls → Apps**.

- **Hourly catalogue updates.** The control plane follows F-Droid’s index and verifies its signature against F-Droid’s published repository key.
- **Pinned downloads.** Every APK is pinned by **SHA-256**.
- **Paged browsing.** Catalogue pages use server-side search.
- **One-time installation.** The node records installed selections in the runtime’s own data. An app you uninstall stays uninstalled until selected again.
- **Direct F-Droid access.** F-Droid inside the runtime can install additional apps without a detour through Android’s settings.

### Payment processing

1. **Deposit.** Each account receives its own USDT (TRC20) address.
2. **Confirm.** Deposits are checked for confirmation and verified against a second TRON node.
3. **Credit once.** Confirmed deposits are credited once to the account’s USDT balance.
4. **Charge usage.** Runtime hours and GB-month storage charges are deducted from that balance.
5. **Fund infrastructure.** Every 30 minutes, deposit addresses holding 20 USDT or more are swept into a treasury. When a balance runs low, the treasury swaps USDT through **SimpleSwap**, sending only to addresses fixed in the service’s configuration:
   - **Siacoin** for the Sia indexer, when it holds 10% or more below its 20,000 SC target
   - **ETH** from which the provisioner buys Aleph credits (keeping some for gas), when they cover less than about a week of recent spending
   - **TRX** for transaction fees, when the treasury runs low

Each swap is between 25 and 200 USDT, and at most 500 USDT leaves for swaps in a day. Every sweep and transfer is built and signed inside the isolated payments service. SimpleSwap sees each swap’s amounts and addresses. Details: [`deploy/vps/payments.md`](deploy/vps/payments.md).

---

## Security model

### Protection boundaries

| Boundary | Protection |
| :--- | :--- |
| **Handset** | Runtime app databases and files remain remote. Device signing keys use Android’s hardware-backed Keystore; the account key is password-wrapped; local logs, settings and session handles can be protected by the app’s encrypted PIN/biometric vault. |
| **Data in use** | SEV-SNP encrypts guest memory with a key unavailable to the host. The phone verifies the VM’s attestation and approved image before viewing. |
| **Data in transit** | The viewer is encrypted between the phone and a proxy inside the VM. The edge and relays see ciphertext. Control requests are signed and replay-protected. |
| **Data at rest** | Snapshots are encrypted inside the VM using account-derived keys and distributed across Sia hosts. The VM's working disk is encrypted with a key made inside it at each boot. Unused compute VMs and their disks are deleted. |
| **Notifications** | App name, time and title are encrypted inside the VM for linked phones. Message bodies are excluded, and the control plane sees ciphertext; it refuses readable titles from a confidential VM. |
| **Location** | A runtime's chosen place and timezone are sealed on the phone for the VM. The control plane stores ciphertext, and runtime logs name neither. |
| **Runtime egress** | A runtime can send its own traffic through Tor. The VM's firewall then lets out only what Tor carries (TCP and DNS), even if Tor stops; the phone's viewer link stays direct. |

A forensic extraction of the handset—at a border, by police or by someone with extraction tools—targets the client and its local state rather than a local copy of the remote Android runtime.

The **device key is non-exportable and signs only while the phone is unlocked**. The **identity password is neither stored on the phone nor sent to the service**.

These protections still depend on the phone’s security while it is unlocked and displaying runtime content.

### What you still trust

- **Image builders and signers.** They determine the approved runtime software. Reproducible builds allow independent measurement comparisons. The signed approval list expires after 60 days, so a server cannot keep serving one that still approves a revoked image.
- **The provisioner.** It holds each VM’s node identity key and releases it only after attestation to an approved image. It never holds the VM’s disk key or a runtime’s keys.
- **The control plane.** It decides placement and sees account, runtime, timing and connection-IP metadata.
- **AMD SEV-SNP and host firmware.** Their security against firmware flaws and side channels remains part of the trust boundary. **FABRICKED** (CVE-2025-54510, AMD-SB-3034) lets a host with unpatched firmware read a VM’s memory and forge its attestation report. The approval list sets minimum firmware per processor (on Genoa, SNP firmware 28, which carries AMD’s fix), and phones check the report’s firmware against AMD’s certificate for that chip. EPYC Milan, whose attestation can be forged, is refused.
- **The runtime’s Android build.** Runtimes use stock ReDroid 14, whose Android security patch level is **May 2024**. Apps inside a runtime could exploit Android flaws fixed since then. A current build is planned (see [Remaining work](#remaining-work)).
- **Tor, for runtimes that use it.** As with any Tor use, exit relays see which ordinary (non-`.onion`) sites the runtime connects to, and the content of any traffic not protected by TLS. Sites see a Tor exit instead of the VM’s address.
- **Your unlocked phone.** Malware on it can see what the viewer displays.
- **Your identity password.** It protects the wrapped account key using **Argon2id (64 MiB, 3 passes)**. The server hands the wrapped key only to a phone that proves the password with a separate login key, and pauses recovery for an hour after ten wrong attempts. Someone holding the server’s database could still attempt offline guesses; a weak password remains vulnerable. Accounts created before app 0.4.5 move from PBKDF2 to Argon2id the next time their password is entered.

<summary><strong>Implemented security controls</strong></summary>

### Request authentication and authorization

- **P-256 signatures** for devices, nodes, capabilities and callbacks.
- Timestamp, nonce, body-integrity and replay validation.
- Runtime- and session-scoped capabilities with expiry.
- Bounded resource quotas and explicit lifecycle cleanup obligations.

### Runtime and transport verification

- SEV-SNP attestation checked by the provisioner before releasing VM secrets and by the phone before streaming.
- Offline-signed approved image measurements that expire, with the verification key and a current list built into the app.
- Minimum AMD firmware per processor, checked against the chip’s AMD certificate; EPYC Milan refused.
- TLS viewer transport with session-bound relay credentials.
- Remote-node connections restricted to **operator-allowlisted IP addresses**, with TLS pinned to each node’s reported key for control-plane calls and phone streams.
- Viewers must prove possession of the runtime’s key, preventing the control plane from watching or controlling the runtime.
- Private sockets for viewer hops inside Android.
- Snapshot keys sealed only to a freshly attested VM, limited to the requested runtime’s keys.
- A runtime’s location sealed on the phone for the VM; the control plane stores ciphertext.
- Per-runtime Tor egress whose firewall lets out only what Tor carries, even if Tor stops, with fresh circuits at each start.

### Client identity and local storage

- Android Keystore-backed client secrets that sign only while the phone is unlocked.
- Encrypted local state.
- Identity password changes authorized by the account’s recovery-key signature.
- A separate notification relay key that works while the phone is locked and is accepted only for the notification stream.

### Snapshot and application integrity

- AES-256-CTR with HMAC-SHA256 snapshot encryption.
- Authenticated manifests, whose generations the VM keeps above the newest each phone has seen.
- Account keys wrapped under the user’s identity password with Argon2id.
- F-Droid index-signature verification and SHA-256 pinning for every APK.

### Payments and infrastructure

- Deposit addresses derived from a public key.
- Wallet seed held only by the isolated payments service, which signs its reports.
- Treasury sweeps and swaps signed inside that service, sent only to configured addresses, with per-swap and daily caps.
- Loopback-bound services and deny-by-default firewalling.
- AppArmor, Auditd, Fail2ban and unattended security updates.
- Falco and Suricata event collection, with sanitized, account-scoped client delivery.
- Protected offline release builds, immutable image and deployment-tree verification, and CI rejection of committed binaries.


> [!CAUTION]
> **Virtroid is not an anonymity network.** The control plane and runtime VM can see the phone’s connection IP address, and USDT deposits are public on TRON. Routing a runtime through Tor hides the VM’s address from the sites its apps visit; it does not hide your phone from Virtroid, because the viewer link stays direct. Use a VPN or Tor on the phone if concealing that connection matters to you.

---

## Latency

Viewer responsiveness depends mainly on the network round trip between the phone and VM.

| Factor | Effect |
| :--- | :--- |
| **Distance** | Input travels to the VM and the updated screen returns, costing a full round trip. Fibre adds roughly **1 ms per 100 km of route**, and network routes are longer than straight-line distances. A distant VM can add about a quarter-second before other processing; a regional VM can reduce this to tens of milliseconds. |
| **Video processing** | Encoding in the VM and decoding on the phone add roughly **1–2 frames**, or **30–60 ms at 30 fps**. |
| **Transport** | TCP packet loss can stall later data for a round trip, producing a hitch. The current implementation uses BBR and bounded queues. QUIC remains an unscheduled option for faster reconnects and network changes. |
| **Rendering** | The deployed confidential VMs have no GPU, so Android renders on the CPU. Heavy apps and animations can drop frames. |

From **Bangkok**, the reported round trip to a Europe VM is approximately **230 ms**. A Singapore server is expected to reduce that to approximately **30–50 ms**; the South-East Asia region is not yet deployed.

A closer region removes geographic delay. Changing transport does not.

---

## Repository map

| Path | Purpose |
| :--- | :--- |
| [`android-client/`](android-client/) | Android controller, runtime agent, scrcpy integration, tests and signing gates. |
| [`operator-console/`](operator-console/) | The Observatory: React operator console, built into each control-plane release. |
| [`backend/cmd/`](backend/cmd/) | Control plane, node agent, provisioner, payments, measurement signing, administration, viewer encryption and sensor entry points. |
| [`backend/internal/`](backend/internal/) | Identity, policy, billing, persistence, lifecycle, app catalogue, Sia, TRON, security and operator API logic. |
| [`deploy/vps/`](deploy/vps/) | Reproducible control-plane deployment, hardening, HAProxy, Falco, Suricata, Sia indexer, payments and release tooling. |
| [`deploy/aleph/`](deploy/aleph/) | Confidential runtime VM image, kernel and provisioning guide. |
| [`deploy/provisioner/`](deploy/provisioner/) | Provisioner image containing `virtroid-provisioner` and Aleph’s `aleph` CLI. |
| [`deploy/aosp/`](deploy/aosp/) | Plan and keep-and-cut list for the custom Android image based on LineageOS. |
| [`assets/`](assets/) | Architecture diagram images and their Archify source. |
| [`third_party/`](third_party/) | Reviewable vendored source (the scrcpy server, ReDroid’s container patches ported to LineageOS), provenance and upstream notices. |
| [`.github/workflows/`](.github/workflows/) | Backend, Android, deployment and code-scanning CI checks. |

---

## Progress


### Live capabilities

| Area | Status | Details |
| :--- | :---: | :--- |
| **Identity and recovery** | ✅ Live | Sign-up with automatically issued invitations, device signing, linked phones, identity password changes, optional fingerprint unlock of the account key, account deletion and replacement-phone recovery. Verified on a real phone. |
| **Confidential runtime VMs** | ✅ Live | Image 15. Per-account SEV-SNP VMs on Aleph Cloud in Europe, created on demand from measured, verified-root images and deleted 30 minutes after last use. Once the compute node has the image, attestation and unlocking take **30–90 seconds**. |
| **Attestation and signed images** | ✅ Live | Verifies each VM against AMD’s root and the offline-signed image list. |
| **Encrypted Sia storage** | ✅ Live | **50 mainnet storage contracts**. A real runtime’s **590 MB** snapshot saves in approximately **30 seconds**. |
| **Restore onto a new VM** | ✅ Live | Verified on a real phone after deleting the old VM. Android was running **27 seconds after start began**; the viewer opened approximately **1 minute 40 seconds after VM creation**. |
| **Runtime lifecycle** | ✅ Live | Create, start, stop, restore, fresh personas (new app device IDs only on **Restart with New Persona**), factory reset, deletion and lost-snapshot handling. |
| **Keep running when closed** | ✅ Live | Per-runtime **30-minute to 1-week** slider, shutdown countdown and **Stop and save now**. |
| **Notifications** | ✅ Live | Metadata-only notifications encrypted inside the VM for account-linked phones. Delivered while the phone is locked; the control plane sees ciphertext. |
| **Location** | ✅ Live | Image 15 and app 0.4.7. Per-runtime place from a built-in city list or coordinates, sealed for the VM. Its timezone applies now; apps will see the place as a GPS position once the custom Android image adds a location provider. |
| **Tor egress** | ✅ Live | Image 15 and app 0.4.7. Per-runtime Tor routing with a firewall kill switch, `.onion` addresses and fresh circuits at each start; the viewer link stays direct. Verified on a real phone. |
| **Apps** | ✅ Live | Approximately **4,400 F-Droid apps**, signed-index catalogue synchronization, paged browsing, per-runtime selection, one-time installs and additional installation through F-Droid. |
| **Remote viewer** | ✅ Live | Encrypted control, audio, on-demand microphone, camera import and direct pinned-TLS streaming with an edge fallback. |
| **Wallet and billing** | ✅ Live | USDT (TRC20) deposits, trial allowance, **0.25 USDT/hour** runtime usage and **0.05 USDT/GB-month** storage. |
| **Treasury and funding** | ✅ Live | Deposits swept into a treasury and swapped through SimpleSwap for Siacoin, ETH for Aleph credits and TRX for fees, when balances run low and within per-swap and daily caps. |
| **Operator console** | ✅ Live | Observatory views for fleet, runtime VMs, payments, Sia, security and release telemetry. |
| **Control-plane hardening** | ✅ Live | Falco, Suricata, firewalling, AppArmor and protected releases. |
| **Database backups** | ✅ Live | Nightly control-plane and `indexd` database backups, sealed with a key derived from the `indexd` recovery phrase and stored on Aleph. [Restoration](deploy/vps/indexd.md#backups) requires only the phrase. |

### Confidential handoffs and viewer — live


- Screen capture runs as Android’s `system` user, allowing screenshot-protected apps such as **SimpleX Chat** to appear in the viewer.
- Viewer hops inside Android use private sockets.
- Snapshot keys are sealed to a VM that has just attested, with access limited to the requested runtime’s keys.
- Notification metadata is encrypted inside the VM.
- Viewers prove possession of the runtime’s key, preventing the control plane from watching or controlling the runtime.
- Imported photos and files are sealed for the VM, preventing the control plane from reading or planting them. Nothing is sent unsealed.
- The VM's working disk, Docker store included, is encrypted with a key made inside the VM at each boot.
- Phones seal the newest snapshot generation they have seen; the VM refuses older snapshots.

### Streaming improvements — live


- **BBR congestion control**
- Direct reads from the viewer’s socket by the node
- Small buffers throughout the streaming path
- Adaptive encoding that lowers bitrate when the connection falls behind and raises it again after recovery
- A session-bar indicator showing how far the picture is behind

The encoder reduces picture quality under pressure instead of allowing video to accumulate in queues.

### Remaining work

| Area | Status | Details |
| :--- | :---: | :--- |
| **Current Android build** | ⏳ Planned | Runtimes use stock ReDroid 14 (Android security patch **2024-05-05**), with F-Droid installed at first start. The replacement is built from **LineageOS 23.2** (Android 16 with monthly security patches), debloated, with a GPS location provider fed by the VM. ReDroid’s container patches have been checked against it and the four that didn’t apply are ported; the first build waits on a build server. See the [plan](deploy/aosp/README.md) and [keep-and-cut list](deploy/aosp/components.md). |
| **Tetragon in runtime VMs** | ⏳ Not built | Planned process monitoring and executable allowlisting. |
| **Off-host append-only logs** | ⏳ Not built | Sensor events currently remain on the control plane. |


> [!WARNING]
> **Active development.** Security properties, interfaces, schemas and deployment procedures may change.
>
> Before using Virtroid for high-risk or production-sensitive workloads, independently review the source, deployed configuration, threat model, recovery design, runtime-host trust and storage limitations.

---

## Building from source

### Requirements

| Component | Requirements |
| :--- | :--- |
| **Backend** | Go **1.27.3** or later; PostgreSQL **18** for integration tests |
| **Android client** | JDK **17** and Android SDK with **compile SDK 36** |
| **Operator console** | Node.js and npm |
| **Runtime VM image** | Nix on Linux with `/dev/kvm` |

### Backend

```bash
cd backend
go build ./cmd/...
go test -p 1 ./...
```

Database-dependent tests run only when **`VIRTROID_TEST_DATABASE_URL`** points to an empty PostgreSQL database. Otherwise, they are skipped.

Keep **`-p 1`**: test packages share the database schema.

To run a local control plane for a debug client:

```bash
cd backend
APP_ENV=development BIND_ADDR=127.0.0.1:8080 BOOTSTRAP_REQUIRE_INVITE=false \
  PUBLIC_BASE_URL=http://10.0.2.2:8080 APP_CATALOG_SYNC_ENABLED=false \
  DATABASE_URL='postgres://virtroid:virtroid@127.0.0.1:5432/virtroid?sslmode=disable' \
  go run ./cmd/virtroidd
```

### Android client

Run unit tests, lint and a debug build:

```bash
cd android-client
./gradlew :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
```

Debug builds use the **production control plane by default**. Override it with **`VIRTROID_DEBUG_CONTROL_PLANE_URL`**.

For an Android emulator connecting to the local control plane above:

```bash
VIRTROID_DEBUG_CONTROL_PLANE_URL=http://10.0.2.2:8080 ./gradlew :app:assembleDebug
```

Release builds require Virtroid’s maintainer-held release signing key. Gradle rejects a release build if:

- The signing key is missing
- The control plane does not use HTTPS
- The manifest fails its security checks

### Operator console

```bash
cd operator-console
npm ci
npm run build
```

### Runtime image and deployment guides

| Guide | Covers |
| :--- | :--- |
| [`deploy/aleph/README.md`](deploy/aleph/README.md) | Building, measuring, publishing and approving the runtime VM image |
| [`deploy/vps/README.md`](deploy/vps/README.md) | Control-plane deployment, hardening and releases |
| [`deploy/vps/indexd.md`](deploy/vps/indexd.md) | Sia indexer operations |
| [`deploy/vps/payments.md`](deploy/vps/payments.md) | Payments service operations |
| [`third_party/scrcpy-server/`](third_party/scrcpy-server/) and [`backend/third_party/`](backend/third_party/) | Screen-capture server source, provenance and rebuild verification |

---

<p align="center">
  <img src="android-client/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png" width="128" alt="Virtroid logo">
</p>
