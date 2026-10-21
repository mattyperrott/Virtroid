#!/bin/bash
# Boots a built runtime VM image under QEMU/KVM (no SEV) on its own kernel and
# verified root, runs its real /sbin/init (vm-init.sh) with test secrets, then
# starts an Android runtime container and its viewer the way virtnoded does.
# Needs an x86_64 Linux host with /dev/kvm, Nix, cpio and openssl.
#
#   deploy/aleph/test/boot-test.sh IMAGE_DIR     (IMAGE_DIR from build-image.sh)
#
# Prints "boot-test: PASS" and exits 0 when Android boots and the viewer
# listens. Test-only changes, all in a tmpfs overlay over the verified root:
# virtnoded runs with NODE_SNP_ATTESTATION=off and no reachable control plane.
#   BOOT_TEST_DEBUG=1   keep a failed VM up with a shell on ttyS1 (see vmsh)
#   BOOT_TEST_SED=EXPR  sed expression applied to the overlay copy of /sbin/init
#   BOOT_TEST_DIR       work directory (default /var/tmp/virtroid-boot-test)
set -euo pipefail

img="$(cd "${1:?usage: boot-test.sh IMAGE_DIR}" && pwd)"
flake="git+file://$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)?dir=deploy/aleph/nix"
work="${BOOT_TEST_DIR:-/var/tmp/virtroid-boot-test}"
rm -rf "$work"
mkdir -p "$work/initrd"
cd "$work"

zcat "$img/runtime/initrd" | (cd initrd && cpio -idm --quiet)
roothash=$(cat "$img/root/roothash")
offset=$(cat "$img/root/hash-offset")
node_key=$(openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 2>/dev/null |
  openssl pkcs8 -topk8 -nocrypt -outform DER | base64 -w0)

cat > initrd/init <<EOF
#!/bin/busybox sh
B=/bin/busybox
export PATH=/bin
\$B mkdir -p /proc /sys /dev /mnt /tmp
\$B mount -t proc proc /proc
\$B mount -t sysfs sys /sys
\$B mount -t devtmpfs dev /dev
# With BOOT_TEST_DEBUG=1 a failed run stays up with a shell on ttyS1 (see vmsh).
fail() {
  echo "boot-test: FAIL \$*"
  [ -n "${BOOT_TEST_DEBUG:-}" ] || \$B poweroff -f
  echo "boot-test: debug shell on ttyS1"
  while true; do \$B sh </dev/ttyS1 >/dev/ttyS1 2>&1; done
}
for m in dm-mod dm-bufio dm-verity dm-crypt; do \$B insmod /lib/modules/\$m.ko || fail "insmod \$m"; done
\$B mkdir -p /dev/mapper /run/cryptsetup
\$B mknod /dev/mapper/control c 10 236 2>/dev/null
\$B ip link set lo up
\$B ip link set eth0 up
\$B ip addr add 10.0.2.15/24 dev eth0
\$B ip route add default via 10.0.2.2

/bin/veritysetup open /dev/vda virtroid-root /dev/vda $roothash --hash-offset=$offset || fail "verity"
\$B mkdir -p /mnt/lower /mnt/ov /mnt/root
\$B mount -t erofs -o ro,nodev /dev/mapper/virtroid-root /mnt/lower || fail "mount root"
\$B mount -t tmpfs tmpfs /mnt/ov
\$B mkdir -p /mnt/ov/u /mnt/ov/w
\$B mount -t overlay overlay -o lowerdir=/mnt/lower,upperdir=/mnt/ov/u,workdir=/mnt/ov/w /mnt/root || fail "overlay"

# Test-only edits to the overlay copy of /sbin/init, from BOOT_TEST_SED.
[ -n "${BOOT_TEST_SED:-}" ] && \$B sed -i "${BOOT_TEST_SED:-}" /mnt/root/sbin/init
real=\$(\$B readlink /mnt/lower/bin/virtnoded)
\$B rm /mnt/root/bin/virtnoded
printf '#!/bin/bash\nNODE_SNP_ATTESTATION=off %s >/run/virtnoded.log 2>&1 &\nexec sleep infinity\n' "\$real" > /mnt/root/bin/virtnoded
\$B chmod 755 /mnt/root/bin/virtnoded

