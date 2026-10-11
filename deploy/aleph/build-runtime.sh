#!/bin/bash
# Builds Virtroid's SEV-SNP instance runtime bundle: Aleph's measured OVMF,
# LUKS initrd and attestation agent, with a guest kernel that adds what ReDroid
# and Docker need (kernel-virtroid.fragment). Needs Nix with flakes on an
# x86_64 Linux builder. Prints the steps to publish the bundle.
#
#   deploy/aleph/build-runtime.sh [work-directory]
set -Eeuo pipefail

# aleph-vm 2.1 line, the runtime the aleph CLI v0.21.0 measures.
ALEPH_VM_COMMIT=7e3c73720e9f203fbf310092238a713d03bdd68e

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mkdir -p "${1:-./aleph-runtime-build}" && cd "${1:-./aleph-runtime-build}" && pwd)"
src="${work}/aleph-vm"

[ "$(uname -s)/$(uname -m)" = Linux/x86_64 ] || {
  echo "build the runtime on an x86_64 Linux machine" >&2
  exit 1
}
command -v nix >/dev/null || {
  echo "Nix with flakes is required (https://nixos.org/download)" >&2
  exit 1
}

if [ ! -d "${src}/.git" ]; then
  git init -q "${src}"
fi
git -C "${src}" fetch -q --depth 1 https://github.com/aleph-im/aleph-vm.git "${ALEPH_VM_COMMIT}"
git -C "${src}" checkout -q --force FETCH_HEAD
[ "$(git -C "${src}" rev-parse HEAD)" = "${ALEPH_VM_COMMIT}" ]

# Replace Aleph's setting of every symbol we set, then append ours, so their
# line-by-line check verifies exactly our values.
fragment="${src}/nix/kernel-config.fragment"
symbols="$(grep -oE '^CONFIG_[A-Z0-9_]+' "${script_dir}/kernel-virtroid.fragment" | sort -u)"
for symbol in ${symbols}; do
  sed -i -E "/^${symbol}=/d; /^# ${symbol} is not set$/d" "${fragment}"
done
cat "${script_dir}/kernel-virtroid.fragment" >> "${fragment}"
# Nix flakes only see tracked content.
git -C "${src}" add nix/kernel-config.fragment

nix --extra-experimental-features 'nix-command flakes' build \
  "git+file://${src}?dir=nix#instanceImage" --out-link "${work}/instance-image"

python3 "${src}/scripts/vprogram_bundle.py" build \
  --out "${work}/bundle" --image-dir "${work}/instance-image" --flavor instance

cat <<EOF

Runtime bundle built in ${work}/bundle.

Publish it with the provisioner's Aleph account (on the VPS, inside the
provisioner container; see deploy/aleph/README.md):
  1. aleph file upload ${work}/bundle/snp-image.tar.gz      -> BUNDLE_HASH
  2. python3 ${src}/scripts/vprogram_bundle.py manifest \\
       --bundle-info ${work}/bundle/bundle-info.json \\
       --bundle-ref BUNDLE_HASH --name virtroid-runtime --runtime-version 1 --flavor instance
  3. aleph file upload ${work}/bundle/manifest.json        -> ALEPH_RUNTIME
EOF
