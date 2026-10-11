# Confidential runtime VMs on Aleph Cloud

Each account in a provisioned region gets its own AMD SEV-SNP virtual machine
on Aleph Cloud. Its memory and disks are encrypted by the CPU, and the phone
checks the VM's attestation before it streams. This directory builds what the
VMs boot. The provisioner (`backend/cmd/virtroid-provisioner`) creates and
deletes them.

> [!WARNING]
> This path is built and unit-tested, but has not yet run end to end against
> real Aleph SNP hosts. The kernel options, the image build and the first
> VM all need a supervised first run (step 6).

## How a VM comes up

1. A user in a provisioned region starts a runtime. `virtroidd` finds no node
   for them, so it claims a warm spare or records a VM request. The app is told
   the server is being prepared and retries.
2. The provisioner sees the request and runs `aleph instance create` with our
   runtime, the encrypted root image, a per-VM data disk, and a compute node
   from the region's list.
3. Aleph boots the VM from our measured kernel and Aleph's initrd. The initrd
   starts Aleph's attestation agent and waits for the disk passphrase.
4. The provisioner runs `aleph instance unlock`. The CLI verifies the VM's SEV-SNP
   report (AMD signature chain, launch measurement, policy) over attested TLS,
   and only then sends:
   - the root disk passphrase;
   - the VM's node id and node key;
   - its region and control plane URL;
   - its own data-disk key.
   The node key is approved in the control plane at the same moment.
5. `/sbin/init` (`rootfs/virtroid-vm-init`) does the rest:
   - opens the per-VM data disk;
   - allows only ports 8443 (attestation agent) and 9443 (node) inbound;
   - starts Docker and `virtnoded` with `NODE_SNP_ATTESTATION=required`.
6. `virtnoded` gets an SNP report that binds its TLS key, adds AMD's
   certificates, and heartbeats. The VM is ready.
7. On each session the phone verifies that bundle itself: AMD root built into
   the app, approved measurement, no debug policy, the TLS key it pins, and the
   viewer key signed by that TLS key. It refuses to connect otherwise.

## What is trusted

| Party | Can it read a user's runtime? |
| :--- | :--- |
| The Aleph host operator | No. Memory is encrypted by SEV-SNP. Both disks are LUKS-encrypted inside the VM. The passphrase and keys only enter after attestation. |
| Aleph's network | No. It only relays signed messages and encrypted images. |
| The Virtroid VPS (provisioner) | Yes, as long as the root disk is not measured. The provisioner holds the disk passphrase, so a compromised VPS could hand a VM a modified root disk. The phone checks the boot chain and the provisioner's address, which is measured into the kernel command line, but not the disk contents. Measuring the root disk (dm-verity, as in Aleph's V-PROGRAMs) is the next hardening step. |

The root image is shared by every VM and holds no secrets. User data and the
node's TLS key live on each VM's own data disk, encrypted with a key only that
VM and the provisioner hold. LUKS with `aes-xts-plain64` protects
confidentiality but not integrity: a host can corrupt blocks, but cannot read
or choose their contents.

## One-time setup

You need the following:

- an x86_64 Linux builder with Nix, Go, debootstrap and cryptsetup (any
  disposable Debian VM);
- the VPS;
- a new Aleph account with credits, used only by the provisioner.

1. **Provisioner account.** On the VPS, build the provisioner image and create
   its Aleph account inside the container. The key never leaves the VPS.
   ```bash
   sudo bash /opt/virtroid-source/deploy/vps/build-provisioner-image.sh
   ```
   Put the two printed lines in `deploy/vps/.env`. Create the secret files,
   root-owned, group `PROVISIONER_SECRET_GID`, mode 0440:
   - `/etc/virtroid/secrets/aleph-password`: the keystore password;
   - `/etc/virtroid/secrets/luks-passphrase`: printable characters, no newline;
   - `/etc/virtroid/secrets/provisioner-key`: a base64 PKCS#8 P-256 key, e.g.
     `openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -outform DER | base64 -w0`.

   Then create the account inside the provisioner container. Use the same
   env files and profile as `deploy.sh`:
   ```bash
   docker compose --profile provisioner run --rm --entrypoint aleph provisioner account create virtroid-provisioner
   ```
   Fund its address with Aleph credits. Its address is measured into every VM,
   so keep it for good.

2. **Runtime.** On the builder:
   ```bash
   deploy/aleph/build-runtime.sh
   ```
   Then publish the bundle and manifest with the provisioner account, following
   the printed steps. The manifest's item hash is `ALEPH_RUNTIME`.

3. **Root image.** Copy the VPS's `/srv/virtroid/apks` to the builder, fill in
   `rootfs/node.env.example` as `node.env`, then:
   ```bash
   sudo deploy/aleph/build-rootfs.sh --node-env node.env --apks apks --passphrase-file luks-passphrase --out out
   ```
   Upload `out/virtroid-rootfs.luks.img`; its item hash is `ALEPH_IMAGE`.

4. **Compute nodes.** Pick compute nodes per region from `aleph node list`.
   Each node's `/about/capability` must list `sev_snp`. Set
   `PROVISIONER_REGION_CRNS`, e.g. `snp-eu=<hash>@Genoa,<hash>@Milan`, naming
   each node's EPYC generation.

5. **Control plane.** In `.env`:
   - `PROVISIONER_PUBLIC_KEY`: the public half of the provisioner key, from
     `virtroid-admin public-key`;
   - `PROVISIONED_REGIONS=snp-eu`;
   - `PROVISIONER_MAX_VMS`: your spending cap;
   - optionally `PROVISIONER_WARM_SPARES=1`.

   Add `provisioner` to `VIRTROID_PROFILES` and redeploy.

6. **First VM and the app.** Assign a test account to the region
   (`virtroid-admin set-account-region --account <id> --region snp-eu`) and start
   its runtime. Watch `deploy.sh logs provisioner`. Once the VM is up, read the
   launch measurements the CLI recorded in its INSTANCE message
   (`aleph message get <item hash> --json`, under
   `content.environment.trusted_execution.measurements`). Add each to
   `android-client/app/src/main/res/raw/snp_launch_measurements` and release the
   app. Until then the app refuses these VMs, by design.

## Cost

A 2 vCPU, 4 GiB VM is 2 Aleph compute units: about $0.057 an hour, or $41 a
month while it exists, plus storage. VMs are deleted after
`PROVISIONER_RETIRE_AFTER` (default 2 h) once their account has no runtime on
them, and `PROVISIONER_MAX_VMS` caps how many exist at once.

## Known gaps

- If an Aleph host reboots a VM, its initrd waits for the passphrase again,
  but the provisioner does not yet notice. `virtroid-admin retire-vm` replaces
  the VM, which loses that VM's local runtime data.
- Runtime snapshots stay on the VM's data disk until Sia storage lands, so a
  retired VM's data is gone. VMs with a runtime on them are therefore never
  retired automatically.
- The root disk is not measured (see What is trusted).