S=/mnt/root/tmp/secrets
\$B mkdir -p \$S
printf %s smoke-node > \$S/virtroid_node_id
printf %s '$node_key' > \$S/virtroid_node_key
printf %s snp-eu > \$S/virtroid_region
printf %s https://control.invalid > \$S/virtroid_control_plane
\$B mount -t proc proc /mnt/root/proc
\$B mount -t sysfs sys /mnt/root/sys
\$B mount -o bind /dev /mnt/root/dev
echo "nameserver 10.0.2.3" > /mnt/root/etc/resolv.conf

. /mnt/root/etc/virtroid/runtime.env
echo "boot-test: starting /sbin/init"
# As the measured initrd does: move the root over / and enter it.
cd /mnt/root
\$B mount --move . / || fail "move the root over /"
\$B chroot . /sbin/init &
init_pid=\$!
cd /
echo \$init_pid > /guest.pid

C() { \$B env -i HOME=/run/root PATH=/bin TMPDIR=/run/tmp \$B chroot /proc/\$init_pid/root "\$@"; }
# Memory of the VM and of the runtime's cgroup, read from sysfs and procfs only,
# so it cannot block behind a stuck process.
memory() {
  echo "boot-test: memory (\$1): \$(C /bin/bash -c 'grep -E "^(MemTotal|MemAvailable):" /proc/meminfo | tr -s " " | tr "\n" " "')"
  C /bin/bash -c 'for d in /sys/fs/cgroup/docker/*/; do echo "boot-test:   runtime cgroup current=\$(( \$(cat \$d/memory.current) >> 20 ))MiB max=\$(cat \$d/memory.max) \$(grep -E "^(anon|file) " \$d/memory.stat | tr "\n" " ") events: \$(tr "\n" " " < \$d/memory.events)"; done' 2>/dev/null
}
for i in \$(\$B seq 1 300); do
  kill -0 \$init_pid 2>/dev/null || fail "/sbin/init exited"
  # The init script starts virtnoded right after loading the image; its log
  # appearing means the node has started.
  C /bin/docker image inspect "\$NODE_RUNTIME_IMAGE" >/dev/null 2>&1 && C /bin/test -e /run/virtnoded.log && break
  \$B sleep 2
done
C /bin/docker image inspect "\$NODE_RUNTIME_IMAGE" >/dev/null 2>&1 || fail "runtime image never loaded"
echo "boot-test: docker up, image loaded"
\$B sleep 5
echo "boot-test: --- nft ruleset (virtroid) ---"
C /bin/nft list table inet virtroid
echo "boot-test: --- binderfs ---"
\$B ls /dev/binderfs
echo "boot-test: --- docker info ---"
C /bin/docker info 2>&1 | \$B grep -iE "storage driver|cgroup|warning|iptables|firewall"
echo "boot-test: --- virtnoded log ---"
C /bin/cat /run/virtnoded.log | \$B tail -15

echo "boot-test: --- node assets ---"
A=/srv/virtroid/assets
# virtnoded writes these just after it starts.
for i in \$(\$B seq 1 30); do
  C /bin/bash -c "cd \$A && test -s scrcpy-server.jar && test -s virtroid-viewercrypt && test -s virtroid-viewer.sh && test -s virtroid-viewer.rc" 2>/dev/null && break
  \$B sleep 2
done
C /bin/ls -la \$A
for f in scrcpy-server.jar virtroid-viewercrypt virtroid-viewer.sh virtroid-viewer.rc; do
  C /bin/test -s \$A/\$f || fail "virtnoded did not write \$A/\$f"
