# Custom Android image: build plan

Status: planned. Nothing here has been built yet. Written 2026-10-21.

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
`android-16.0.0_r2`): 18 of 22 apply to `lineage-23.2` unchanged, and the
other 4, which fail on Android 16 QPR2's moved code, are ported in
[`third_party/redroid-patches-lineage`](../../third_party/redroid-patches-lineage/PROVENANCE.md).
They apply cleanly. Whether they boot has not been tested.

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
2. **First build.** Rent the build host. Sync `lineage-23.2`, add ReDroid's
   device trees and our patches through a local manifest, build an x86_64
   user image, package it as a container image.
3. **First boot.** Put `runtime-image.tar` in a copy of the builder's assets
   and run `deploy/aleph/test/boot-test.sh`. It already checks that Android
   boots, resolves names (through Tor) and starts the viewer.
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
- Some apps check for a certified Google device and won't run without one;
  that is true today and stays true.
