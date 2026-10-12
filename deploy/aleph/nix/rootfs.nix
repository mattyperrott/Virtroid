{ pkgs, lib, virtroid-node, assets, runtimeImageTar, runtimeImageRef, vmInit, ... }:

# The read-only root of a Virtroid runtime VM: Docker, cryptsetup, nftables,
# adb, virtnoded, the Android runtime image and the pinned apps, and nothing
# else (no shell login, SSH, package manager or compiler). It is mounted under
# dm-verity, so it is built reproducibly: fixed ownership, timestamps, UUID
# and hash seed, after aleph-vm's nix/compose-rootfs.nix (MIT licence,
# Copyright (c) 2021-2024 ALEPH.IM SAS).
#
# `assets` holds the deployment's own inputs, measured like everything else:
#   node.env            virtnoded's fixed settings (see assets-example/)
#   apks/               the pinned APKs (the runtime agent, optional manifest.json)
# and the Android runtime image comes in as runtimeImageTar (see flake.nix).
let
  packages = with pkgs; [
    bashNonInteractive
    coreutils
    findutils
    gawk
    gnugrep
    gnused
    util-linux
    procps
    iproute2
    nftables
    iptables
    cryptsetup
    e2fsprogs
    docker
    android-tools
    virtroid-node
  ];

  env = pkgs.buildEnv {
    name = "virtroid-root-env";
    paths = packages;
    pathsToLink = [ "/bin" "/libexec" "/share" ];
    ignoreCollisions = true;
  };

  closure = pkgs.closureInfo { rootPaths = [ env pkgs.cacert pkgs.iana-etc ]; };
in
pkgs.runCommand "virtroid-rootfs.ext4" {
  nativeBuildInputs = [ pkgs.e2fsprogs pkgs.fakeroot ];
  SOURCE_DATE_EPOCH = "0";
} ''
  for required in node.env apks; do
    [ -e ${assets}/$required ] || { echo "assets are missing $required" >&2; exit 1; }
  done

  mkdir -p rootfs
  cd rootfs
  mkdir -p nix/store bin sbin etc/ssl/certs etc/virtroid usr/bin usr/local/bin usr/share/virtroid \
    proc sys dev run var tmp/secrets mnt srv/virtroid root
  chmod 1777 tmp
  chmod 0700 tmp/secrets root

  for path in $(cat ${closure}/store-paths); do
    cp -a "$path" nix/store/
  done
  for bin in ${env}/bin/*; do
    ln -s "$(readlink -f "$bin")" "bin/$(basename "$bin")"
  done
  ln -s /bin/adb usr/bin/adb
  ln -s /bin/viewercrypt usr/local/bin/virtroid-viewercrypt
  ln -s ${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt etc/ssl/certs/ca-certificates.crt
  # nft and iptables resolve protocol names (ipv6-icmp, tcp) through these.
  ln -s ${pkgs.iana-etc}/etc/protocols etc/protocols
  ln -s ${pkgs.iana-etc}/etc/services etc/services

  install -m 0755 ${vmInit} sbin/init
  install -m 0600 ${assets}/node.env etc/virtroid/node.env
  install -m 0644 ${runtimeImageTar} usr/share/virtroid/runtime-image.tar
  # The image this root carries is the only one the node may run.
  printf 'NODE_RUNTIME_IMAGE=%s\nNODE_REQUIRE_DIGESTED_RUNTIME_IMAGE=false\n' '${runtimeImageRef}' \
    > etc/virtroid/runtime.env
  cp -r ${assets}/apks usr/share/virtroid/apks
  chmod -R u+w,go-w usr/share/virtroid/apks

  # The initrd bind-mounts DHCP's resolv.conf over this placeholder.
  touch etc/resolv.conf
  printf 'root:x:0:0:root:/run/root:/bin/false\n' > etc/passwd
  printf 'root:x:0:\ndocker:x:998:\n' > etc/group
  printf 'hosts: files dns\n' > etc/nsswitch.conf
  printf '127.0.0.1 localhost\n::1 localhost\n' > etc/hosts
  printf 'ID=virtroid\nNAME="Virtroid runtime VM"\n' > etc/os-release
  cd ..

  size=$(du -sm rootfs | cut -f1)
  size=$(( size + size / 10 + 64 ))
  truncate -s "''${size}M" $out
  # Reproducible mkfs: see aleph-vm nix/rootfs.nix for each lever.
  fakeroot -- sh -c '
    chown -R 0:0 rootfs
    mkfs.ext4 -b 4096 -U 00000000-0000-0000-0000-000000000000 \
      -E hash_seed=a1e5c0de-1111-2222-3333-444455556666,lazy_itable_init=0,lazy_journal_init=0 \
      -O ^has_journal -d rootfs '"$out"'
  '
''