done
C /bin/mkdir -p /srv/virtroid/runtimes/smoke/data
echo "boot-test: starting a runtime container as virtnoded does (limits, mounts, arguments)"
. /mnt/lower/etc/virtroid/node.env 2>/dev/null || true
# A Tor runtime, set up as virtnoded does: its bridge joins the firewall's Tor
# set before it exists. Nothing from it is forwarded, so Android resolving a
# name below proves its DNS went through Tor.
C /bin/nft add element inet virtroid tor_bridges '{ "vrt-smoke" }' || fail "add the Tor bridge"
C /bin/docker network create -o com.docker.network.bridge.name=vrt-smoke virtroid-guest-smoke >/dev/null || fail "runtime network"
C /bin/docker run -d --name smoke --privileged --network virtroid-guest-smoke \
  -p 127.0.0.1:15555:5555 \
  --memory \${NODE_RUNTIME_MEMORY_BYTES:-3221225472} --memory-swap \${NODE_RUNTIME_MEMORY_BYTES:-3221225472} \
  --cpus 2 --pids-limit \${NODE_RUNTIME_PIDS_LIMIT:-4096} --shm-size \${NODE_RUNTIME_SHM_BYTES:-268435456} \
  --log-driver local --log-opt max-size=10m --log-opt max-file=3 \
  -v /srv/virtroid/runtimes/smoke/data:/data \
  -v \$A/scrcpy-server.jar:/opt/virtroid/scrcpy-server.jar:ro \
  -v \$A/virtroid-viewercrypt:/opt/virtroid/virtroid-viewercrypt:ro \
  -v \$A/virtroid-viewer.sh:/vendor/bin/virtroid-viewer.sh:ro \
  -v \$A/virtroid-viewer.rc:/vendor/etc/init/virtroid-viewer.rc:ro \
  "\$NODE_RUNTIME_IMAGE" androidboot.use_memfd=1 androidboot.redroid_gpu_mode=guest \
  androidboot.redroid_gpu_node=auto androidboot.redroid_fps=30 androidboot.redroid_width=720 \
  androidboot.redroid_height=1280 androidboot.redroid_dpi=320 || fail "docker run"
booted=""
for i in \$(\$B seq 1 90); do
  C /bin/adb connect 127.0.0.1:15555 >/dev/null 2>&1
  booted=\$(C /bin/adb -s 127.0.0.1:15555 shell getprop sys.boot_completed 2>/dev/null | \$B tr -d '\r')
  [ "\$booted" = 1 ] && break
  \$B sleep 4
done
C /bin/docker ps -a
C /bin/docker logs smoke 2>&1 | \$B tail -10
if [ "\$booted" != 1 ]; then
  echo "boot-test: --- android services ---"
  C /bin/docker exec smoke /system/bin/getprop | \$B grep -E "init.svc|boot|bootanim" | \$B head -80
  echo "boot-test: --- crash log ---"
  C /bin/docker exec smoke /system/bin/logcat -d -b crash | \$B tail -60
  echo "boot-test: --- errors ---"
  C /bin/docker exec smoke /system/bin/logcat -d '*:E' | \$B tail -80
  \$B ls /proc/pressure 2>&1
  fail "Android did not finish booting"
fi
memory "Android booted"
echo "boot-test: Android booted: \$(C /bin/adb -s 127.0.0.1:15555 shell getprop ro.build.fingerprint | \$B tr -d '\r')"
echo "boot-test: Android security patch: \$(C /bin/adb -s 127.0.0.1:15555 shell getprop ro.build.version.security_patch | \$B tr -d '\r')"
# Android's own networking (netd's firewall and routing, its DNS resolver)
# must work for apps: resolve a name from inside Android, through Tor (which
# may still be bootstrapping).
C /bin/bash -c 'grep -m1 "Bootstrapped 100%" /var/log/tor.log || tail -3 /var/log/tor.log'
resolved=""
for i in \$(\$B seq 1 90); do
  resolved=\$(C /bin/adb -s 127.0.0.1:15555 shell 'ping -c 1 -W 2 dns.google 2>&1 | head -1' | \$B tr -d '\r')
  echo "\$resolved" | \$B grep -q "^PING dns.google (" && break
  \$B sleep 2
