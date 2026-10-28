# Custom Android image: build plan

Status: first build done; boot test and upgrade from today's runtime data
passed (milestones 2 and 3, 2026-10-26). Cuts and built-in apps applied
(milestone 4, [components.md](components.md)); signed with our own release
keys (2026-10-27, [below](#release-keys)). Not released.
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
   coordinates, set in the app, finally reach apps. With each fix comes the
   sky a phone there would see (the real satellites' orbits, built into the
   node), its NMEA, and the GNSS chip of the phone the persona claims.
3. **Fewer container privileges.** Our image runs without `Privileged:
   true`: [Container confinement](#container-confinement). Stock ReDroid
   still needs it. Still open: whether our image needs
   `kernel.unprivileged_bpf_disabled` settable to 0, which stock ReDroid's
   `bpfloader` does, so the VM can lock it at 1
   (`deploy/aleph/nix/vm-init.sh`); unprivileged, the container can't set
   it through Docker's read-only `/proc/sys`.
4. **Hardening that ports to a container.** GrapheneOS's `hardened_malloc`
   first. Its other changes (network permission toggle, exec spawning) are
   frameworks patches to assess one by one.
5. **Personas.** Build properties already come from the node at start (the
   `0002-allow-override-ro.-prop` patch). Keep rotating them on every start,
   with new app device IDs only on Restart with New Persona. Still open:
   a single uniform persona instead, because the x86_64 CPU contradicts any
   phone model the persona claims.

   What apps read without a permission must not name Virtroid. The
   image's own build properties name ReDroid's device
   (`lineage_redroid_x86_64`) with a neutral build user, host and number,
   where they named our product and build host (`Build.HOST` was
   `virtroid-lineage`). The partition fingerprints, build date, host and
   user stay the image's own, as in live runtimes, and so does the security
   patch level. Still naming
   Virtroid, in live runtimes as well, for the next milestone: the
   viewer's mounts, files and properties (`/opt/virtroid`,
   `virtroid.viewer.*`, the `virtroid_viewer` service), the GNSS socket's
   path, and the runtime agent's package (`io.virtroid.runtimeagent`).
   ReDroid itself still shows (`ro.hardware=redroid`, the raw
   `build.prop` files), as it does today.

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

## Release keys

The image is signed with our own keys, not AOSP's public test keys
(2026-10-27). With test keys anyone can sign an app with the platform's
certificate and claim its privileges, or `android.uid.system` itself.

- **The keys.** `make-release-keys.sh` made 53 certificates (the default,
  releasekey; the named ones such as platform, shared, media and
  networkstack; one per certificate a module kept in its own directory) and
  35 APEX payload keys, all 4096-bit RSA. The subject is a plain
  `CN=Android`: apps can read the system's certificates, so naming Virtroid
  would label every runtime.
- **Where they live.** The private halves only in the build host's
  `$LINEAGE_ROOT/keys` (mode 700), plus an encrypted backup the owner keeps
  with a passphrase only they know. They are not kept offline: whoever
  controls the build host can already put anything into the next image, so
  keys held elsewhere would add little. The public halves are in
  [keys/](keys/), in git.
- **How the build uses them.** LineageOS signs in the build from
  `vendor/lineage-priv/keys`, where `build-lineage.sh`'s patch stage puts
  both halves after checking each pair matches. `keys.mk` makes releasekey
  the default certificate (so the build is tagged `release-keys`), named
  certificates and APEX payload keys come from the same directory, and
  `PRODUCT_CERTIFICATE_OVERRIDES` swaps the certificates modules keep in
  their own directories. Recovery's OTA keys are cleared: a runtime never
  installs an update package.
- **The check.** A key the build can't find in our directory falls back to
  the module's own test key without a word, so the package stage runs
  `check-signatures.py`: every APK and APEX in the image, and every APK inside
  each APEX, must carry one of our certificates and payload keys, or be a
  pinned prebuilt that keeps its publisher's signature (F-Droid, the runtime
  agent). The first signed build passed: 146 checked, none other.
- **Runtimes keep their data across the change.** Android accepts a new
  signature on a system package, and on a shared user ID such as
  `android.uid.system`, when it comes from the system image, and keeps the
  data. `boot-test.sh` checked it (`BOOT_TEST_RUNTIME_IMAGE` with
  `BOOT_TEST_UPGRADE_FROM`): from our test-key build's data with SimpleX
  installed, the signed build booted, logged "signature changed; retaining
  data" for 108 system packages, recorded our platform certificate for
  `android`, SystemUI and Settings, kept SimpleX and its data, and ran it.
  Nothing crashed, and F-Droid's extension kept its install permission.
  From stock ReDroid 16's data, as today's runtimes have it, the same: 90
  system packages changed signature with their data kept, and the apps the
  image cuts were dropped with theirs.
- **When Android adds a module.** A new APEX or module certificate makes the
  check fail and name it; add it to `make-release-keys.sh` and run it again,
  which only adds keys. Never replace a key: each change is another
  migration of every runtime's system signatures.
- **Left out.** The CTS shims (an APEX and two apps that only Android's
  compatibility tests use) come prebuilt with Google's test keys, which a
  prebuilt can't be re-signed away from, so the image drops them.

## Look

Android's own look (2026-10-28). The colour theme, tile shape and icon
patches of 2026-10-27 were dropped once the owner saw them on a phone;
Lawnchair, with the owner's settings built in, is to carry the look.

- **Notification shade.** As Android 16 first shipped it (bp2a). QPR1 and
  QPR2 turned on a blurred shade, quick settings in Compose and a shader that
  pushes the app back behind the shade (SystemUI's notification_shade_blur,
  qs_ui_refactor_compose_fragment, qs_compose_fragment_early_expansion,
  spatial_model_app_pushback and spatial_model_pushback_in_shader). A runtime
  has no GPU, so the CPU draws every frame of them: in the same VM as stock
  ReDroid 16, the shade opened about 230 ms later, and SystemUI's slowest
  frames took over two seconds against under one. These flags are fixed
  when the image is built, so `device/virtroid/release` turns them off for
  the bp4a release, along with the launcher's blur behind the app drawer
  and overview (all_apps_blur, enable_overview_background_wallpaper_blur),
  which doubled the time its frames took to draw. With both off, the
  launcher misread its blur radius and crashed each time it came back home
  from an app; `patches/packages/apps/Launcher3` fixes that.
- **Home screen.** LineageOS's own launcher: Browser, F-Droid, Files and
  Settings in the dock, Gallery and Calculator above it, on a black
  wallpaper. The wallpaper doesn't zoom out behind the opening shade
  (`config_wallpaperMaxScale` in the framework overlay): each step of that
  zoom had SurfaceFlinger redraw the viewer's screen on the CPU while
  SystemUI drew the shade, and on black it can't be seen.
- **Navigation** stays 3-button: with gesture navigation, the swipe up for
  the runtime's home would trigger the viewing phone's own home, which the
  app can't exclude.
- **Battery.** The status bar shows a battery with a "?", as a container has
  none, and Android 16's battery icon can't be hidden by a setting.

## Compiled code

Android 16's garbage collector needs the kernel's userfaultfd, which the VM
kernel leaves out, and code compiled for it can't run under the concurrent
copying collector ART falls back to. A ReDroid build has no kernel to go
by, so it compiled for the newer one: at boot ART threw away every odex in
the image and the boot image itself, and the framework, SystemUI and the
launcher ran interpreted (stock ReDroid 16 does the same in our VM). The
image is built for the concurrent copying collector
(`PRODUCT_ENABLE_UFFD_GC := false`), so what the build compiles is what
runs: the boot image, and SystemUI and the launcher compiled with `speed`,
as LineageOS lists them.

## Container confinement

Our image runs without `--privileged` (2026-10-27). Privileged, a runtime
gets every device of the VM, including its disks and the device-mapper
volumes (the encrypted data volume all its runtimes share among them), a
writable sysfs and `/proc/sys`, and every capability, including setting
the VM's clock: root in Android is root on the VM. The image's label
`io.virtroid.container-profile=unprivileged-1` (set by `build-lineage.sh`'s
package stage) tells virtnoded it boots without that, and the node then
gives its container (`backend/cmd/virtnoded/runtime_container.go`):

- **Capabilities:** what the image's services declare (init gives each its
  own) and init's own for mounts, devices and cgroups. Not `SYS_MODULE`,
  `SYS_RAWIO`, `SYS_TIME`, `MAC_ADMIN` or `MAC_OVERRIDE`, nor `SYSLOG`
  (the VM's kernel log, which holds every runtime's) or `AUDIT_CONTROL`.
