{ pkgs, attest-agent, init-script, init-common-script, udhcpc-script, udhcpc6-script, ... }:

# The measured initrd of a Virtroid runtime VM. Adapted from aleph-vm's
# nix/initrd.nix (MIT licence, Copyright (c) 2021-2024 ALEPH.IM SAS), keeping
# its content-only archive: every file sits at its final path as a regular
# file with a fixed mode and timestamp, so the SEV-SNP launch measurement
# depends on file contents alone, never on Nix store paths.
#
# The changes: no kernel modules. Ours builds device-mapper in and can't load
# modules at all (kernel-virtroid.fragment), so the archive has no
# lib/modules. And only the tools init-virtroid.sh runs: veritysetup for the
# root, no cryptsetup (vm-init.sh unlocks the data disk with the root's), no
# nft, no GPU flavour.
let
  # No loader or libc in here: every binary must be statically linked.
  staticCryptsetup = pkgs.pkgsStatic.cryptsetup;

  # init-common.sh treats a failing udhcpc6 as "stay IPv4-only", so a busybox
  # without the applet would quietly drop the guest's IPv6. Fail the build
  # instead; a nixpkgs bump is the only way it could change.
  checkedBusybox = pkgs.runCommand "busybox-with-udhcpc6" { } ''
    ${pkgs.pkgsStatic.busybox}/bin/busybox --list | grep -qx udhcpc6 || {
      echo "error: busybox lacks the udhcpc6 applet; guest DHCPv6 would silently no-op." >&2
      exit 1
    }
    mkdir -p $out/bin
    cp ${pkgs.pkgsStatic.busybox}/bin/busybox $out/bin/busybox
  '';

  entries = [
    { source = "${checkedBusybox}/bin/busybox"; path = "bin/busybox"; mode = "755"; }
    { source = init-script; path = "init"; mode = "755"; }
    { source = init-common-script; path = "bin/init-common.sh"; mode = "755"; }
    { source = udhcpc-script; path = "bin/udhcpc.script"; mode = "755"; }
    { source = udhcpc6-script; path = "bin/udhcpc6.script"; mode = "755"; }
    { source = "${attest-agent}/bin/aleph-attest-agent"; path = "bin/aleph-attest-agent"; mode = "755"; }
    { source = "${staticCryptsetup}/bin/veritysetup"; path = "bin/veritysetup"; mode = "755"; }
  ];

  installEntries = pkgs.lib.concatMapStringsSep "\n"
    (e: "install -D -m ${e.mode} ${e.source} root/${e.path}")
    entries;
in
pkgs.runCommand "initrd" {
  nativeBuildInputs = [ pkgs.cpio pkgs.gzip pkgs.file ];
} ''
  mkdir -p root/dev root/proc root/sys
  ${installEntries}

  for f in root/bin/* root/init; do
    if file -b "$f" | grep -q 'dynamically linked'; then
      echo "error: $f is dynamically linked; the content-only initrd cannot run it" >&2
      exit 1
    fi
  done

  # nixpkgs' makeInitrd recipe (fixed mtime, sorted entries, root ownership,
  # gzip -9n) without the store closure.
  (cd root && find . -mindepth 1 -exec touch -h -d '@1' '{}' +)
  (cd root && find . -mindepth 1 -printf '%P\0' | sort -z \
    | cpio --quiet -o -H newc -R +0:+0 --reproducible --null \
    | gzip -9n > ../initrd.gz)

  if gzip -dc initrd.gz | cpio -t --quiet | grep -q '^nix/'; then
    echo "error: initrd embeds nix store paths; the launch measurement would track derivation hashes" >&2
    exit 1
  fi
  if gzip -dc initrd.gz | cpio -t --quiet | grep -q '\.ko$'; then
    echo "error: initrd carries a kernel module; this kernel cannot load one" >&2
    exit 1
  fi

  mkdir -p $out
  mv initrd.gz $out/initrd
  ln -s initrd $out/initrd.gz
''