done
echo "boot-test: network: \$resolved"
echo "\$resolved" | \$B grep -q "^PING dns.google (" || fail "Android cannot resolve names through Tor"
# Tor maps a .onion name to an address in its VirtualAddrNetwork (10.192.0.0/10)
# itself, so this checks the mapping without reaching the service.
onion=duckduckgogg42xjoc72x3sjasowoarfbgcmvfimaftt6twagswzczad.onion
mapped=\$(C /bin/adb -s 127.0.0.1:15555 shell "ping -c 1 -W 2 \$onion 2>&1 | head -1" | \$B tr -d '\r')
echo "boot-test: onion: \$mapped"
echo "\$mapped" | \$B grep -qE "^PING \$onion \\(10\\.(19[2-9]|2[0-5][0-9])\\." || fail "Android cannot resolve .onion names through Tor"
# As virtnoded's startViewerService does: Android's init starts the viewer
# service, so it runs with Android's environment (app_process needs
# ANDROID_ART_ROOT and friends, which docker exec does not pass).
echo "boot-test: starting the viewer as virtnoded does"
# The node leaves the runtime's viewer key for viewercrypt, which reads and
# removes it; this test's key is a dummy.
\$B timeout 60 C /bin/docker exec -e VIRTROID_VIEWER_AUTH_KEY=WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo= smoke /system/bin/sh -c '(umask 077; echo "\$VIRTROID_VIEWER_AUTH_KEY" > /data/local/tmp/virtroid-viewer-auth.key) && chown 1000:1000 /data/local/tmp/virtroid-viewer-auth.key'
\$B timeout 60 C /bin/docker exec smoke /system/bin/sh -c "setprop virtroid.viewer.client_ip 127.0.0.1; setprop virtroid.viewer.max_size 1280; setprop virtroid.viewer.bit_rate 4000000; setprop virtroid.viewer.audio_enabled false; setprop ctl.start virtroid_viewer"
listening=""
for i in \$(\$B seq 1 30); do
  listening=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "[ -S /data/local/tmp/virtroid-viewer/viewer.sock ] && [ -S /data/local/tmp/virtroid-viewer/scrcpy.sock ] && echo sockets" 2>/dev/null)
  [ -n "\$listening" ] && break
  \$B sleep 1
done
\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "getprop init.svc.virtroid_viewer; tail -5 /data/local/tmp/virtroid-viewer.log" 2>&1
memory "viewer started"
if [ -z "\$listening" ]; then
  # Where the streaming server is stuck: thread states, wait channels and
  # kernel stacks, read without anything that waits on the process's memory.
  echo "boot-test: --- viewer diagnostics ---"
  \$B timeout 60 C /bin/docker exec -d smoke /system/bin/sh -c 'CLASSPATH=/opt/virtroid/scrcpy-server.jar app_process / org.server.scrcpy.Server /127.0.0.1 1280 4000000 false false >/dev/null 2>&1 </dev/null'
  \$B sleep 20
  for c in /proc/[0-9]*/comm; do
    p=\${c%/comm}; p=\${p#/proc/}
    comm=\$(\$B cat \$c 2>/dev/null)
    state=\$(\$B grep -m1 '^State:' /proc/\$p/status 2>/dev/null | \$B cut -c8-)
    case "\$comm:\$state" in app_process*|main:*|dex2oat*|*:D*) ;; *) continue ;; esac
    echo "boot-test: pid \$p \$comm state=\$state \$(\$B grep -m1 '^Uid:' /proc/\$p/status)"
    for t in /proc/\$p/task/*; do
      echo "boot-test:   task \${t##*/} \$(\$B cat \$t/comm) \$(\$B grep -m1 '^State:' \$t/status | \$B cut -c8-) wchan=\$(\$B cat \$t/wchan)"
      \$B timeout 5 \$B cat \$t/stack 2>/dev/null | \$B head -6 | \$B sed 's/^/boot-test:     /'
    done | \$B head -80
  done
  fail "the viewer's sockets never opened"
