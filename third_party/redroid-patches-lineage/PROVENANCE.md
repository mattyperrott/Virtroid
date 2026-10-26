# ReDroid patches ported to LineageOS 23.2

ReDroid runs Android in a container by patching a few AOSP projects so Android
boots without the hardware and kernel features a phone has. Its patches for
Android 16 live in
[`remote-android/redroid-patches`](https://github.com/remote-android/redroid-patches),
directory `android-16.0.0_r2/`, at commit
`a5c70c0dd89c12b5027c902ab4182f31d38f3bb7` (2025-09-07). The custom Android
image (see [`deploy/aosp/README.md`](../../deploy/aosp/README.md)) applies them
to LineageOS `lineage-23.2`, which is based on `android-16.0.0_r4`.

Of the 22 upstream patches, 17 apply to LineageOS and build unchanged; take
those from upstream at the commit above. Four fail to apply because of code
moved in Android 16 QPR2 (they fail the same way on plain AOSP
`android-16.0.0_r4`). One, netd's, applies but doesn't compile: netd builds
with `-Werror`, and the tree's clang (r563880c) rejects the variable the patch
leaves unused. This directory holds those 5, ported by Virtroid. The changes
are mechanical, and each keeps the upstream patch's purpose:

| Port | What changed from upstream |
| :--- | :--- |
| `patches/system/core/0001-fix-booting.patch` | Same hunks, re-based onto the moved code; one stray trailer line dropped. QPR2 also takes the ashmem device number from a new `__init_ashmem_rdev()`, which stats `/dev/ashmem<boot id>`; the port points that at `/dev/ashmem` too, as upstream's open already is, since a container has no boot-ID node (found in the fourth boot attempt: every graphics buffer failed to allocate). |
| `patches/system/bpf/0001-fix-booting.patch` | The skipped block in `loader/Loader.cpp` moved; the `#if 0` guard follows it. |
| `patches/packages/modules/Connectivity/0001-fix-booting.patch` | `NetBpfLoad.cpp` now uses `writeFile` with distinct return codes; the BPF JIT writes stay best-effort. |
| `patches/packages/modules/Connectivity/0002-ignore-nativeSynchronizeKernelRCU-error.patch` | The function moved to `com_android_net_module_util_Bpf.cpp`; a missing `PF_KEY` socket (none in the container) returns success. |
| `patches/system/netd/0001-fix-booting.patch` | Same hunks, except that `TetherController::getTetherStats` ignores the tether stats parser's result outright instead of keeping its unused return code under an `#if 0`. |

None of this has booted yet. The four ports for moved code apply cleanly, and
the netd port was made when the first build (deploy/aosp/build-lineage.sh)
reached it.

`check-patches.sh WORKDIR LABEL` re-runs the check that the patches apply; it
compiles nothing, so only a build catches a patch like netd's. `WORKDIR` holds a
`redroid-patches` checkout at the commit above and `projects-LABEL.txt`
(`projects-lineage.txt` or `projects-aosp.txt`, copied from here), listing each
project's path, repository and branch. The script makes sparse checkouts of
the files the patches touch and reports each patch as `OK`, `FUZZ` or `FAIL`.

## Licensing

`remote-android/redroid-patches` declares no licence (no licence file, and
GitHub reports none as of 2026-10-21), so its patches are not clearly
redistributable, and these ports are derived from them. The upstream patches
are not copied here, and before this directory is published the licensing
should be settled: ask the ReDroid maintainers for a licence, or replace the
ports with independently written equivalents. The AOSP and LineageOS code the
patches modify is Apache License 2.0.
