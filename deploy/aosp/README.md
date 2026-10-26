# Custom Android image: build plan

Status: first build done; boot test and upgrade from today's runtime data
passed (milestones 2 and 3, 2026-10-26). Not released: test keys, nothing
cut yet (milestone 4).
Written 2026-10-21.

Every runtime VM image up to 15 carries stock ReDroid 14
(`redroid/redroid:14.0.0_64only-latest`, pinned in the deployment's
`runtime-image.json`), whose Android security patch level is 2024-05-05.
Image 16 moves, as a stopgap, to ReDroid's last Android 16 image (patch
2025-06-05) with Google's ARM translation added
([`deploy/aleph/build-runtime-image.sh`](../aleph/build-runtime-image.sh)),
because ReDroid's own image dropped it. Android 16 also needs three things
from the VM kernel that Android 14 did not, now in place
([`deploy/aleph/kernel-virtroid.fragment`](../aleph/kernel-virtroid.fragment)):
loop devices for its APEX packages, writes to mounted block devices (apexd
holds each loop open while mounting it), and ashmem, ported from the Android
Common Kernel ([`deploy/aleph/kernel-ashmem.patch`](../aleph/kernel-ashmem.patch)),
because Android 16 uses memfd only on kernels with Android's ashmem ioctls
on memfd. The image built from this plan relies on the same kernel. This
plan replaces both with an
Android image of our own: current security patches, no Google, the apps
Virtroid needs built in, a location provider fed by the node, and a container
that needs fewer privileges.

## Where it fits

The VM image build already takes our own Android image: put
`runtime-image.tar` (a `docker save` of it) in the assets directory and
`deploy/aleph/build-image.sh` measures it into the root instead of the pinned
ReDroid image (`deploy/aleph/nix/flake.nix`). Nothing else in the release
changes: build, boot test, measure, publish, sign the approved list, switch.
The first VM image to carry it will be the one after image 15.

## Base: LineageOS 23.2

LineageOS `lineage-23.2` is Android 16 QPR2 (`android-16.0.0_r4`) with the
monthly security patches merged, which is the main reason to build at all.
It was preferred over iodéOS, crDroid, CalyxOS, CipherOS and CarbonROM.

