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
</p>

<p align="center">
  <a href="https://github.com/mattyperrott/Virtroid/releases/latest"><strong>Download the Android client</strong></a>
</p>

<p align="center">
  <a href="#why-virtroid">Why Virtroid</a> ·
  <a href="#get-started">Get started</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#security-model">Security</a> ·
  <a href="#performance">Performance</a> ·
  <a href="#status">Status</a> ·
  <a href="#building-from-source">Build</a>
</p>

<img width="1788" height="944" alt="The Virtroid Android client on two phones, beside the Observatory operator console on a laptop" src="https://github.com/user-attachments/assets/e9cace37-5f98-4384-a01a-dddc96e5b4d2">

Virtroid runs your Android apps in the cloud instead of on your phone. Each account gets its own confidential virtual machine, protected by **AMD SEV-SNP**. Your phone checks the VM's hardware attestation, then shows its screen and sends your touches and typing. The apps, their accounts, messages and files stay in the runtime.

When a runtime stops, the VM encrypts its data and saves it across independent **Sia** storage hosts, and once the VM is unused it is deleted. The next start restores the data, on the same VM if it is still there or on a new one. You decide when a runtime starts, how long it keeps running after you close the viewer, which apps it has, and when to reset or delete it.

---

## Why Virtroid

- **Your apps' data isn't on your phone.** The phone holds the Virtroid client, its keys and its settings, not your apps' databases and files. A forensic extraction of the phone finds the client, not your runtime.
- **It runs only when you need it.** Close the viewer and the runtime keeps going for as long as you choose, from 30 minutes to a week, so messaging apps keep receiving. Then Android stops and its data is saved.
- **You verify it before you connect.** Your phone checks AMD's attestation and the VM's software against an offline-signed approval list before it shows anything. Images are built reproducibly, so anyone can check what was approved.
- **Storage hosts hold only encrypted shards.** Snapshots are encrypted inside the VM and stored as 30 shards on independent Sia hosts, any 10 of which rebuild the ciphertext. The service can't roll a runtime back to an older snapshot.
- **Apps see the runtime, not your phone.** A generated device persona and the VM's network, or Tor, instead of your handset's identity and IP address, and a place of your choosing ([details](#what-apps-in-a-runtime-see)).
- **No personal details.** An account is a key pair and an identity password: no name, email address or phone number. You pay in USDT.
- **Lose your phone, keep your runtimes.** Recover on another phone with your account ID and identity password, then revoke the lost one.
- **No Google.** Runtimes have no Google apps or services. Install apps from a verified F-Droid catalogue, or with F-Droid inside the runtime.

---

## Get started

### 1. Set up your identity

1. **[Download the Android client](https://github.com/mattyperrott/Virtroid/releases/latest)** and install it on a phone running Android 9 or later. The app offers each new release as an update.
2. **Create your identity.** The app generates your account's key pair and asks for an identity password of at least 14 characters.
3. **Write down your account ID and identity password.** Together they recover your account on another phone. There is no other way back in.
4. **Lock the app (recommended).** In **Privacy & Security**, set an 8-digit PIN, with fingerprint unlock if you like, to keep the app's local state in an encrypted vault.

New accounts get **one runtime and one hour of running time free**.

### 2. Create a runtime

Tap **+** and choose:

| Setting | What it does |
| :--- | :--- |
| **Session Name** | The runtime's name. |
| **Keep running** | How long it keeps running after you close the viewer: 30 minutes to 1 week. |
| **Pre-install apps** | Up to 32 apps from F-Droid, installed when it starts. |
| **Location** | A city from the built-in list, coordinates, or nothing. The runtime takes the place's timezone. |
| **Route through Tor** | Sends the runtime's own traffic through Tor. Slower, and calls, video chat and some sites won't work. |
| **Audio Passthrough** | Plays the runtime's sound on your phone. |
| **Host Physical Camera** | Lets you take photos and short videos with your phone's camera and import them into the runtime. |

The **ⓘ** on the storage card explains how the runtime's data is stored.

### 3. Start it

- **Start Session** prepares a VM, verifies it and opens the viewer. A first start usually takes a minute or two. Starting needs your account key, which your identity password unlocks (or your fingerprint, if you turn that on).
- **Use it like a phone.** Touch, type and listen. The microphone turns on only when an app in the runtime asks for it.
- **Bring files in.** Import a photo, video or file from your phone.
- **Close the viewer** and the runtime keeps running for its **Keep running** period. Messaging apps keep receiving, and your phone can show their notifications: the app name, time and title, encrypted end to end, never the message body.

### Runtime controls

Open a runtime's **Controls**:

| Control | What it does |
| :--- | :--- |
| **Apps** | Change its F-Droid apps. |
| **Keep running when closed** | Change the period and see the countdown to shutdown. |
| **Stop and save now** | Shut Android down and save its data now. |
| **Location** | Change or turn off its place. |
| **Route through Tor** | Turn Tor on or off while it's stopped. The change applies from the next start. |
| **Restart with New Persona** | Save, then restart as a new device, with new app device IDs. Some apps may ask you to sign in again. |
| **Display & Output** | Its screen size and density. |
| **Console Logs** | Its boot and network events. |
| **Build Identity** | The device model and Android version it presents. |
| **Factory Reset Runtime** | Erase its saved data but keep the runtime. |
| **Wipe & Delete Runtime** | Erase its saved data and remove it. |

### Wallet and pricing

**Wallet** shows your account's USDT deposit address on TRON (TRC20), your balance and its activity. Charges come out of the balance.

| Item | Price |
| :--- | :--- |
| New account | One runtime and one hour of running time, free |
| Running time | **0.25 USDT** per hour |
| Stored data | **0.05 USDT** per GB-month |

Running time counts whenever a runtime runs, including its keep-running period after you close the viewer. When the free hour or your balance runs out, running runtimes are stopped and saved.

### Phones and recovery

On a new or replacement phone, choose **Recover Account** and enter your account ID and identity password. The phone makes its own signing key; the old phone isn't needed.

In **Account Identity**:

- **Linked Devices** lists every phone on your account. Revoke any you no longer have.
- **Change password** changes your identity password but not the account key, so runtimes and snapshots are unaffected.
- **Unlock with fingerprint** opens the account key on this phone with your fingerprint instead of the password. That copy of the key is destroyed if a fingerprint is added to the phone, and dropped when you sign out.
- **Delete Account** permanently removes your runtimes and their encrypted data, linked phones, the identity backup, and your wallet balance and history.

### Privacy settings

In **Privacy & Security**:

| Setting | What it does |
| :--- | :--- |
| **Block Screen Capture** | Keeps the viewer out of screenshots and the recent-apps preview. |
| **Close Viewer When Idle** | Closes the viewer after a while without input. The runtime keeps running. |
| **Auto-clear Clipboard** | Clears the phone's clipboard when a session ends. |
| **Require unlock on app resume** | Asks for the PIN whenever you return to the app. |
| **Auto-lock Timer** | Sets when the app locks. |
| **Force Clear Local Cache Now** | Deletes the app's cached files on the phone. |
| **What the operator can see** | Lists the metadata the service can and can't see. |

---

## How it works

### A runtime's life

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-lifecycle-dark.png">
  <img alt="Runtime lifecycle: create; start on a confidential VM and restore from Sia; verify and use; keep running after closing the viewer; stop and save encrypted data to Sia; destroy the unused VM; start again. Wipe clears saved data, and delete removes the runtime." src="assets/architecture/virtroid-lifecycle-light.png">
</picture>

1. **Create.** Choose a name, a keep-running period, apps, a place and whether to use Tor. Nothing runs yet.
2. **Start.** The account's VM is reused, or a new one is created and verified ([Starting a session](#starting-a-session)). The runtime's saved data is restored inside it before Android boots, with a fresh device persona, its place and, if chosen, Tor on new circuits. Selected apps are installed once.
3. **Use.** The phone streams the screen over an end-to-end encrypted channel.
4. **Close.** The runtime keeps running for its period. Apps keep receiving messages and sending notification metadata.
5. **Stop and save.** When the period ends, or you choose **Stop and save now**, Android shuts down. Its data is encrypted, saved to Sia and removed from the VM.
6. **Retire the VM.** Once none of the account's runtimes is using the VM, the provisioner deletes it and its encrypted disk: after 30 minutes on the live deployment. The next start restores the data onto a new VM.

**Factory Reset** clears a runtime's saved data, and **Wipe & Delete** also removes the runtime. Saved snapshots are kept on Sia, not on the machine that ran the runtime.

### Starting a session

| Step | What happens |
| :--- | :--- |
| **1. Request** | The phone signs a start request with its Android Keystore key and sends it to the edge, the public API entry point. |
| **2. Check** | `virtroidd` verifies the signature, the account and its credit. |
| **3. Provision** | The provisioner reuses the account's VM, assigns a warm spare, or creates a new SEV-SNP VM on Aleph Cloud in the account's region. |
| **4. Unlock** | The provisioner verifies the VM's attestation before sending it its node identity. The VM encrypts its working disk with a key it makes at each boot, which never leaves it. |
| **5. Hand over keys** | The phone checks a fresh attestation report against AMD's certificates and the signed approval list, then seals the runtime's keys, with the newest snapshot generation it has seen, to a key that exists only in the VM's memory. |
| **6. Restore** | The VM fetches the latest snapshot from Sia, checks it, decrypts it and starts Android. |
| **7. Stream** | The phone checks that the VM's TLS key is the one bound into its attestation report, then streams over TLS pinned to that key. Without a direct route, the stream goes through the edge, still end-to-end encrypted. |

Saving is the reverse: on stop, the VM encrypts the runtime's data and uploads it to Sia.

### Snapshots and storage

A snapshot is the runtime's whole `/data`: apps, app data, accounts and files.

| Layer | How it works |
| :--- | :--- |
| **Encryption** | Inside the VM, the snapshot is compressed, then encrypted with AES-256-CTR and HMAC-SHA256 (encrypt-then-MAC). Its keys derive from a per-runtime key, which derives from the account key, with a fresh salt for every snapshot. |
| **Manifest** | Lists the snapshot's 4 MiB chunks, their hashes and its generation, authenticated with the account key, so neither storage hosts nor the control plane can substitute, reorder or truncate chunks. |
| **Rollback protection** | Each phone records the newest generation it has seen for each runtime and seals it, with the runtime's keys, to the VM. The VM refuses older or unauthenticated snapshots and numbers new ones above it, so a wipe or a lost snapshot never brings an older one back. A phone with no record yet (new or recovered) accepts the newest authenticated snapshot. |
| **Distribution** | The Sia indexer, `indexd`, gives each account its own keys. Each chunk becomes 30 shards on independent hosts, any 10 of which rebuild it. The indexer sees opaque object names and sizes. |
| **Restore** | The VM fetches the latest snapshot and checks its authentication tags before decrypting anything. |

Rebuilding a snapshot from its shards gives only ciphertext. Decrypting it needs the account key, which your identity password protects.

### What apps in a runtime see

- **A device persona.** Each start presents a fresh device model and build fingerprint. Apps keep their device IDs across ordinary starts, so they stay signed in; **Restart with New Persona** gives every app new ones. Your accounts and restored app data can still link sessions.
- **The VM's network.** Apps use the VM's datacenter connection, not your phone's IP address or carrier. With **Route through Tor**, the runtime's own traffic goes through Tor, including `.onion` addresses, on fresh circuits at each start, and nothing else gets out, even if Tor stops. The viewer's link to your phone stays direct.
- **A place you choose.** Give a runtime a city or coordinates and it takes that place's timezone. The choice is sealed on your phone for the VM, so the service can't read it. Apps don't yet see the place as a GPS position; our own Android image adds that ([in progress](#in-progress-our-own-android-image)).

### Viewer and media

Android's screen is captured inside the runtime and encoded as H.264. A proxy inside the VM encrypts it for the phone's session key, and the phone decrypts and decodes it. The same encrypted session carries touch and keyboard input, the clipboard, audio, the microphone (only on request) and imports. Imported photos and files are sealed for the VM, so the control plane can neither read them nor plant its own.

Capture runs as Android's `system` user, so apps that block screenshots, such as SimpleX Chat, still appear in the viewer: the phone is the runtime's screen. On the phone, **Block Screen Capture** keeps the viewer itself out of screenshots and the recent-apps preview.

### Apps

Each runtime has its own app selection, set when you create it or later in **Controls → Apps**.

- **A verified catalogue.** The control plane follows F-Droid's index every hour, checks its signature against F-Droid's published repository key, and pins every APK by SHA-256. You browse it page by page, with search on the server.
- **Installed once.** The node installs each selected app once and records that in the runtime's own data, so an app you uninstall stays uninstalled until you select it again.
- **More through F-Droid.** F-Droid inside the runtime installs other apps directly.
- **No Google.** Apps that insist on a certified Google device won't run. ARM-only apps, such as SimpleX, run through Google's ARM translation, a closed binary.

### Payments

Each account gets its own USDT (TRC20) deposit address, derived from a public key, so the control plane holds no private keys. An isolated payments service, the only holder of the wallet seed, checks each deposit against a second TRON node and credits it once, then sweeps deposits into a treasury. When the service's own balances run low, the treasury swaps USDT through **SimpleSwap** for what the service runs on: Siacoin for storage, ETH for Aleph compute credits and TRX for fees. It sends only to addresses fixed in its configuration, 25 to 200 USDT a swap and no more than 500 USDT a day, and signs every sweep and swap itself. SimpleSwap sees each swap's amounts and addresses. Thresholds and operations: [`deploy/vps/payments.md`](deploy/vps/payments.md).

---

## Architecture

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/architecture/virtroid-target-dark.png">
  <img alt="Virtroid architecture: the phone sends signed requests through the edge; virtroidd checks credit; the provisioner prepares an account-dedicated SEV-SNP VM and releases its node key only after attestation; the phone verifies the VM and streams from it; encrypted snapshots move through the indexer to independent Sia hosts; encrypted notification metadata is relayed to linked phones." src="assets/architecture/virtroid-target-light.png">
</picture>

| Component | Where | What it does |
| :--- | :--- | :--- |
| **Android client** | Your phone | Identity, wallet, runtime controls, local security, attestation checks, the encrypted viewer, media capture and notifications. Holds no runtime app data. |
| **Edge (HAProxy)** | Control plane | The public HTTPS and API entry point, the fallback streaming path, and the Sia indexer's app API. |
| **`virtroidd`** | Control plane | Accounts, devices, policy, billing, placement, runtimes, capabilities, sessions, the app catalogue, notification delivery and the operator console (the Observatory). |
| **PostgreSQL** | Control plane | Authoritative account, billing and lifecycle state. |
| **Provisioner** | Control plane | Creates each account's SEV-SNP VM on Aleph Cloud, verifies its attestation before releasing its secrets, keeps warm spares and deletes unused VMs. |
| **Payments service** | Control plane | The only holder of the deposit wallet's seed. Credits deposits, then sweeps and swaps them as [above](#payments). |
| **Sia indexer (`indexd`)** | Control plane | Gives each account its own Sia keys and pays storage hosts in Siacoin. Sees opaque storage metadata. |
| **Host sensors** | Control plane | Falco and Suricata watch the control plane. Their events stay on it for now. |
| **Runtime VM** | Aleph Cloud, one per account | An SEV-SNP guest booting a measured image: `virtnoded`, the viewer encryption proxy, Tor, and a container for each runtime's Android. |
| **`virtnoded`** | Runtime VM | Android's lifecycle, viewing, app installs, location and timezone, Tor routing, Sia snapshots and cleanup. |
| **Android** | Runtime VM | Android 16 from ReDroid's image (security patch June 2025), with Google's ARM translation added. F-Droid is installed at first start, and a Virtroid agent forwards notification metadata. |
| **Sia network** | Independent hosts | Stores encrypted snapshot chunks as 30 shards, any 10 of which rebuild them. |

### Runtime VM image

Everything a VM boots is part of its SEV-SNP launch measurement, so a phone that accepts the measurement has checked all of it.

- **Read-only root.** Built reproducibly with Nix and verified block by block with dm-verity. Its root hash is in the measured initrd, so any change to the image changes the measurement. There is no SSH, login shell, package manager or compiler.
- **Hardened kernel.** A 6.18 LTS kernel that loads no modules, with what Docker and Android need: Binder; loop devices, and writes to mounted block devices, for Android 16's APEX packages; and ashmem, ported from Android's Common Kernel ([patch](deploy/aleph/kernel-ashmem.patch)).
- **The Android image.** Built by [`deploy/aleph/build-runtime-image.sh`](deploy/aleph/build-runtime-image.sh) from two pinned sources, ReDroid's Android 16 image and the ARM translation from Google's Android 16 emulator image. It rebuilds byte for byte.
- **Services.** `virtnoded`, the attestation agent, the viewer encryption proxy, Docker for the Android containers, and Tor.
- **Tor egress.** Each Tor runtime gets its own network bridge. The VM's firewall sends its DNS and TCP to Tor and drops everything else (UDP and IPv6), so nothing leaves directly even if Tor stops. A Tor runtime reaches nothing else on the VM, and no runtime can reach `virtnoded`'s private port.
- **Nothing kept between boots.** Writable state lives in memory and on the VM's data disk, encrypted with a key the VM makes at each boot. The Docker store is rebuilt from the measured image every boot, and the image carries no user data: runtimes are restored from Sia.

Phones accept only measurements on a list signed with Virtroid's **offline release key**, whose public key is built into the app. Approving a new image takes a newly signed list, and so does revoking one. Lists expire after 60 days. Building, measuring and approving an image: [`deploy/aleph/README.md`](deploy/aleph/README.md).

### Regions

Each account has a region, and its VM is never shared with another account. **Europe** is live, on Aleph Cloud's SEV-SNP hosts, paid by the hour. The provisioner can keep warm spare VMs ready to shorten first starts.

---

## Security model

> [!CAUTION]
> **Virtroid is not an anonymity network.** The control plane and the runtime VM see your phone's IP address, and USDT deposits are public on TRON. Routing a runtime through Tor hides the VM's address from the sites its apps visit; it doesn't hide your phone from Virtroid, because the viewer link stays direct. Use a VPN or Tor on the phone if hiding that connection matters to you.

### What it protects

| Boundary | Protection |
| :--- | :--- |
| **Handset** | Runtime app data stays remote. The device's signing key lives in Android's hardware-backed Keystore, can't be exported, and signs only while the phone is unlocked. The account key is wrapped by your identity password, which is never stored on the phone or sent to the service. Local logs, settings and session handles can be kept in the app's encrypted vault, behind its PIN or your fingerprint. |
| **Data in use** | SEV-SNP encrypts the VM's memory with a key the host can't get. The phone verifies the VM's attestation and approved image before viewing. |
| **Data in transit** | The viewer is encrypted between the phone and a proxy inside the VM; the edge and relays see ciphertext. Control requests are signed and replay-protected. |
| **Data at rest** | Snapshots are encrypted inside the VM with account-derived keys and spread across Sia hosts. The VM's working disk is encrypted with a key made inside it at each boot. Unused VMs and their disks are deleted. |
| **Notifications** | The app name, time and title are encrypted inside the VM for your linked phones; message bodies are left out. The control plane sees ciphertext, and refuses readable titles from a confidential VM. |
| **Location** | A runtime's place and timezone are sealed on the phone for the VM. The control plane stores ciphertext, and runtime logs name neither. |
| **Runtime egress** | With Tor on, the VM's firewall lets out only what Tor carries (TCP and DNS), even if Tor stops. |

A forensic extraction of the phone, whether at a border, by police or by anyone with extraction tools, finds the client and its local state, not a copy of the runtime. All of this still depends on the phone's own security while it's unlocked and showing a runtime.

### What you still trust

- **Image builders and signers.** They decide which runtime software is approved. Reproducible builds let anyone compare measurements, and approval lists expire after 60 days, so a server can't keep serving one that still approves a revoked image.
- **The provisioner.** It holds each VM's node identity key and releases it only to a VM that attests to an approved image. It never holds the VM's disk key or a runtime's keys.
- **The control plane.** It decides placement and sees account, runtime, timing and connection-IP metadata.
- **AMD SEV-SNP and host firmware.** Firmware flaws and side channels remain part of the trust boundary. **FABRICKED** (CVE-2025-54510, AMD-SB-3034) lets a host with unpatched firmware read a VM's memory and forge its attestation report. The approval list sets a minimum firmware per processor (on Genoa, SNP firmware 28, which carries AMD's fix), and phones check the report's firmware against AMD's certificate for that chip. EPYC Milan, whose attestation can be forged, is refused.
- **The runtime's Android.** Runtimes run ReDroid's Android 16 image, whose security patch level is **June 2025**, and ReDroid publishes no newer one, so apps in a runtime could exploit Android flaws fixed since. Android runs in a privileged container with SELinux off, as stock ReDroid does, so its app sandbox rests on Linux users, seccomp and the container. ARM-only apps run through Google's closed ARM translation. Our own image brings monthly patches, and a narrower container is part of its hardening ([in progress](#in-progress-our-own-android-image)).
- **The other runtimes in your account.** They share the account's VM, so no VM boundary separates them from each other.
- **Tor, for runtimes that use it.** As with any Tor use, exit relays see which ordinary (non-`.onion`) sites the runtime connects to, and the content of any traffic not protected by TLS. Sites see a Tor exit instead of the VM's address.
- **Your unlocked phone.** Malware on it can see what the viewer shows.
- **Your identity password.** It protects the wrapped account key with **Argon2id (64 MiB, 3 passes)**. The server hands the wrapped key only to a phone that proves the password with a separate login key, and pauses recovery for an hour after 10 wrong attempts in a row. Someone holding the server's database could still guess offline, so a weak password stays weak. Accounts created before app 0.4.5 move from PBKDF2 to Argon2id the next time their password is entered.

<details>
<summary><strong>Security controls in place</strong></summary>

<br>

**Requests**

- P-256 signatures for devices, nodes, capabilities and callbacks.
- Timestamp, nonce, body-integrity and replay checks.
- Capabilities scoped to a runtime or session, with expiry.
- Bounded resource quotas and explicit cleanup obligations.

**Runtimes and transport**

- SEV-SNP attestation, checked by the provisioner before it releases a VM's secrets and by the phone before it seals keys or streams.
- Approved image measurements on an offline-signed list that expires; the app carries the verification key and a current list.
- Minimum AMD firmware per processor, checked against the chip's AMD certificate; EPYC Milan refused.
- Connections to nodes only at operator-allowlisted IP addresses, with TLS pinned to each node's reported key for control-plane calls and phone streams.
- Session-bound relay credentials for the fallback path.
- Viewers must prove they hold the runtime's key, so the control plane can't watch or control a runtime.
- Private sockets for the viewer's hops inside Android.
- Snapshot keys sealed only to a freshly attested VM, limited to the keys of the runtime asked for.
- Locations sealed on the phone for the VM.
- Tor egress per runtime, with a firewall kill switch and fresh circuits at each start.

**Phones and keys**

- Android Keystore-backed client keys that sign only while the phone is unlocked.
- Encrypted local state.
- Identity-password changes authorized by the account's recovery-key signature.
- A separate notification key that works while the phone is locked and is accepted only for the notification stream.
- Account keys wrapped under the identity password with Argon2id.

**Snapshots and apps**

- AES-256-CTR with HMAC-SHA256 snapshot encryption.
- Authenticated manifests, whose generations the VM keeps above the newest each phone has seen.
- F-Droid index signatures verified, and every APK pinned by SHA-256.

**Payments and infrastructure**

- Deposit addresses derived from a public key; the wallet seed held only by the isolated payments service, which signs its reports.
- Treasury sweeps and swaps signed inside that service, sent only to configured addresses, within per-swap and daily caps.
- Loopback-bound services and deny-by-default firewalls.
- AppArmor, auditd, Fail2ban and unattended security updates.
- Falco and Suricata event collection, sanitized and scoped to the account before reaching a client.
- Protected offline release builds, immutable image and deployment-tree verification, and CI that rejects committed binaries.

</details>

---

## Performance

How responsive the viewer feels depends mostly on the round trip between your phone and the VM.

| Factor | Effect |
| :--- | :--- |
| **Distance** | Every touch travels to the VM and the new picture travels back: a full round trip. Fibre adds about 1 ms per 100 km of route, and routes are longer than the straight line. A distant VM can add about a quarter of a second before anything else; a VM in your region brings that down to tens of milliseconds. |
| **Video** | Encoding in the VM and decoding on the phone add about 1–2 frames: 30–60 ms at 30 fps. |
| **Transport** | Streaming uses TLS over TCP, with BBR congestion control and small buffers throughout. A lost packet can still hold up later data for a round trip, which shows as a hitch. |
| **Rendering** | The confidential VMs have no GPU, so Android renders on the CPU. Heavy apps and animations can drop frames. |

From **Bangkok**, the round trip to a Europe VM is about **230 ms**. A Singapore server would bring that to about **30–50 ms**, but there is no South-East Asia region yet. A closer region removes geographic delay; a different transport doesn't.

When the connection falls behind, the encoder lowers the bitrate instead of letting video queue up, and raises it again once the connection recovers. The session bar shows how far behind the picture is.

Measured on the live deployment:

| Measurement | Result |
| :--- | :--- |
| Attestation and unlocking of a new VM, once its host has the image | 30–90 seconds |
| Restore onto a new VM | Android running 27 seconds after the start began; the viewer open about 1 minute 40 seconds after the VM was created |
| Saving a real runtime's 590 MB snapshot to Sia | About 30 seconds |

---

## Status

_Updated 27 October 2026._

### Live

| Area | Details |
| :--- | :--- |
| **Identity and recovery** | Sign-up with automatically issued invitations, device signing, linked phones, password changes, fingerprint unlock, account deletion and recovery on a replacement phone. |
| **Confidential runtime VMs** | Image 17. Per-account SEV-SNP VMs on Aleph Cloud in Europe, created on demand from measured images and deleted 30 minutes after last use. |
| **Android 16 and ARM apps** | Since 22 October 2026: ReDroid's Android 16 with Google's ARM translation, so ARM-only apps such as SimpleX run. Runtimes saved on Android 14 upgrade on their next start. |
| **Attestation and signed images** | Every VM checked against AMD's root and the offline-signed image list. |
| **Encrypted Sia storage** | 50 storage contracts on Sia mainnet. |
| **Runtime lifecycle** | Create, start, stop, restore onto a new VM, keep running when closed, new personas, factory reset, deletion and lost-snapshot handling. |
| **Remote viewer** | Encrypted control, audio, on-demand microphone, photo and file import, and direct pinned-TLS streaming with an edge fallback. |
| **Notifications** | Metadata only, encrypted inside the VM, delivered while the phone is locked. |
| **Location and Tor** | A sealed place per runtime, applied as its timezone, and Tor egress with a kill switch and `.onion` addresses. |
| **Apps** | About 4,400 F-Droid apps from the signed index, chosen per runtime and installed once, plus F-Droid in every runtime. |
| **Wallet and billing** | USDT (TRC20) deposits, the free trial, and charges for running time and storage. |
| **Treasury** | Deposits swept into a treasury and swapped for Siacoin, ETH for Aleph credits, and TRX as balances run low, within caps. |
| **Operator console** | The Observatory: fleet, runtime VMs, payments, Sia, security and release telemetry. |
| **Control-plane hardening** | Falco, Suricata, firewalling, AppArmor and protected releases. |
| **Database backups** | Nightly copies of the control-plane and `indexd` databases, sealed with a key derived from the `indexd` recovery phrase and stored on Aleph. [Restoring](deploy/vps/indexd.md#backups) needs only the phrase. |

### In progress: our own Android image

ReDroid publishes no Android 16 image newer than June 2025's security patch, so we're building our own from **LineageOS 23.2**: Android 16 with the monthly security patches (the current build has October 2026's). ReDroid's container patches are ported to it, and it runs on the same VM kernel. It adds:

- no Google apps or services, and fewer of Android's own: telephony, Bluetooth, printing and other parts a runtime doesn't need are cut ([keep-and-cut list](deploy/aosp/components.md));
- F-Droid, its privileged extension and the Virtroid agent built in, and defaults that keep connectivity checks, time and search off Google's servers;
- a GPS provider that reads the runtime's sealed place from the VM, so apps see an ordinary GPS fix there.

It builds and passes the VM boot test, including SimpleX's ARM build and the upgrade of a runtime saved on today's image, but it isn't live yet: signing with our own release keys, then hardening (a container with fewer privileges, `hardened_malloc`), come first. Plan and progress: [`deploy/aosp/README.md`](deploy/aosp/README.md).

### Planned

| Area | Details |
| :--- | :--- |
| **Open ARM translation** | Try Digitalis, an open-source ARM64 translator, in place of Google's. It replaces Google's only if SimpleX and other ARM-only apps pass the boot test with it. |
| **Tetragon in runtime VMs** | Process monitoring and an allowlist of executables. |
| **Off-host security logs** | Append-only storage away from the control plane. Sensor events stay on it for now. |
| **QUIC** | Unscheduled: faster reconnects and smoother network changes. |

> [!WARNING]
> **Active development.** Security properties, interfaces, schemas and deployment procedures may change.
>
> Before using Virtroid for high-risk or production-sensitive work, review the source, the deployed configuration, the threat model, the recovery design, runtime-host trust and storage limits yourself.

---

## Repository map

| Path | What's there |
| :--- | :--- |
| [`android-client/`](android-client/) | The Android client, the runtime agent, scrcpy integration, tests and release signing gates. |
| [`operator-console/`](operator-console/) | The Observatory, a React operator console built into each control-plane release. |
| [`backend/cmd/`](backend/cmd/) | Entry points: control plane, node agent, provisioner, payments, measurement signing, administration, viewer encryption and sensors. |
| [`backend/internal/`](backend/internal/) | Identity, policy, billing, persistence, lifecycle, app catalogue, Sia, TRON, security and operator API logic. |
| [`deploy/vps/`](deploy/vps/) | Reproducible control-plane deployment, hardening, HAProxy, Falco, Suricata, the Sia indexer, payments and release tooling. |
| [`deploy/aleph/`](deploy/aleph/) | The confidential runtime VM image, its kernel, boot tests and the provisioning guide. |
| [`deploy/provisioner/`](deploy/provisioner/) | The provisioner's image, with `virtroid-provisioner` and Aleph's `aleph` CLI. |
| [`deploy/aosp/`](deploy/aosp/) | Our own Android image: the LineageOS build, device tree, patches and plan. |
| [`assets/`](assets/) | Diagram images and their Archify sources. |
| [`third_party/`](third_party/) | Vendored source to review (the scrcpy server, ReDroid's container patches ported to LineageOS), with provenance and upstream notices. |
| [`.github/workflows/`](.github/workflows/) | CI: backend, Android, deployment and runtime-image checks, and code scanning. |

---

## Building from source

### Requirements

| Component | Requirements |
| :--- | :--- |
| **Backend** | Go **1.27.3** or later; PostgreSQL **18** for integration tests |
| **Android client** | JDK **17** and the Android SDK with **compile SDK 36** |
| **Operator console** | Node.js and npm |
| **Runtime VM image** | Nix on Linux with `/dev/kvm` |
| **Our Android image** | A Linux build host with about 32 GB of RAM (64 GB is comfortable) and 300 GB of disk |

### Backend

```bash
cd backend
go build ./cmd/...
go test -p 1 ./...
```

Database tests run only when **`VIRTROID_TEST_DATABASE_URL`** points to an empty PostgreSQL database; otherwise they're skipped. Keep **`-p 1`**: the test packages share the database schema.

To run a local control plane for a debug client:

```bash
cd backend
APP_ENV=development BIND_ADDR=127.0.0.1:8080 BOOTSTRAP_REQUIRE_INVITE=false \
  PUBLIC_BASE_URL=http://10.0.2.2:8080 APP_CATALOG_SYNC_ENABLED=false \
  DATABASE_URL='postgres://virtroid:virtroid@127.0.0.1:5432/virtroid?sslmode=disable' \
  go run ./cmd/virtroidd
```

### Android client

Unit tests, lint and a debug build:

```bash
cd android-client
./gradlew :app:testDebugUnitTest :app:lintDebug :app:assembleDebug
```

Debug builds use the **production control plane** unless you set **`VIRTROID_DEBUG_CONTROL_PLANE_URL`**. For an Android emulator and the local control plane above:

```bash
VIRTROID_DEBUG_CONTROL_PLANE_URL=http://10.0.2.2:8080 ./gradlew :app:assembleDebug
```

Release builds need Virtroid's maintainer-held release signing key. Gradle refuses a release build if the key is missing, the control plane isn't HTTPS, or the manifest fails its security checks.

### Operator console

```bash
cd operator-console
npm ci
npm run build
```

### Images and deployment

| Guide | Covers |
| :--- | :--- |
| [`deploy/aleph/README.md`](deploy/aleph/README.md) | Building, measuring, publishing and approving the runtime VM image |
| [`deploy/aosp/README.md`](deploy/aosp/README.md) | Building our own Android image from LineageOS |
| [`deploy/vps/README.md`](deploy/vps/README.md) | Control-plane deployment, hardening and releases |
| [`deploy/vps/indexd.md`](deploy/vps/indexd.md) | Running the Sia indexer |
| [`deploy/vps/payments.md`](deploy/vps/payments.md) | Running the payments service |
| [`third_party/scrcpy-server/`](third_party/scrcpy-server/) and [`backend/third_party/`](backend/third_party/) | The screen-capture server's source, provenance and rebuild check |

---

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request, and follow the [code of conduct](CODE_OF_CONDUCT.md). Report vulnerabilities privately, as the [security policy](SECURITY.md) describes, not in public issues. Accessibility reports are welcome: see [ACCESSIBILITY.md](ACCESSIBILITY.md).

Virtroid's own code is source-available under the Apache License 2.0 with the Commons Clause, which restricts selling it, including as a paid hosted service. Third-party parts keep their own licences. [LICENSING.md](LICENSING.md) has the details and covers commercial use.

<p align="center">
  <img src="android-client/app/src/main/res/mipmap-xxxhdpi/ic_launcher.png" width="128" alt="Virtroid logo">
</p>
