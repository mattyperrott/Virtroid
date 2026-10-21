{ pkgs, lib, virtroid-node, assets, runtimeImageTar, runtimeImageRef, vmInit, ... }:

# The read-only root of a Virtroid runtime VM: Docker, cryptsetup, nftables,
# adb, Tor, virtnoded, the Android runtime image and the pinned apps, and
# nothing else (no shell login, SSH, package manager or compiler). It is a
# compressed EROFS image mounted under dm-verity, so it is built
# reproducibly: fixed ownership, timestamps and UUID.
#
# `assets` holds the deployment's own inputs, measured like everything else:
#   node.env            virtnoded's fixed settings (see assets-example/)
#   apks/               the pinned APKs (the runtime agent, optional manifest.json)
# and the Android runtime image comes in as runtimeImageTar (see flake.nix).
let
  # Docker's daemon, containerd and runc without what nixpkgs' docker adds for
  # building images (buildx, compose), rootless mode, or journald logging,
  # which links systemd. The node uses none of them.
  docker = pkgs.docker.override {
    withSystemd = false;
    withBtrfs = false;
    withLvm = false;
    buildxSupport = false;
    composeSupport = false;
  };
  # Only the binaries: nixpkgs' wrappers put git, xfsprogs, xz and more on
  # dockerd's PATH, and all of that would land in the root with them.
  dockerBinaries = pkgs.runCommand "virtroid-docker" { } ''
    mkdir -p $out/bin
    libexec=$(dirname "$(dirname "$(readlink -f ${docker}/bin/dockerd)")")/libexec/docker
    for bin in dockerd docker-proxy containerd containerd-shim-runc-v2 runc; do
      cp -L "$libexec/$bin" "$out/bin/$bin"
    done
    cp -L ${docker}/libexec/docker/docker $out/bin/docker
  '';
  # Tor without systemd (it would only use sd_notify), for runtimes whose
  # traffic leaves through it (vm-init.sh).
  tor = pkgs.tor.overrideAttrs (old: {
    buildInputs = lib.filter (p: (p.pname or "") != "systemd") old.buildInputs;
    configureFlags = (old.configureFlags or [ ]) ++ [ "--disable-systemd" ];
  });
  # TransPort and DNSPort take what the firewall redirects from Tor runtimes'
  # bridges; IsolateClientAddr (the default, stated here) gives each runtime
  # its own circuits. Nothing else listens: no SOCKS, no relay, and the
  # control socket is for virtnoded (root) only.
  #
  # DNSPort answers a .onion name with an address from VirtualAddrNetwork,
  # and a connection to it through TransPort reaches that onion service.
  # Tor's default range is loopback, which a runtime's packets could never
  # carry to the VM, so it is 10.192.0.0/10 (outside Docker's pools).
  torrc = pkgs.writeText "torrc" ''
    User tor
    DataDirectory /srv/virtroid/tor
    ControlSocket /run/tor/control
    SocksPort 0
    TransPort 0.0.0.0:9040 IsolateClientAddr
    DNSPort 0.0.0.0:5353 IsolateClientAddr
    AutomapHostsOnResolve 1
    AutomapHostsSuffixes .onion
    VirtualAddrNetworkIPv4 10.192.0.0/10
    ClientOnly 1
    AvoidDiskWrites 1
    Log notice stdout
  '';
  # adb alone: the rest of android-tools brings Python.
  adb = pkgs.runCommand "virtroid-adb" { } ''
    mkdir -p $out/bin
    cp -L ${pkgs.android-tools}/bin/adb $out/bin/adb
  '';

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
    dockerBinaries
    adb
    tor
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
pkgs.runCommand "virtroid-rootfs.erofs" {
  nativeBuildInputs = [ pkgs.erofs-utils ];
  SOURCE_DATE_EPOCH = "0";
} ''
  for required in node.env apks; do
    [ -e ${assets}/$required ] || { echo "assets are missing $required" >&2; exit 1; }
  done

  mkdir -p rootfs
  cd rootfs
  mkdir -p nix/store bin sbin etc/ssl/certs etc/tor etc/virtroid usr/bin usr/local/bin usr/share/virtroid \
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
  install -m 0644 ${torrc} etc/tor/torrc
  install -m 0644 ${runtimeImageTar} usr/share/virtroid/runtime-image.tar
  # The image this root carries is the only one the node may run.
  printf 'NODE_RUNTIME_IMAGE=%s\nNODE_REQUIRE_DIGESTED_RUNTIME_IMAGE=false\n' '${runtimeImageRef}' \
    > etc/virtroid/runtime.env
  cp -r ${assets}/apks usr/share/virtroid/apks
  chmod -R u+w,go-w usr/share/virtroid/apks

  # The initrd bind-mounts DHCP's resolv.conf over this placeholder.
  touch etc/resolv.conf
  printf 'root:x:0:0:root:/run/root:/bin/false\ntor:x:990:990:tor:/var/empty:/bin/false\n' > etc/passwd
  printf 'root:x:0:\ndocker:x:998:\ntor:x:990:\n' > etc/group
  printf 'hosts: files dns\n' > etc/nsswitch.conf
  printf '127.0.0.1 localhost\n::1 localhost\n' > etc/hosts
  printf 'ID=virtroid\nNAME="Virtroid runtime VM"\n' > etc/os-release
  cd ..

  # A compressed, read-only EROFS image: Android images compress well, and the
  # root is downloaded by every new VM. Fixed timestamps, owner and UUID keep
  # it reproducible.
  mkfs.erofs --quiet -zzstd,level=15 -C262144 -Ededupe,all-fragments,ztailpacking \
    -T0 --all-root -U 00000000-0000-0000-0000-000000000000 \
    $out rootfs
''