ReDroid's container patches for Android 16
([`remote-android/redroid-patches`](https://github.com/remote-android/redroid-patches)
`android-16.0.0_r2`): 17 of 22 apply to `lineage-23.2` and build unchanged.
The other 5 are ported in
[`third_party/redroid-patches-lineage`](../../third_party/redroid-patches-lineage/PROVENANCE.md):
4 that fail on Android 16 QPR2's moved code, and netd's, which applies but
doesn't compile with the tree's clang. Whether they boot has not been tested.

## What the image adds

1. **A user build without Google, cut to what's needed.** No Google apps or
   microG, and none of the phone hardware's apps and services: the full
   keep-and-cut list is in [components.md](components.md). Built in: the
   Virtroid runtime agent as a system app, a launcher (Lawnchair is the
   recommendation; Niagara and Hyperion are proprietary and can't ship),
   F-Droid with its privileged extension, a WebView, a browser and a keyboard.
2. **A location provider.** AOSP's default GNSS HAL, patched to read fixes
   from a socket the node serves for each runtime. The node side is done
   (`backend/cmd/virtnoded/gnss.go`): it serves the place from the runtime's
   sealed region in the HAL's own fixed-location format, and the image's part
   is in [gnss-feed.md](gnss-feed.md). Apps then see an ordinary GPS fix, not
   a mock location (`Location.isMock()` is false), and the region's
   coordinates, set in the app, finally reach apps.
3. **Fewer container privileges.** Containers run with `Privileged: true`
   today. Find what Android needs (binder devices, `netd`'s iptables, BPF,
   some mounts) and grant only that: named capabilities, the device nodes, a
   seccomp profile. Also drop the requirement the stock image's `bpfloader`
   imposes, that `kernel.unprivileged_bpf_disabled` can be set to 0, so the
   VM can lock it at 1 (`deploy/aleph/nix/vm-init.sh`).
4. **Hardening that ports to a container.** GrapheneOS's `hardened_malloc`
   first. Its other changes (network permission toggle, exec spawning) are
   frameworks patches to assess one by one.
5. **Personas.** Build properties already come from the node at start (the
   `0002-allow-override-ro.-prop` patch). Keep rotating them on every start,
   with new app device IDs only on Restart with New Persona. Still open:
   a single uniform persona instead, because the x86_64 CPU contradicts any
   phone model the persona claims.

## Build host

A full Android build needs about 32 GB of RAM (64 GB is comfortable), about
300 GB of disk for source, output and ccache, and as many cores as the budget
allows: 16 cores bring a clean build to a few hours, and later builds with
ccache are much faster. The Aleph builder (4 vCPU, 8 GB) can't do it; it stays
for VM images and boot tests.

The build host is trusted: what it builds is what VMs run and phones approve.
Record each build's inputs (the LineageOS manifest with every project pinned
to a revision, our local manifest and patches) so anyone can rebuild it.
Android builds are not bit-for-bit reproducible by default, so a rebuild may
not match byte for byte.

Monthly security patches mean a rebuild about once a month, each becoming a
new VM image to measure and approve.

## Milestones

1. **Done.** Base chosen; ReDroid patches checked and the 4 failures ported.
2. **Done (2026-10-26).** First build, with
   [`build-lineage.sh`](build-lineage.sh) on the build host (OVH, 6 cores,
   64 GB): `lineage-23.2` with ReDroid's device trees pinned in
   [`local_manifests/redroid.xml`](local_manifests/redroid.xml), as product
   `lineage_virtroid_x86_64` ([`device/virtroid`](device/virtroid):
   ReDroid's `redroid_x86_64_only` with LineageOS's mini phone
   configuration), an x86_64 user build packaged as `runtime-image.tar` and
   `runtime-image.json`. About 3 h 40 min from clean. Beyond ReDroid's
   patches it needed two of our own to ReDroid's device trees
   ([`patches/`](patches): package names Android 16 no longer has, which a
   plain AOSP build skips silently and LineageOS rejects) and a fifth port
   (netd). Against stock ReDroid 16 the image has the same vendor files
   (newer library versions aside), LineageOS's apps in place of AOSP's, and
   375 MiB more in all, much of it in `system_ext` and `product`.
3. **Done (2026-10-26), eighth attempt.** `boot-test.sh` passed with
   `BOOT_TEST_APKS` (SimpleX's ARM build) on a VM image from main c3b04a4
   whose runtime image is our build plus the QPR2 translator: Android boots
   (security patch 2026-10-01), the node's adb key, the GNSS mount,
   timezone, Tor and `.onion`, the viewer and its key check, the secure
   mirror, bitrate adapting to a slow viewer, and SimpleX running through
   translation. With `BOOT_TEST_UPGRADE_FROM` set to today's runtime image
   (stock ReDroid 16 with Google's translator, image 17's), it also passed
   the upgrade: that image booted on fresh data with SimpleX installed, then
   ours booted on the same data, kept SimpleX and ran it (the test's data
   disk grew to 16 GiB to hold both images). The attempts:

   **First boot.** Put `runtime-image.tar` in a copy of the builder's assets
   and run `deploy/aleph/test/boot-test.sh`. It already checks that Android
   boots, resolves names (through Tor) and starts the viewer. Like stock
   ReDroid, the image claims arm64 apps but carries no translator, which the
   boot test rejects, so add Google's `ndk_translation` first, as
   `deploy/aleph/build-runtime-image.sh` does for image 16 (it takes our
   image as `BASE_TAR`).
   - First attempt (2026-10-26): init rebooted at once
     (`bootstrap-apexd-failed`). Android 16 QPR2's cgroup setup stops at the
     first controller it can't set up, and the container can't mount the v1
     blkio controller on the VM's cgroup v2; r2 (stock ReDroid) warned and
     went on. Fixed by restoring r2's loop
     ([`patches/system/core`](patches/system/core)).
   - Second attempt: services started, but apexd activated no APEX and init
     rebooted again. QPR2's apexd opens loop devices only as
     `/dev/block/loopN`, which ueventd creates and a container doesn't have;
     r2's also tried the kernel's `/dev/loopN`. Fixed by restoring that
     fallback ([`patches/system/apex`](patches/system/apex)).
   - Third attempt: all 40 APEXes activated; then NetBpfLoad stopped
     because BPF programs already existed in the kernel (Docker's, in the
     VM), a check new in QPR2 that a container can never pass. Fixed by
     making it a warning
     ([`patches/packages/modules/Connectivity`](patches/packages/modules/Connectivity)).
   - All three are the same pattern: QPR2 dropped a tolerance, or added a
     check, that ReDroid's patches, written for r2, never needed to handle.
   - Fourth attempt: no reboot; init, the BPF loaders, servicemanager and
     the HALs ran, but SurfaceFlinger crash-looped ("output buffer not gpu
     writeable") because no graphics buffer could be allocated: QPR2's
     libcutils takes the ashmem device number from `/dev/ashmem<boot id>`,
     which a container lacks, and our `system/core` port only covered the
     open. Fixed in the port.
   - Fifth attempt: **Android booted** (`sys.boot_completed=1`). With the
     test's adb key authorized, it passed the GNSS mount, ARM translation,
     timezone, Tor networking and `.onion`, the viewer's private sockets
     and key check, streaming and the secure mirror. It failed only the
     slow-viewer bitrate check, because LineageOS's setup wizard held the
     screen (the device was never marked set up), so the screen barely
     changed. Fixed by leaving the wizard out so AOSP's Provision runs, as
     in ReDroid's image ([`patches/vendor/lineage`](patches/vendor/lineage)).
   - Sixth attempt: set up and on the launcher, but adbd was off: a user
     build defaults `persist.sys.usb.config` to `none`. The product turns
     adb on from boot, as a debuggable build does; adbd still accepts only
     the node's key.
   - Seventh attempt: every check passed (boot, the node's adb key, GNSS
     mount, timezone, Tor and `.onion`, the viewer, secure mirror, bitrate
     adapting to a slow viewer) except SimpleX, which crashed in the
     translator's generated code: Google's translator from the Android 16
     emulator image doesn't fit a QPR2 system. `build-runtime-image.sh` now
     takes it from the QPR2 emulator image (`android-36.1`, revision 4) for
     our own builds, with that image's layout (`etc/berberis`) and its
     `ro.berberis.*` settings (`accurate-sigsegv`, for one). Eighth
     attempt: PASS.
   - The user build's adbd accepts only authorized keys (`ro.adb.secure=0`
     works on debuggable builds alone), so the node puts its own public key,
     and only that, in each runtime's `/data/misc/adb/adb_keys` before every
     start (`backend/cmd/virtnoded/adb_key.go`; `boot-test.sh` does the
     same). The key lives in the node's tmpfs home, so each VM boot has its
     own and a restored runtime drops the previous VM's.
4. **Virtroid's apps and debloat.** The built-in apps above; check the F-Droid
   catalogue's common apps and SimpleX still work.
   - **ARM translation without Google.** Try
     [Digitalis](https://github.com/DigitalisX64/digitalis) (Apache-2.0), an
     open ARM64 backend for AOSP's own translator, Berberis, built on Android
     16, in place of Google's closed `libndk_translation`. Upstream Berberis
     translates only RISC-V. Digitalis has no releases or published test
     results yet and says nothing about containers, and building it needs a
     full Android tree, so it is tried on this build host. It replaces
     Google's binary only if the boot test passes with
     `BOOT_TEST_APKS` holding SimpleX's ARM build and the F-Droid apps with
     ARM-only code that runtimes use; otherwise the image keeps Google's.
5. **Location.** The GNSS HAL reading the node's feed, as
   [gnss-feed.md](gnss-feed.md) sets out; check that a map app shows the
   runtime's place and that `isMock()` is false.
6. **Hardening.** Unprivileged container, the BPF setting, `hardened_malloc`.
7. **Release.** A VM image carrying it, through the usual release, then a
   written monthly rebuild runbook.

## Decisions needed

- **Patch licensing.** `redroid-patches` declares no licence, and our ports
  derive from it. Ask its maintainers for one before publishing the ports,
  or write equivalents independently.
- **Build host.** Provider and budget, for the first build and monthly
  rebuilds.
- **Personas.** Rotating (as now) or one uniform persona.
- **GrapheneOS changes.** Which, beyond `hardened_malloc`.

## Risks

- ReDroid's device trees may not fit LineageOS's product makefiles, and
  LineageOS's own services may not expect a container. This is the largest
  unknown, and milestone 2 settles it.
- SELinux stays off inside the container, as with stock ReDroid (the kernel
  config notes why), so Android's app sandbox relies on users, seccomp and
  the container's limits. Item 3 matters more because of that.
- A privileged container can shut down the VM's data filesystem. When
  Android reboots it can't unmount `/data`, so init calls the filesystem
  shutdown ioctl on it, and `/data` is a bind mount of the VM's encrypted
  data volume, which Docker's storage and every other runtime share. The
  first boot test hit it (milestone 3); without `CAP_SYS_ADMIN` (item 3) the
  ioctl fails.
- The image is signed with AOSP's public test keys, as stock ReDroid is, so
  anyone can sign an app that shares the platform's signature and claims
  its privileges. A release image needs our own release keys (LineageOS's
  "signing builds" procedure), kept offline like the measurement key.
- Some apps check for a certified Google device and won't run without one;
  that is true today and stays true.