fi
echo "boot-test: viewer listening on its private sockets"
# Apps share the runtime's network, so no viewer hop may be a TCP port, and the
# sockets' directory must be the viewer's (system) alone.
tcp=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "ss -ltn 2>/dev/null | grep -E ':(7007|7017) '" 2>/dev/null)
[ -z "\$tcp" ] || fail "the viewer still listens on a TCP port: \$tcp"
perms=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "stat -c '%a %U' /data/local/tmp/virtroid-viewer" 2>/dev/null | \$B tr -d '\r')
echo "boot-test: viewer socket directory: \$perms"
[ "\$perms" = "700 system" ] || fail "the viewer socket directory is open to others: \$perms"
leftover=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "ls /data/local/tmp/virtroid-viewer-auth.key 2>/dev/null")
[ -z "\$leftover" ] || fail "viewercrypt left the viewer key on disk"
# A viewer without the runtime's viewer key gets only viewercrypt's greeting
# (magic, length, its 91-byte key, a 32-byte nonce: 133 bytes), never its
# proof, and must not reach the streaming server, which accepts one client.
unproven=\$(\$B timeout 30 C /bin/docker exec smoke /system/bin/sh -c "{ printf 'VRTENC2\\n\\000\\133'; echo MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEYlO/iFyod1pk3kttzLxSC0rsI1hKh2mrC2lnpQ69pt+pY5Yrf3XjsHU2flyVEAIdeJwPTdrGTSBksXJRZg9JXg== | toybox base64 -d; toybox head -c 32 /dev/zero; sleep 3; } | toybox timeout 8 toybox nc -U /data/local/tmp/virtroid-viewer/viewer.sock | toybox wc -c" 2>/dev/null | \$B tr -dc '0-9')
echo "boot-test: a viewer without the key received \${unproven:-0} bytes"
[ "\${unproven:-0}" = 133 ] || fail "viewercrypt answered a viewer without the key with \${unproven:-0} bytes, not just its 133-byte greeting"
still=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "[ -S /data/local/tmp/virtroid-viewer/scrcpy.sock ] && echo open")
[ "\$still" = open ] || fail "a viewer without the key reached the streaming server"
# Pull video as a viewer would, straight from the streaming server (the
# encryption proxy dials it the same way for each client), for 12 seconds:
# a working stream does not end on its own. The screen size alone is 8
# bytes; a working encoder sends at least a key frame.
\$B timeout 40 C /bin/docker exec smoke /system/bin/sh -c "(sleep 20) | timeout 12 toybox nc -U /data/local/tmp/virtroid-viewer/scrcpy.sock | wc -c" >/run/streamed 2>/dev/null &
stream_pid=\$!
# While it streams, the capture display must be secure: windows apps mark
# as secure are drawn only on secure displays, and are black otherwise.
\$B sleep 6
secure=\$(\$B timeout 15 C /bin/docker exec smoke /system/bin/sh -c "dumpsys display | grep -m1 -E 'scrcpy.*FLAG_SECURE'" 2>/dev/null)
wait \$stream_pid
streamed=\$(\$B tr -dc '0-9' </run/streamed)
echo "boot-test: streamed \${streamed:-0} bytes in 12s"
echo "boot-test: capture display: \$(echo "\${secure:-not secure}" | \$B cut -c1-160)"
if [ "\${streamed:-0}" -lt 10000 ]; then
  \$B timeout 30 C /bin/docker exec smoke /system/bin/sh -c "tail -20 /data/local/tmp/virtroid-viewer.log; logcat -d -b main,system,crash 2>/dev/null | grep -iE 'scrcpy|codec|C2|MediaCodec|dma_heap|AndroidRuntime|FATAL' | tail -40" 2>&1 | \$B sed 's/^/boot-test:   /'
  fail "the viewer did not stream video"
fi
if [ -z "\$secure" ]; then
  \$B timeout 30 C /bin/docker exec smoke /system/bin/sh -c "tail -20 /data/local/tmp/virtroid-viewer.log" 2>&1 | \$B sed 's/^/boot-test:   /'
  fail "the viewer's capture display is not secure, so protected windows would be black"
fi
# A congested viewer: while the screen keeps changing (an idle screen sends
# only about 14 KB/s), read the stream at about 512 kbps for 40 seconds. The
# encoder must lower its bitrate rather than let video queue on the way.
echo "boot-test: reading the stream slowly, as a congested viewer would"
\$B timeout 60 C /bin/docker exec -e VIRTROID_VIEWER_AUTH_KEY=WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo= smoke /system/bin/sh -c '(umask 077; echo "\$VIRTROID_VIEWER_AUTH_KEY" > /data/local/tmp/virtroid-viewer-auth.key) && chown 1000:1000 /data/local/tmp/virtroid-viewer-auth.key'
\$B timeout 60 C /bin/docker exec smoke /system/bin/sh -c "setprop ctl.stop virtroid_viewer; sleep 3; setprop ctl.start virtroid_viewer"
for i in \$(\$B seq 1 30); do
  \$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "[ -S /data/local/tmp/virtroid-viewer/scrcpy.sock ]" 2>/dev/null && break
  \$B sleep 1
