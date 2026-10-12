#!/bin/bash
# Builds everything a Virtroid runtime VM boots, on an x86_64 Linux machine
# with Nix (flakes enabled):
#   runtime/   OVMF, the kernel and the measured initrd (the root hash is in
#              the initrd), packaged as an Aleph instance-runtime bundle
#   root/      the read-only root disk with its dm-verity hash tree
#
#   deploy/aleph/build-image.sh ASSETS_DIR [OUT_DIR]
#
# ASSETS_DIR holds node.env, runtime-image.json or runtime-image.tar, and apks/
# (see nix/assets-example). Everything in it is measured into the root.
set -Eeuo pipefail

assets="$(cd "${1:?usage: build-image.sh ASSETS_DIR [OUT_DIR]}" && pwd)"
out="$(mkdir -p "${2:-./virtroid-image}" && cd "${2:-./virtroid-image}" && pwd)"
repo="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
flake="git+file://${repo}?dir=deploy/aleph/nix"
nix=(nix --extra-experimental-features 'nix-command flakes')
override=(--override-input assets "path:${assets}" --no-write-lock-file)

"${nix[@]}" build "${flake}#runtimeImage" "${override[@]}" -o "${out}/runtime"
"${nix[@]}" build "${flake}#rootImage" "${override[@]}" -o "${out}/root"

# Package the runtime with Aleph's own bundle script, from a git checkout of
# the pinned commit (the script records the checkout's revision).
aleph_rev="$("${nix[@]}" eval --raw --impure --expr "(builtins.getFlake \"${flake}\").inputs.aleph-vm-src.rev")"
aleph_src="${out}/aleph-vm"
[ -d "${aleph_src}/.git" ] || git init -q "${aleph_src}"
git -C "${aleph_src}" fetch -q --depth 1 https://github.com/aleph-im/aleph-vm.git "${aleph_rev}"
git -C "${aleph_src}" checkout -q --force FETCH_HEAD
python="$("${nix[@]}" build --no-link --print-out-paths --impure --expr \
  "(builtins.getFlake \"${flake}\").inputs.nixpkgs.legacyPackages.x86_64-linux.python3.withPackages (p: [ p.pydantic ])")"
rm -rf "${out}/bundle"
"${python}/bin/python3" "${aleph_src}/scripts/vprogram_bundle.py" build \
  --out "${out}/bundle" --image-dir "${out}/runtime" --flavor instance

(cd "${out}" && sha256sum bundle/snp-image.tar.gz root/root.img > SHA256SUMS)
cat <<NEXT

Built ${out}:
  root hash $(cat "${out}/root/roothash")
$(sed 's/^/  /' "${out}/SHA256SUMS")

Publish with the provisioner's account on the VPS (deploy/aleph/README.md):
  1. aleph file upload bundle/snp-image.tar.gz            -> BUNDLE_HASH
  2. ${aleph_src}/scripts/vprogram_bundle.py manifest \\
       --bundle-info bundle/bundle-info.json --bundle-ref BUNDLE_HASH \\
       --name virtroid-runtime --runtime-version 1 --flavor instance
  3. aleph file upload bundle/manifest.json                -> ALEPH_RUNTIME
  4. aleph file upload root/root.img                       -> ALEPH_IMAGE
NEXT