- **Devices:** the ones a privileged runtime was seen to open: ashmem, the
  kernel log for writing only, the system DMA heap, FUSE, tun, loop-control
  and the loop devices, and a cgroup rule for the binder major (binderfs is
  mounted by Android itself). None of the VM's disks, device-mapper volumes
  or serial lines. A runtime can see the loop devices other runtimes'
  APEXes sit on, which hold only the image's own files.
- **Cgroups:** its own subtree, writable (`writable-cgroups`), which init
  splits into its services' and apps' groups.
- **Seccomp:** everything the capabilities allow, except calls that reach
  past the container: shutting down a filesystem (`/data` is the VM's data
  volume), opening files by handle (anywhere on that volume), loading
  kernels or modules, setting the clock, swap, process accounting and raw
  I/O.
- **sysfs and `/proc`:** Docker's, read-only and masked.

The image needed four changes for it: apexd no longer fails when it can't
set a loop device's read-ahead (`patches/system/apex` 0002); logd asks for
neither `SYSLOG` nor `AUDIT_CONTROL` (`patches/system/logging`, with
`ro.logd.auditd=false`), and NetBpfLoad not for `SYSLOG`
(`patches/packages/modules/Connectivity` 0002), since init can't start a
service whose capabilities the container doesn't have and bpfloader's
failure reboots Android; and init no longer shuts down
`/data`'s filesystem at a reboot (`patches/system/core` 0002, see
[Risks](#risks)). The VM's kernel runs no usermode helpers
(`CONFIG_STATIC_USERMODEHELPER`) and makes 128 loop devices, 40 per runtime
(`deploy/aleph/kernel-virtroid.fragment`). `boot-test.sh` starts its own
runtimes the same way (a node test keeps the two in step) and checks that
the runtime can't open the VM's disks or volumes, read its kernel log, set
its clock, or write sysfs.

**What it doesn't stop.** The container keeps `CAP_SYS_ADMIN` in the VM's
own user namespace, and SELinux is off, so a process that gets Android's
root (an app exploiting a system service, say) can still mount a fresh
sysfs or procfs and change the VM's settings: sysctls, or unbinding the
VM's disks from their driver. The kernel log, the disks themselves, the
clock and the usermode-helper hooks (`kernel.modprobe`, core dumps) are out
of its reach. Closing the rest means a user namespace for each runtime:
[User namespaces](#user-namespaces).

### User namespaces

The next milestone: each runtime's root maps to an unprivileged range of
the VM's users, so its capabilities cover only its own namespaces. What it
takes:

1. **APEXes.** apexd mounts each APEX's ext4 image from a loop device,
   which a user namespace can't. Have the node mount the image's APEXes
   once per VM, read-only, and patch apexd to bind those mounts instead
   (which also frees the 40 loop devices each runtime holds).
2. **netd's BPF programs.** Loading them needs `CAP_BPF` and `NET_ADMIN`
   outside the namespace. Either the node loads them for the runtime, or
   BPF tokens (Linux 6.9 and later) delegate a bpffs; to be tried.
3. **One range per runtime.** Docker maps every container through one
   daemon-wide range (`userns-remap`), so all runtimes' roots would be the
   same VM user and could reach each other's files. Distinct ranges mean
   starting runtimes through containerd or runc directly, or idmapped
   mounts of each runtime's data; to be chosen.
4. **Data ownership.** Existing runtimes' files are owned by the VM's real
   ids: map them at the first start (idmapped mounts) or chown them once.
5. **Device nodes and mounts** Android makes itself (`mknod`, configfs):
   check each fails harmlessly, as most already do in the container.
6. **The check:** `boot-test.sh` with the runtime unable to mount sysfs or
   procfs, write any sysctl, or see another runtime's files.

## Milestones

1. **Done.** Base chosen; ReDroid patches checked and the 4 failures ported.
2. **Done (2026-10-26).** First build, with
   [`build-lineage.sh`](build-lineage.sh) on the build host (OVH, 6 cores,
   64 GB): `lineage-23.2` with ReDroid's device trees pinned in
   [`local_manifests/redroid.xml`](local_manifests/redroid.xml), as product
   `lineage_virtroid_x86_64`, since renamed `lineage_redroid_x86_64` so
   the image's properties don't name Virtroid ([`device/virtroid`](device/virtroid):
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
5. **Location.** Done 2026-10-27 (build 16): the GNSS HAL reads the node's
   feed, as [gnss-feed.md](gnss-feed.md) sets out, and the boot test's
   location check passed end to end, from a sealed region to an app's GPS
   fix with its satellites, not mock. Still to check: a map app showing the
   runtime's place.
6. **Hardening.** Unprivileged container: built (2026-10-27), see
   [Container confinement](#container-confinement); user namespaces next.
   Still open: the BPF setting and `hardened_malloc`.
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
  the container's limits, and a root exploit in Android reaches the
  container's capabilities: [what confinement doesn't stop](#container-confinement).
- A privileged container can shut down the VM's data filesystem. When
  Android reboots it can't unmount `/data`, so init calls the filesystem
  shutdown ioctl on it, and `/data` is a bind mount of the VM's encrypted
  data volume, which Docker's storage and every other runtime share. The
  first boot test hit it (milestone 3); without `CAP_SYS_ADMIN` (item 3) the
  ioctl fails. Hit again 2026-10-27, when a test container failed early and
  rebooted: `EXT4-fs (dm-1): shut down requested`, and every file under
  `/srv/virtroid` gave I/O errors until the VM restarted. Our image no longer
  does it (`patches/system/core` 0002: init leaves `/data`'s filesystem to
  the host): with build 17, Android rebooting in a privileged runtime left
  the VM's storage intact. Stock ReDroid 16, which runtimes run today, never
  reached the ioctl in the same test, so they were not exposed.
- The release keys sit on the build host ([Release keys](#release-keys)).
  Whoever takes them can sign an app that every runtime treats as part of
  the platform, which is the same trust the build host already holds over
  the next image.
- Some apps check for a certified Google device and won't run without one;
  that is true today and stays true.
