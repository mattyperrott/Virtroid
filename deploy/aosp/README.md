# Custom Android image: build plan

Status: first build done; boot test and upgrade from today's runtime data
passed (milestones 2 and 3, 2026-10-26). Cuts and built-in apps applied
(milestone 4, [components.md](components.md)); signed with our own release
keys (2026-10-27, [below](#release-keys)). Live since VM image 19 (2026-10-28).
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
   still needs it, and it needs `kernel.unprivileged_bpf_disabled` at 0.
   Ours doesn't: its BPF loader and netd accept the setting locked at 1
   (`patches/packages/modules/Connectivity` 0003), which the VM sets for its
   whole boot (`deploy/aleph/nix/vm-init.sh`). netd checks at start that it
   can create a BPF map, and with the lock on only CAP_BPF allows that. netd
   doesn't get CAP_BPF, since it hosts the DNS resolver, which reads what the
   network sends. Only the two loaders create maps; netd just uses the ones
   they pinned.
4. **Hardening that ports to a container.** GrapheneOS's `hardened_malloc`
   and its other C library hardening: [Memory allocator and C
   library](#memory-allocator-and-c-library). Its frameworks changes
   (exec spawning, the network permission toggle) are still to assess one by
   one.
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
patches of 2026-10-27 were dropped once the owner saw them on a phone.
On 2026-10-30 the owner compared the image with stock ReDroid 16 side by
side and picked the wallpaper, home screen and icon shape below from live
screenshots. Lawnchair, tried on 2026-10-28, was no faster and ran beside
Launcher3, which recents still need.

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
- **Wallpaper and colours.** AOSP's default wallpaper, the phone-size file
  stock ReDroid ships, in every density and screen size LineageOS gives its
  own, so a runtime's wallpaper and the colours Android takes from it
  (which any app can read) are those of every ReDroid phone. LineageOS's
  dark theme stays. The black wallpaper before it left the colours flat
  grey.
- **Home screen.** LineageOS's own launcher on the five-column grid a
  runtime's screen gets: Browser, F-Droid, Gallery, Files and Settings fill
  the dock and the rest shows the wallpaper (the four-column grid keeps a
  four-app dock with Gallery and Calculator above it). Icons are a squircle
  (`config_icon_mask` in the framework overlay, in place of LineageOS's
  circle), across the system. The wallpaper doesn't zoom out behind the
  opening shade (`config_wallpaperMaxScale`): each step of that zoom had
  SurfaceFlinger redraw the viewer's screen on the CPU while SystemUI drew
  the shade.
- **Navigation** stays 3-button: with gesture navigation, the swipe up for
  the runtime's home would trigger the viewing phone's own home, which the
  app can't exclude.
- **Battery.** A container has no battery, so the stock health HAL finds no
  `/sys/class/power_supply` and reports it absent at 0%: the status bar showed
  a "?", battery-aware apps misbehaved (Fauxx stayed paused on "battery below
  threshold" and never ran), and a phone with no battery is an obvious emulator
  tell. A health HAL stand-in (`patches/hardware/interfaces` 0003) reports a
  believable battery instead — it slowly drains, then charges when low, takes
  the claimed phone's pack size from `ro.product.device` and seeds its
  per-start variation (level, wear, cycle count, where in the charge cycle it
  begins) from `ro.serialno`, both set from the persona.

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

## Screen encoder

A runtime has no hardware encoder, so the viewer's stream comes from
Android's software AVC encoder, and while the screen moves it takes more
CPU than anything but SurfaceFlinger. Three patches in
`patches/frameworks/av` cut it by half in a 2-vCPU VM (2026-10-28, builds
26 to 28), with the first frame of each gesture as soon as before:

- 0001 encodes at libavc's fastest preset (no 4x4 intra modes, half-pel
  motion search or deblocking), which suits mostly still screen content.
- 0002 converts the virtual display's RGBA to YUV a row at a time, byte for
  byte as before; Android's per-pixel loop was a third of the encoder's CPU.
- 0003 encodes on one thread. libavc started a second thread for every
  frame, and the two waited on each other by spinning on `sched_yield`.

Since build 30 (2026-10-31) x264 encodes the stream instead, as a Codec2
component of our own, `c2.virtroid.x264.encoder` (0004). It ranks ahead of
`c2.android.avc.encoder`, so whatever asks for `video/avc` gets it, and the
libavc encoder stays as the fallback. On frames recorded from the runtime's
own gestures and encoded on one core, libavc at its fastest preset needs
about 3.1 Mbps for 50 dB PSNR and its worst frames fall to about 30 dB;
x264 at its superfast preset needs about 1.7 Mbps, with worst frames about
45 dB. The component runs superfast tuned for zero latency (no lookahead,
no B-frames), on its own thread raised to `ANDROID_PRIORITY_URGENT_DISPLAY`,
in High profile, with the bitrate held within a second. Its rate control
goes by the configured frame rate, not the frames' timestamps: with
timestamps x264 holds each frame until the next one arrives, to know how
long it lasts, and the screen sends one only when it changes (build 30a
was 100-170 ms slower for it).

Against build 29 in a 2-vCPU VM (one run each), the first change of each
gesture reaches the viewer as soon as before (opening the drawer 186 ms
against 252, back 282 against 290, the shade 409 against 456), each frame
takes about 8 ms longer from capture to output (38 ms against 26 opening
the drawer), and the encoder uses 5.5% of the VM's CPU over five minutes of
gestures against 3.9%. The same gestures take 30-55% less data (6.1 MB
against 8.9 for one script), and the largest frame is 32 KB instead of
185 KB. Settings, still after a scroll, reached the viewer at 52-56 dB PSNR
against a screenshot all six times (worst 32x32 block 43-45 dB); with
libavc it was 45-48 dB four times out of five and 58 once (worst block
36-39, and 49 that once).

x264 is GPL-2.0-or-later. `build-x264.sh` builds it with the NDK during the
patch stage (the build host needs `nasm`) into `device/virtroid/x264`, and
`patches/packages/modules/common` 0001 lets the swcodec APEX depend on it.
`libcodec2_soft_x264enc`, which links it, is under the GPL too, so an image
that ships it goes out with x264's and the component's source (or under
x264 LLC's commercial licence instead). The package stage writes that source
as `x264-source.tar.gz` (`x264-source.sh`: x264 at its pinned commit, the
build's files from this repository, every LineageOS project's revision),
and each VM image is published on Aleph with it, from the same account.
Aleph serves it by its SHA-256 at
`https://api2.aleph.im/api/v0/storage/raw/<sha256>`.

| VM image | Android build | x264 source on Aleph (STORE message) | sha256 |
| :--- | :--- | :--- | :--- |
| 21 | 30 (`20261031T144222Z-user`) | `7aa6529c6a22d3886aa5a97429806463c68b64315f29167fbadcf1aaaf4d1907` | `6b6c152b03f30d70af918e46e6efbf55dc45276e555c070569bf46b55ab29b80` |
| 22 | 32 (`20261101T224242Z-user`) | `ecd6a355c18b2ac6648a8d92301dbd4879c320acdd4e0b42bc92c97e57593726` | `81a1fc65006d1bae43b046d4f6490b2e4f116f96a77bc780572b49de148f0367` |
| 24 | 34 (`20261102T174239Z-user`) | `46b84e32014fe41bdaae9c0a6cc06d006698712e81db4ca8b02d7031ae9f253e` | `f13ab1e3ffacb20dee23bda6df13d9d33fdedac69c5523ca8e7d2a1eafd72262` |
| 26 | 36 (`20261103T140345Z-user`) | `0b638fb2c026c9ddca8b96ee28752e5ca7bfb40dc83a5d9dd2d66c2ee392c762` | `4902576064edb00b87322d208a43c0d22afefe7fd0baef0c7d6668ea8ba7beba` |
| 29 | 39 (`20261105T013215Z-user`) | `055c0e3b9f068dc513794cce048fd3c6538ffb0a574bf208553855f715fffea5` | `cbebf0ca74ffd5dd3f8c939b1ee0f3383f2e6be629934a9d621ef969963888a0` |
| 34 | 46 (`20261111T194116Z-user`) | `3983c675086c6687185266658d2014be1081e2613d64370aa4beb7899e4a142f` | `bb50cbf4f61e86b9d8df1af66e64e8096ff06d0a0e022a923a9e4e733f680933` |

The copyleft apps built in as prebuilts (`device/virtroid/prebuilt-sources.sha256`)
go out the same way: `app-sources.sh` puts each one's source beside the
build, and it's published with the first VM image that ships it. A tarball
that doesn't change between images is the same file, so it isn't published
again. LibreOffice's, at about 1 GB, is pinned by its IPFS CID instead, and
the gateway serves it at `https://ipfs.aleph.cloud/ipfs/<CID>`.

| App source | First VM image | On Aleph (STORE message) | sha256 |
| :--- | :--- | :--- | :--- |
| Currencies 1.23.0 (`de.salomax.currencies_12300_src.tar.gz`) | 22 | `dbec2c84e2331070487230eb51cc9c27878fd9f59ef0557acdc092cef28fba1e` | `7254bfb42c0b8274b2e8e6684341e9cc55d37333e69f325a7a117d6caf7a50b5` |
| Fossify Notes 1.7.0 (`org.fossify.notes_13_src.tar.gz`) | 22 | `04a7f8561e5467d75575138878b98bad42dc2480012f817ef070ba49f687b786` | `c859abd363ebc551c55b165241501c88e484fc09b7892d7c34d2932e60e7f0f3` |
| LibreOffice Viewer 26.2.6.3 (`org.documentfoundation.libreoffice_129_src.tar.gz`, CID `QmUaoCXU7iPMXTr4ZPriQ5hR6GSndKJJv8w74xZxkSZ6UD`) | 22 | `3229a81094d7595c8471ae0d194ce837284fdd036244338aff89f829d9c388fe` | `1d4264ae8b7f68c55124c3f68d38caa3dda31781fcf6f88c7b0a08e091963f1f` |
| Shield 0.1.0 (`shield-source-ef53579.tar.gz`) | 22 | `44079a27b55d5e10dfa3a48222b894689487dac880ade89d61944eed6ae484ef` | `32580f05d232a8692f47d299c095b0941f28318bb8b2cba138ca5ef27d62cf2a` |
| Fauxx 0.9.3-virtroid.1 (`fauxx-source-0.9.3-virtroid.1.tar.gz`) | 24 | `20b0fd3a33158631711ddcb6ffb863592ea2429c121f70dce2a14a547507a7cb` | `a6ef324b7618854412beafad8c587b49b98f5ea149f4d70e38c68f22e5a1dbd8` |
| Shield 0.2.0 (`shield-source-5254d5b.tar.gz`) | 24 | `148d1800c27e1edb9b66abc0a608bc3a3a2d5db14fc7381e423a4f7c627d2747` | `2f4d80b86fef00e1349e97b1002e156a7c0005101391db26ebf79ee58226dab7` |
| Fauxx 0.9.3-virtroid.2 (`fauxx-source-0.9.3-virtroid.2.tar.gz`) | 26 | `d29d7488f7c50865c70e43a06a29565850c4b0900fa459d4ff2fe991d6b915a3` | `c3dd43228f449960c72aff7ce85beb50fb0c4fcf404c70fb146af6ae96211ae8` |
| Shield 0.2.1 (`shield-source-16a7703.tar.gz`) | 26 | `e3a3a5537db7ed7cfa95dfab2df6981f6de18e695d724f425aaaeaa699731c5d` | `00b2ef526a491497f36fdf2f9689597ef8dbfbfb1deecca04fa979c2237ba557` |
| Fauxx 0.9.3-virtroid.3 (`fauxx-source-0.9.3-virtroid.3.tar.gz`) | 29 | `04f2e2583aafb086b30fb246640f8068c33fd061eb47dfd96ad7a0793fc2ea8d` | `71446f30982f385ccc4292eb9b64008026ba5baa32868c777a64e06f619e2af5` |
| Shield 0.3.1 (`shield-source-91b17fa.tar.gz`) | 29 | `fc77ee65579b1c4fddaeda7970132a9eec1451d3044b613ff3c8eb91b59971db` | `21c1114574dd1ec51ae9f4774c282d853d4a9f0bb9d6ac031a50e6d54ae4607b` |

## Container confinement

Our image runs without `--privileged` (2026-10-27). Privileged, a runtime
gets every device of the VM, including its disks and the device-mapper
volumes (the encrypted data volume all its runtimes share among them), a
writable sysfs and `/proc/sys`, and every capability, including setting
the VM's clock: root in Android is root on the VM. The image's label
`io.virtroid.container-profile` (set by `build-lineage.sh`'s package stage)
tells virtnoded how it boots: `unprivileged-1` without that, and
`userns-1` (our image since build 43) the same way inside a user namespace
of its own ([User namespaces](#user-namespaces)). For both the node gives
its container (`backend/cmd/virtnoded/runtime_container.go`):

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
  I/O. Also perf events, a large part of the kernel Android doesn't use:
  apps on phones can't open them either, and init only logs that its probe
  failed.
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

**What it doesn't stop, without a user namespace.** An `unprivileged-1`
container keeps `CAP_SYS_ADMIN` in the VM's own user namespace, and SELinux
is off, so a process that gets Android's
root (an app exploiting a system service, say) can still mount a fresh
sysfs or procfs and change the VM's settings: most sysctls, or unbinding
the VM's disks from their driver. The kernel log, the disks themselves, the
clock and the usermode-helper hooks (`kernel.modprobe`, core dumps) are out
of its reach, and so are module loading (the VM's kernel is built without
it), unprivileged BPF (off until the VM reboots) and the kernel's lockdown
(on from boot), which keeps everyone,
the VM's root included, from reading or writing kernel memory or a
device's PCI configuration (`deploy/aleph/kernel-virtroid.fragment`).
A `userns-1` runtime's root holds its capabilities only in its own user
namespace: [User namespaces](#user-namespaces).

## Memory allocator and C library

Every 64-bit process allocates with GrapheneOS's
[hardened_malloc](https://github.com/GrapheneOS/hardened_malloc) (MIT),
pinned in `local_manifests/hardened_malloc.xml`, instead of Scudo:
allocator metadata kept apart from the heap, guard pages around slabs and
large allocations, random slot choice, canaries, freed memory zeroed and
checked for writes after free, and quarantines that delay reuse. The C
library carries GrapheneOS's own integration and its other hardening,
ported from its Android 17 branch to LineageOS 23.2 (`patches/bionic`;
each patch keeps its author and names the GrapheneOS commit):

- hardened_malloc for 64-bit processes, with a `DISABLE_HARDENED_MALLOC`
  variable that switches one process to Scudo for debugging; GWP-ASan
  sampling never enabled.
- Stacks: guard pages between a thread's stack and its TLS, thread state
  behind a guard page, a random gap above each stack, an 8 MiB default
  stack.
- The stack canary read-only once set, with its first byte zeroed on 64-bit
  so a string overflow can't copy it.
- `pthread_atfork` handlers in their own mapping, read-only between
  registrations; thread-local destructors' pointers mangled.
- Blocking `getrandom` with no `/dev/urandom` fallback; `brk` and `sbrk`
  stubbed out.

Left out: memory tagging (Arm only), GrapheneOS's opt-outs for Pixel
hardware services, and its per-app opt-out, which needs GrapheneOS's
kernel and frameworks. Nothing here adds a symbol or header to the C
library apps see. Our own patch 0019 clears `DISABLE_HARDENED_MALLOC` for
programs that start with privileges, as for `LD_PRELOAD`.

hardened_malloc's guard pages need many more memory mappings than Linux
allows by default, so the VM raises `vm.max_map_count` to 1048576
(`deploy/aleph/nix/vm-init.sh`). `boot-test.sh` checks that system
processes and apps carry its reservations (about 1.5 TiB of address space
each) and that none fell back to Scudo.

### User namespaces

Live since 2026-11-12 in VM image 34. A `userns-1` image's runtime
runs in a user namespace that maps its ids 0 to 999999 to the VM's 1000000
to 1999999 (the VM's `virtroid-runtime` user in `/etc/subuid` and
`/etc/subgid`), so its root's capabilities cover only its own namespaces.
Docker starts it through a runtime of our own, `virtroid-userns`
(`backend/cmd/virtroid-runtime-wrapper`, which then runs runc), which the
node names with annotations for the user namespace, the image's APEX set
and the runtime's BPF filesystem (`backend/cmd/virtnoded/runtime_userns.go`).
Docker itself maps no ids (no `userns-remap`), so other containers and older
images are unchanged. Every runtime on a VM gets the same range: they are
all the one account's.

- **Rootfs.** An overlay mount can't be id-mapped, but its layers can. On
  create the wrapper replaces Docker's overlay with one of the same layers,
  each an id-mapped clone, at the same path, so no two overlays ever share
  the writable layer, and gives runc a bind of it, since the namespace's
  root can't pass through Docker's directories. Files keep their owners on
  disk. If Docker keeps the overlay mounted across a stop, the next start
  binds it again.
- **Data and binds.** `/data` and every read-only bind are id-mapped too, so
  their files read inside with the ids they have on disk, and a runtime's
  data looks on disk as an unprivileged runtime's does: either boots on the
  other's. Docker can't copy into an id-mapped rootfs, so the node writes
  imported files into the runtime's data directory itself
  (`backend/cmd/virtnoded/runtime_data_file.go`).
- **APEXes.** apexd can't mount loop devices in a user namespace. The node
  copies each image's pre-installed APEX files once, through a container
  that never starts, and mounts them read-only
  (`virtroid-runtime-wrapper premount-image`); the wrapper binds that set at
  `/dev/.vapex`, and apexd binds from it (`patches/system/apex` 0003 to
  0005). These runtimes get no loop devices.
- **BPF.** Before each start, a short loading pass of the image, outside any
  user namespace and with no network or data, fills a BPF filesystem for the
  runtime; the wrapper binds it at `/sys/fs/bpf` and attaches netd's cgroup
  programs, and NetBpfLoad and netd accept that
  (`patches/packages/modules/Connectivity` 0004 and 0005).
- **Android's other changes.** init leaves ASLR entropy to the VM, which
  sets it (`patches/system/core` 0003); libcutils keeps a uevent socket whose
  buffer it can't raise (0004); binderfs is mounted without global binder
  stats (0005); the hard memlock limit is kept (0006); apps get stock
  memlock and rtprio limits (`patches/frameworks/base` 0004); AlarmManager
  runs wakeup alarms on plain clocks where the alarm clocks are refused
  (0005); netd ends a rule's UID range where the namespace's map ends
  (`patches/system/netd` 0001). The node gives these runtimes unlimited
  memlock, nice 40 and rtprio 10.
- **Checks.** `boot-test.sh` starts its runtimes the same way and also fails
  if system_server doesn't stay up or if the kernel logs an overlay mounted
  on an upper directory another still uses. Image 34 passed the boot,
  upgrade (on build 39's data) and initrd tests.

Still open: a range of its own for each runtime, should a VM ever hold more
than one account's.

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
   [Container confinement](#container-confinement). hardened_malloc, the C
   library hardening, unprivileged BPF locked off and the VM kernel's
   lockdown: build 41 (2026-11-10: boot, upgrade and initrd tests passed),
   see [Memory allocator and C library](#memory-allocator-and-c-library).
   User namespaces: build 46 (2026-11-12: boot, upgrade and initrd tests
   passed), see [User namespaces](#user-namespaces). Both live since
   2026-11-12 in VM image 34.
7. **Release.** Done: VM image 19 (2026-10-28). Still to write: a monthly
   rebuild runbook.

## Decisions needed

- **Patch licensing.** `redroid-patches` declares no licence, and our ports
  derive from it. Ask its maintainers for one before publishing the ports,
  or write equivalents independently.
- **Build host.** Provider and budget, for the first build and monthly
  rebuilds.
- **Personas.** Rotating (as now) or one uniform persona.
- **GrapheneOS changes.** Which frameworks changes next. Exec spawning
  gives every app its own address-space layout and hardened_malloc secrets
  instead of the zygote's, at a cost in app start time and memory on 2-vCPU,
  4 GiB VMs.

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
  the VM's storage intact. Stock ReDroid 16, which runtimes ran then, never
  reached the ioctl in the same test, so they were not exposed.
- The release keys sit on the build host ([Release keys](#release-keys)).
  Whoever takes them can sign an app that every runtime treats as part of
  the platform, which is the same trust the build host already holds over
  the next image.
- Some apps check for a certified Google device and won't run without one;
  that is true today and stays true.
