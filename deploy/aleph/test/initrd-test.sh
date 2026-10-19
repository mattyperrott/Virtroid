#!/bin/bash
# Boots a built runtime VM image under QEMU/KVM (no SEV) with its real measured
# initrd and verified root. The secrets the provisioner would inject are
# placed in the initramfs beforehand, and the attestation agent, which needs
# SEV-SNP, is replaced by a stand-in that stays up. Everything else is the real
# boot path: the initrd verifies the root, moves it over / and starts
# vm-init.sh, which unlocks the data disk, starts Docker, loads the Android
# image and starts virtnoded. Without SNP, virtnoded then stops at
# "SNP attestation required"; reaching that line is the pass condition.
#
#   deploy/aleph/test/initrd-test.sh IMAGE_DIR    (IMAGE_DIR from build-image.sh)
set -euo pipefail

img="$(cd "${1:?usage: initrd-test.sh IMAGE_DIR}" && pwd)"
flake="git+file://$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)?dir=deploy/aleph/nix"
work="${BOOT_TEST_DIR:-/var/tmp/virtroid-initrd-test}"
rm -rf "$work"
mkdir -p "$work/initrd/tmp/secrets"
cd "$work"

zcat "$img/runtime/initrd" | (cd initrd && cpio -idm --quiet)
s=initrd/tmp/secrets
printf %s test-node > $s/virtroid_node_id
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 2>/dev/null |
  openssl pkcs8 -topk8 -nocrypt -outform DER | base64 -w0 > $s/virtroid_node_key
printf %s snp-eu > $s/virtroid_region
printf %s https://control.invalid > $s/virtroid_control_plane
chmod 0700 $s
printf '#!/bin/busybox sh\nexec /bin/busybox sleep 999999\n' > initrd/bin/aleph-attest-agent
chmod 755 initrd/bin/aleph-attest-agent
(cd initrd && find . | cpio -o -H newc --quiet | gzip -1) > initrd.gz
truncate -s 8G data.img

qemu=$(nix --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths \
  --inputs-from "$flake" nixpkgs#qemu_kvm)
timeout "${BOOT_TEST_TIMEOUT:-900}" "$qemu/bin/qemu-system-x86_64" -enable-kvm -cpu host -smp 2 -m 4096 \
  -nographic -no-reboot \
  -kernel "$img/runtime/bzImage" -initrd initrd.gz \
  -append "console=ttyS0 luks=1 owner=0x0000000000000000000000000000000000000001" \
  -drive "file=$img/root/root.img,format=raw,if=virtio,readonly=on" \
  -drive file=data.img,format=raw,if=virtio \
  -netdev user,id=n0,ipv6-net=2001:db8:1::/64 -device virtio-net-pci,netdev=n0 | tee "$work/console.log" || true
grep -aq "virtroid-init: node test-node at" "$work/console.log" &&
  grep -aq "SNP attestation required but unavailable" "$work/console.log" &&
  echo "initrd-test: PASS"