done
\$B timeout 60 C /bin/docker exec -d smoke /system/bin/sh -c "i=0; while [ \\\$i -lt 60 ]; do am start -W -a android.settings.SETTINGS >/dev/null 2>&1; sleep 0.7; input keyevent 3; sleep 0.7; i=\\\$((i+1)); done"
\$B timeout 60 C /bin/docker exec smoke /system/bin/sh -c "(sleep 50) | toybox timeout 40 toybox nc -U /data/local/tmp/virtroid-viewer/scrcpy.sock | (i=0; n=0; while [ \\\$i -lt 160 ]; do c=\\\$(toybox dd bs=16384 count=1 2>/dev/null | toybox wc -c); n=\\\$((n+c)); sleep 0.25; i=\\\$((i+1)); done; echo \\\$n > /data/local/tmp/slow-read.count)" >/dev/null 2>&1
slow=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "cat /data/local/tmp/slow-read.count 2>/dev/null" | \$B tr -dc '0-9')
adapted=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "grep -E 'Video bitrate|Cannot change the video bitrate' /data/local/tmp/virtroid-viewer.log | tail -4" 2>/dev/null | \$B tr -d '\r')
echo "boot-test: the slow viewer read \${slow:-0} bytes; bitrate:"
echo "\${adapted:-  unchanged}" | \$B sed 's/^/boot-test:   /'
if ! echo "\$adapted" | \$B grep -q "Video bitrate"; then
  \$B timeout 30 C /bin/docker exec smoke /system/bin/sh -c "tail -25 /data/local/tmp/virtroid-viewer.log" 2>&1 | \$B sed 's/^/boot-test:   /'
  fail "the encoder did not lower its bitrate for a slow viewer"
fi
echo "boot-test: cpu.max \$(C /bin/bash -c 'cat /sys/fs/cgroup/docker/*/cpu.max' 2>&1 | \$B head -1)"
echo "boot-test: lmkd \$(C /bin/adb -s 127.0.0.1:15555 shell getprop init.svc.lmkd | \$B tr -d '\r'), docker exec: \$(C /bin/docker exec smoke /system/bin/getprop sys.boot_completed)"
echo "boot-test: --- virtnoded log ---"
C /bin/cat /run/virtnoded.log | \$B tail -8
echo "boot-test: PASS"
\$B poweroff -f
EOF
chmod 755 initrd/init
# C runs a command in the VM's root, for the debug shell.
printf '#!/bin/busybox sh\nexec /bin/busybox env -i HOME=/run/root PATH=/bin TMPDIR=/run/tmp /bin/busybox chroot /proc/$(/bin/busybox cat /guest.pid)/root "$@"\n' > initrd/bin/C
chmod 755 initrd/bin/C
(cd initrd && find . | cpio -o -H newc --quiet | gzip -1) > initrd.gz
truncate -s 8G data.img

qemu=$(nix --extra-experimental-features 'nix-command flakes' build --no-link --print-out-paths \
  --inputs-from "$flake" nixpkgs#qemu_kvm)
timeout "${BOOT_TEST_TIMEOUT:-1200}" "$qemu/bin/qemu-system-x86_64" -enable-kvm -cpu host -smp 2 -m 4096 \
  -nographic -no-reboot -serial mon:stdio -serial "unix:$work/debug.sock,server=on,wait=off" \
  -kernel "$img/runtime/bzImage" -initrd initrd.gz -append "console=ttyS0 panic=-1 loglevel=4" \
  -drive "file=$img/root/root.img,format=raw,if=virtio,readonly=on" \
  -drive file=data.img,format=raw,if=virtio \
  -netdev user,id=n0,ipv6-net=2001:db8:1::/64 -device virtio-net-pci,netdev=n0 | tee "$work/console.log"
grep -aq "boot-test: PASS" "$work/console.log"
