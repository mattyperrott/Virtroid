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
data_key=$(openssl rand -hex 32)

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
\$B mount -t ext4 -o ro,nodev /dev/mapper/virtroid-root /mnt/lower || fail "mount root"
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
printf %s '$data_key' > \$S/virtroid_data_key
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
  C /bin/docker image inspect "\$NODE_RUNTIME_IMAGE" >/dev/null 2>&1 && C /bin/docker network inspect virtroid-guest >/dev/null 2>&1 && break
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
C /bin/ls -la \$A
for f in scrcpy-server.jar virtroid-viewercrypt virtroid-viewer.sh virtroid-viewer.rc; do
  C /bin/test -s \$A/\$f || fail "virtnoded did not write \$A/\$f"
done
C /bin/mkdir -p /srv/virtroid/runtimes/smoke/data
echo "boot-test: starting a runtime container as virtnoded does (limits, mounts, arguments)"
. /mnt/lower/etc/virtroid/node.env 2>/dev/null || true
C /bin/docker run -d --name smoke --privileged --network virtroid-guest \
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
# Android's own networking (netd's firewall and routing, its DNS resolver)
# must work for apps: resolve a name from inside Android.
resolved=""
for i in \$(\$B seq 1 30); do
  resolved=\$(C /bin/adb -s 127.0.0.1:15555 shell 'ping -c 1 -W 2 dns.google 2>&1 | head -1' | \$B tr -d '\r')
  echo "\$resolved" | \$B grep -q "^PING dns.google (" && break
  \$B sleep 2
done
echo "boot-test: network: \$resolved"
echo "\$resolved" | \$B grep -q "^PING dns.google (" || fail "Android cannot resolve names"
# As virtnoded's startViewerService does: Android's init starts the viewer
# service, so it runs with Android's environment (app_process needs
# ANDROID_ART_ROOT and friends, which docker exec does not pass).
echo "boot-test: starting the viewer as virtnoded does"
\$B timeout 60 C /bin/docker exec smoke /system/bin/sh -c "setprop virtroid.viewer.client_ip 127.0.0.1; setprop virtroid.viewer.max_size 1280; setprop virtroid.viewer.bit_rate 4000000; setprop virtroid.viewer.audio_enabled false; setprop ctl.start virtroid_viewer"
listening=""
for i in \$(\$B seq 1 30); do
  listening=\$(\$B timeout 20 C /bin/docker exec smoke /system/bin/sh -c "ss -ltn 2>/dev/null | grep -E ':(7007|7017) '" 2>/dev/null)
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
  fail "the viewer never listened on 7007/7017"
fi
echo "boot-test: viewer listening: \$(echo \$listening | \$B tr -s ' ')"
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
