#!/bin/bash
# Builds the encrypted root disk of a Virtroid runtime VM: a minimal Debian
# with Docker, adb, cryptsetup, virtnoded and the VM init, formatted as Aleph's
# confidential-instance boot expects (whole-disk LUKS2, aes-xts-plain64).
# Run as root on an x86_64 Debian or Ubuntu builder with debootstrap,
# cryptsetup, e2fsprogs and Go. Every VM boots from this one image; it holds
# no secrets, and each VM keeps its data on its own encrypted disk.
#
#   sudo deploy/aleph/build-rootfs.sh --node-env FILE --apks DIR \
#     --passphrase-file FILE --out DIR [--size-mib 3072]
set -Eeuo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(git -C "${script_dir}" rev-parse --show-toplevel)"
suite=trixie
mirror=http://deb.debian.org/debian
size_mib=3072
node_env="" apks="" passphrase_file="" out=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --node-env) node_env="$2"; shift 2 ;;
    --apks) apks="$2"; shift 2 ;;
    --passphrase-file) passphrase_file="$2"; shift 2 ;;
    --out) out="$2"; shift 2 ;;
    --size-mib) size_mib="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 64 ;;
  esac
done
[ -n "${node_env}" ] && [ -n "${apks}" ] && [ -n "${passphrase_file}" ] && [ -n "${out}" ] || {
  sed -n '9,10p' "$0" >&2
  exit 64
}
[ "$(id -u)" -eq 0 ] && [ "$(uname -m)" = x86_64 ] || { echo "run as root on x86_64" >&2; exit 1; }
for tool in debootstrap cryptsetup losetup mkfs.ext4 e2fsck resize2fs go; do
  command -v "${tool}" >/dev/null || { echo "missing ${tool}" >&2; exit 1; }
done
[ -f "${apks}/manifest.json" ] && [ -f "${apks}/virtroid-runtime-agent.apk" ] || {
  echo "${apks} must hold manifest.json and virtroid-runtime-agent.apk" >&2
  exit 1
}
for key in CONTROL_PLANE_CALLBACK_PUBLIC_KEY_B64 NODE_RUNTIME_IMAGE NODE_NOTIFICATION_AGENT_APK_SHA256; do
  grep -Eq "^${key}=.+" "${node_env}" || { echo "${node_env}: ${key} is not set" >&2; exit 1; }
done
grep -Eq '^NODE_RUNTIME_IMAGE=[^@]+@sha256:[0-9a-f]{64}$' "${node_env}" || {
  echo "NODE_RUNTIME_IMAGE must be pinned by digest" >&2
  exit 1
}
# Aleph's guest reads the injected passphrase up to the first newline.
passphrase="$(cat "${passphrase_file}")"
[ -n "${passphrase}" ] && ! printf '%s' "${passphrase}" | LC_ALL=C grep -q '[[:cntrl:]]' || {
  echo "the passphrase must be printable characters only" >&2
  exit 1
}

mkdir -p "${out}"
work="$(mktemp -d)"
root="${work}/root"
loop=""
cleanup() {
  cryptsetup close virtroid-root-build 2>/dev/null || true
  [ -n "${loop}" ] && losetup -d "${loop}" 2>/dev/null || true
  rm -rf "${work}"
}
trap cleanup EXIT

echo "building virtnoded and virtroid-viewercrypt"
(
  cd "${repo}/backend"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "${work}/virtnoded" ./cmd/virtnoded
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "${work}/virtroid-viewercrypt" ./cmd/viewercrypt
)

echo "bootstrapping Debian ${suite}"
debootstrap --variant=minbase \
  --include=ca-certificates,docker.io,nftables,iproute2,cryptsetup-bin,e2fsprogs,adb,procps \
  "${suite}" "${root}" "${mirror}"

install -m 0755 "${work}/virtnoded" "${root}/usr/local/bin/virtnoded"
install -m 0755 "${work}/virtroid-viewercrypt" "${root}/usr/local/bin/virtroid-viewercrypt"
rm -f "${root}/sbin/init"
install -m 0755 "${script_dir}/rootfs/virtroid-vm-init" "${root}/sbin/init"
install -d -m 0755 "${root}/etc/virtroid" "${root}/usr/share/virtroid"
install -m 0600 "${node_env}" "${root}/etc/virtroid/node.env"
cp -a "${apks}" "${root}/usr/share/virtroid/apks"
chown -R root:root "${root}/usr/share/virtroid/apks"
chmod -R go-w "${root}/usr/share/virtroid/apks"
# No logins: the VM is reached only through its attested endpoints.
chroot "${root}" passwd -l root >/dev/null
rm -rf "${root}/var/cache/apt/archives"/*.deb "${root}/var/lib/apt/lists"/*
echo "virtroid-runtime" > "${root}/etc/hostname"

echo "creating the root filesystem"
mkfs.ext4 -q -L virtroid-root -d "${root}" "${work}/rootfs.ext4" "${size_mib}M"

echo "encrypting it"
image="${out}/virtroid-rootfs.luks.img"
truncate -s "$((size_mib + 64))M" "${image}"
loop="$(losetup --find --show "${image}")"
printf '%s' "${passphrase}" | cryptsetup luksFormat --type luks2 --cipher aes-xts-plain64 --batch-mode --key-file - "${loop}"
printf '%s' "${passphrase}" | cryptsetup open --key-file - "${loop}" virtroid-root-build
dd if="${work}/rootfs.ext4" of=/dev/mapper/virtroid-root-build bs=4M conv=fsync status=none
e2fsck -fp /dev/mapper/virtroid-root-build || [ $? -lt 4 ]
resize2fs /dev/mapper/virtroid-root-build >/dev/null
cryptsetup close virtroid-root-build
losetup -d "${loop}"
loop=""

sha256sum "${image}" | tee "${image}.sha256"
cat <<EOF

Encrypted root disk: ${image}
Publish it with the provisioner's Aleph account (see deploy/aleph/README.md):
  aleph file upload ${image}      -> ALEPH_IMAGE
VM_DISK_MIB must be at least $((size_mib + 64)).
EOF
