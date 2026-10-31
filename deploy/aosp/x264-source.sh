#!/usr/bin/env bash
# Packs the corresponding source of the image's screen encoder, which is
# GPL-2.0-or-later: x264 at the commit build-x264.sh pins, and the build's
# own inputs (deploy/aosp, with the encoder component in
# patches/frameworks/av 0004 and the scripts that build both, and
# third_party/redroid-patches-lineage), plus the exact revision of every
# LineageOS project the build used. It is published with each VM image that
# ships the encoder.
#
#   deploy/aosp/x264-source.sh OUT
#
# OUT is a build's $LINEAGE_ROOT/out/<build id> directory; build-lineage.sh's
# package stage calls this. Run alone, it first checks that this checkout's
# deploy/aosp and third_party/redroid-patches-lineage are still the files
# that build used (OUT/virtroid-inputs.sha256). Writes
# OUT/x264-source.tar.gz, the same bytes for the same inputs.
set -Eeuo pipefail

out="$(cd "$1" && pwd)"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${here}/../.." && pwd)"
root="${LINEAGE_ROOT:-/srv/lineage}"
id="$(basename "${out}")"

(cd "${repo_root}" && sha256sum -c --quiet "${out}/virtroid-inputs.sha256")
x264_commit="$(sed -n 's/^x264_commit="\([0-9a-f]\{40\}\)".*/\1/p' "${here}/build-x264.sh")"
[ "$(git -C "${root}/x264" rev-parse HEAD)" = "${x264_commit}" ]

stage="$(mktemp -d)"
trap 'rm -rf "${stage}"' EXIT
top="${stage}/virtroid-x264-source-${id}"
mkdir -p "${top}/virtroid" "${top}/lineage"
git -C "${root}/x264" archive --prefix="x264-${x264_commit:0:7}/" "${x264_commit}" | tar -x -C "${top}"
(cd "${repo_root}" && cut -c67- "${out}/virtroid-inputs.sha256" | tar -c --no-recursion -T -) | tar -x -C "${top}/virtroid"
cp "${out}/virtroid-inputs.sha256" "${top}/virtroid/"
cp "${out}/manifest-pinned.xml" "${top}/lineage/"
cp -r "${out}/local_manifests" "${top}/lineage/"
cat >"${top}/SOURCE.md" <<EOF
# Source of the screen encoder in Virtroid's Android build ${id}

The image's c2.virtroid.x264.encoder
(/apex/com.android.media.swcodec/lib64/libcodec2_soft_x264enc.so) contains
x264, which is free software under the GNU General Public License, version 2
or (at your option) any later version: see x264-${x264_commit:0:7}/COPYING.
The encoder component that links it is under the same licence.

- x264-${x264_commit:0:7}/: x264 at commit ${x264_commit}
  (https://code.videolan.org/videolan/x264).
- virtroid/: the files that build used from Virtroid's repository
  (https://github.com/mattyperrott/Virtroid), listed with their SHA-256 in
  virtroid/virtroid-inputs.sha256. The encoder component is
  virtroid/deploy/aosp/patches/frameworks/av/0004-*.patch;
  virtroid/deploy/aosp/build-x264.sh builds x264 (Android NDK r29, nasm),
  virtroid/deploy/aosp/device/virtroid/x264/Android.bp imports it, and
  virtroid/deploy/aosp/build-lineage.sh builds the rest.
- lineage/: every LineageOS 23.2 project the build used, at its exact
  revision (manifest-pinned.xml), and the extra projects (local_manifests/).
EOF
tar -c -C "${stage}" --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
  --mode='u+rwX,go+rX,go-w' "virtroid-x264-source-${id}" | gzip -n -9 >"${out}/x264-source.tar.gz"
sha256sum "${out}/x264-source.tar.gz" >"${out}/x264-source.tar.gz.sha256"
