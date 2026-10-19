# Confidential runtime VMs on Aleph Cloud

Each account in a provisioned region gets its own AMD SEV-SNP virtual machine
on Aleph Cloud. Its memory is encrypted by the CPU, everything it boots is
measured, and the phone checks the VM's attestation before it streams. This
directory builds what the VMs boot (`nix/`). The provisioner
(`backend/cmd/virtroid-provisioner`) creates and deletes them.

> [!WARNING]
> Running as a pilot: VMs boot on Aleph's SEV-SNP hosts in the `snp-eu` region,
> and phones stream from them after checking their attestation against the
> signed measurement list (step 6). App releases before 0.3.7 do not carry the
> signing key and refuse these VMs.

## What a VM boots

Everything below is part of the SEV-SNP launch measurement, so a phone that
accepts the measurement has checked all of it:

- **Firmware and kernel.** Aleph's measured OVMF and a 6.18 LTS guest kernel
  built from Aleph's minimal option list, plus what Docker and Android need
  (binder, overlayfs, netfilter, DMA-BUF heaps and more, following AOSP's
  android-base.config), EROFS and configfs-tsm (`kernel-virtroid.fragment`).
- **Initrd** (`nix/init-virtroid.sh`, adapted from Aleph's). It holds the root
  disk's dm-verity root hash, so the measurement covers the root too. It
  verifies the root, starts Aleph's attestation agent in owner-auth mode, and
  waits for the provisioner's secrets.
- **Read-only root** (`nix/rootfs.nix`), a zstd-compressed EROFS image built
  reproducibly by Nix, about 720 MiB with its hash tree. It contains:
  - Docker's daemon, containerd and runc (without nixpkgs' image-building
    extras), cryptsetup, nftables and adb;
  - `virtnoded` and its viewer encryption tool;
  - the Android runtime image, loaded at boot and never pulled;
  - the pinned apps.

  There is no SSH, login shell, package manager or compiler. Anyone can
  rebuild it from this repository and get the same root hash.

Writable state lives only in memory and on the VM's own data disk, which the
VM encrypts with a key only it and the provisioner hold. Once booted, the VM
loads no further kernel modules.

## How a VM comes up

1. A user in a provisioned region starts a runtime. `virtroidd` finds no node
   for them, so it claims a warm spare or records a VM request. The app is told
   the server is being prepared and retries.
2. The provisioner runs `aleph instance create` with our runtime, the root
   image, a per-VM data disk, and a compute node from the region's list.
3. The VM boots, verifies its root against the measured hash, and waits.
4. The provisioner runs `aleph instance unlock`. The CLI verifies the VM's
   SEV-SNP report (AMD signature chain, launch measurement, policy) over
   attested TLS, and only then sends the VM's node id and key, region, control
   plane URL and data-disk key. Only the provisioner's address, measured on the
   kernel command line, can inject them. The node key is approved in the
   control plane at the same moment.
5. `/sbin/init` (`nix/vm-init.sh`) does the rest:
   - opens the data disk;
   - allows only ports 8443 (attestation agent) and 9443 (node) inbound;
   - loads the Android image;
   - starts Docker and `virtnoded` with `NODE_SNP_ATTESTATION=required`.
6. `virtnoded` gets an SNP report that binds its TLS key, adds AMD's
   certificates, and heartbeats. The VM is ready.
7. On each session the phone verifies that bundle itself: AMD root built into
   the app, an approved measurement, no debug policy, the TLS key it pins, and
   the viewer key signed by that TLS key. It refuses to connect otherwise.

## What is trusted

| Party | Can it read or change a user's runtime? |
| :--- | :--- |
| The Aleph host operator | No. Memory is encrypted by SEV-SNP. The root is verified against the measurement, and the data disk is encrypted inside the VM with a key that only arrives after attestation. |
| Aleph's network | No. It only relays signed messages and public images. |
| The Virtroid VPS | It cannot change what a VM runs: the root, kernel and initrd are measured, and phones only accept measurements on a list signed with your offline key. The provisioner does hold each VM's data-disk key, so a compromised VPS that also controlled an Aleph host could read that VM's stored data. |

The data disk uses LUKS with `aes-xts-plain64`, which protects confidentiality
but not integrity: a host can corrupt blocks, but cannot read them or choose
their contents.

## One-time setup

You need the VPS, a funded Aleph wallet, and a disposable x86_64 Linux machine
with Nix for builds (a small Aleph VM works).

1. **Provisioner account.** On the VPS, build the provisioner image and create
   its Aleph account inside the container. The key never leaves the VPS.
   ```bash
   sudo bash /opt/virtroid-source/deploy/vps/build-provisioner-image.sh
   ```
   Put the two printed lines in `deploy/vps/.env`. Create these secret files,
   root-owned, group `PROVISIONER_SECRET_GID`, mode 0440:
   - `/etc/virtroid/secrets/aleph-password`: the keystore password;
   - `/etc/virtroid/secrets/provisioner-key`: a base64 PKCS#8 P-256 key, e.g.
     `openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -outform DER | base64 -w0`;
   - `/etc/virtroid/secrets/luks-passphrase`: any printable value. The root is
     no longer encrypted, but the Aleph CLI's unlock step still expects one.

   Create the account with the same env files and profile as `deploy.sh`:
   ```bash
   docker compose --profile provisioner run --rm --entrypoint aleph provisioner account create virtroid-provisioner --encrypted
   ```
   Its address is measured into every VM, so keep it for good, and back up the
   `virtroid_provisioner-state` volume.

   Pay for its VMs in one of two ways:
   - **Permission.** In the Aleph console's Permissions page, let the
     provisioner's address act for your wallet with INSTANCE and STORE only
     (deleting follows from INSTANCE). Set `ALEPH_OWNER_ADDRESS` to your wallet.
     There is no amount cap, so keep the balance modest; `PROVISIONER_MAX_VMS`
     caps how many VMs exist.
   - **Transfer.** `aleph credit transfer --to <provisioner address> --amount
     <credits> --expiration <date>` from your wallet. Only that amount is
     exposed, and anything unspent returns after the expiry.

2. **Build.** Copy `nix/assets-example` and fill it in:
   - `node.env`: the VPS's `CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64` and runtime
     agent hash;
   - `apks/`: the VPS's `/srv/virtroid/apks` without `cache/`;
   - the Android image: `runtime-image.json` pins a registry image by digest,
     or put our own build in `runtime-image.tar`.

   Then, on the builder:
   ```bash
   deploy/aleph/build-image.sh /path/to/assets out
   ```

   Before publishing, boot it on the builder (it needs `/dev/kvm`):
   ```bash
   deploy/aleph/test/boot-test.sh out
   deploy/aleph/test/initrd-test.sh out
   ```
   `boot-test.sh` runs the root's real boot script with test secrets, then
   starts an Android runtime and its viewer with virtnoded's limits, mounts and
   arguments, and passes once Android boots and the viewer listens.
   `initrd-test.sh` boots the measured initrd itself, with a stand-in for the
   attestation agent, and passes when virtnoded starts and stops for want of
   SEV-SNP. With `BOOT_TEST_DEBUG=1`, a failed `boot-test.sh` VM stays up and
   `test/vmsh 'command'` runs commands in it.

3. **Publish.** Upload the runtime bundle, its manifest and the root image with
   the provisioner's account, following the printed steps. Add
   `--on-behalf-of <your wallet>` when paying by permission. The manifest's
   item hash is `ALEPH_RUNTIME`, and the root image's is `ALEPH_IMAGE`.

4. **Compute nodes.** Pick compute nodes per region from `aleph node list`.
   Each node's `/about/capability` must list `sev_snp`. Set
   `PROVISIONER_REGION_CRNS`, e.g. `snp-eu=<hash>@Genoa,<hash>@Milan`, naming
   each node's EPYC generation.

5. **Control plane.** In `.env`:
   - `PROVISIONER_PUBLIC_KEY`: the public half of the provisioner key, from
     `virtroid-admin public-key`;
   - `PROVISIONED_REGIONS=snp-eu`. New accounts start in the first region
     listed; move older accounts with `virtroid-admin set-account-region`;
   - `PROVISIONER_MAX_VMS`: your spending cap;
   - optionally `PROVISIONER_WARM_SPARES=1`.

   Add `provisioner` to `VIRTROID_PROFILES` and redeploy.

6. **First VM and the approved list.** Start a new account's runtime, or
   move an existing account to the region first
   (`virtroid-admin set-account-region --account <id> --region snp-eu`). Watch `deploy.sh logs provisioner`. Once the VM is up,
   read the launch measurements the CLI recorded in its INSTANCE message
   (`aleph message get <item hash> --json`, under
   `content.environment.trusted_execution.measurements`).

   Phones only accept images on a list signed with your offline release key:
   - Once: `virtroid-measurements keygen --out measurement-signing.key` on a
     machine you keep offline, or a key you store offline. Put the printed
     public key in `android-client/app/src/main/res/raw/measurement_signing_keys`
     and release the app once.
   - For each new image: `virtroid-measurements sign --key
     measurement-signing.key --serial <higher than last> --measurement <hex> ...
     --out snp-measurements.json`. Copy that file to the VPS and set
     `SNP_MEASUREMENTS_FILE` to its path.

   Phones fetch the list when they meet an image they don't know yet, check
   your signature, and never go back to a lower serial. To revoke an image,
   sign a new list without it. Until a list is published, phones refuse these
   VMs, by design.

## Cost

A 2 vCPU, 4 GiB confidential VM is 2 Aleph compute units: about $0.057 an
hour, or $41 a month while it exists, plus storage. VMs are deleted after
`PROVISIONER_RETIRE_AFTER` (default 2 h) once their account has no runtime on
them, and `PROVISIONER_MAX_VMS` caps how many exist at once.

## Known gaps

- A VM keeps its secrets only in memory, so after a restart its initrd waits
  for them again. The provisioner notices because the attestation agent
  presents a new TLS key on every boot: it attests the VM again, injects the
  same secrets, and the node comes back with its data disk intact. A VM that
  restarts more than three times after being unlocked, before its node ever
  comes up, is failing to boot and is retired.
- With `NODE_BLOB_STORE_KIND=local-disk`, runtime snapshots stay on the VM's
  data disk, so a retired VM's data is gone and VMs with a runtime on them are
  never retired automatically. Images built with `sia-indexd` keep snapshots
  on Sia instead ([`deploy/vps/indexd.md`](../vps/indexd.md)): a stopped
  runtime then holds no VM, and its next start restores it on whichever VM
  the account gets.
- The stock ReDroid 14 image turns unprivileged BPF on: its bpfloader sets
  `kernel.unprivileged_bpf_disabled` to 0, and Android reboots if it cannot.
  In a runtime VM that reaches only the one account's VM; on the VPS it is
  host-wide today. The custom AOSP image is to drop this.
- Tetragon (intrusion detection) and the custom AOSP image are next; the first
  build uses the stock ReDroid 14 image the VPS runs today.
