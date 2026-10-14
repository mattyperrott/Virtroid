#!/bin/bash
# /sbin/init of the Virtroid runtime VM's read-only root. The measured initrd
# runs it in a chroot once the root is verified and the provisioner has
# injected this VM's secrets. It must stay in the foreground: when it exits,
# the VM powers off.
set -uo pipefail
export PATH=/bin
export TMPDIR=/run/tmp

log() { echo "virtroid-init: $*"; }
die() { log "FATAL: $*"; exit 1; }

secrets=/tmp/secrets
data_device=/dev/vdb
data_mapper=virtroid-data
public_port=9443

# --- Writable state lives on tmpfs or the encrypted data disk ----------------
mount -t tmpfs -o mode=0755,nosuid,nodev tmpfs /run || die "mount /run"
mount -t tmpfs -o mode=0755,nosuid,nodev tmpfs /var || die "mount /var"
mkdir -p /run/tmp /run/root /var/run /var/log /var/lib /dev/pts /dev/shm /dev/binderfs
chmod 1777 /run/tmp
export HOME=/run/root
mount -t devpts -o gid=5,mode=0620,ptmxmode=0666 devpts /dev/pts 2>/dev/null || true
mount -t tmpfs -o mode=1777,nosuid,nodev tmpfs /dev/shm || die "mount /dev/shm"
mount -t cgroup2 -o nsdelegate cgroup2 /sys/fs/cgroup || die "mount cgroup2"
mount -t configfs configfs /sys/kernel/config || die "mount configfs"
mount -t binder binder /dev/binderfs || die "binderfs is unavailable"
# The kernel creates these on mount (CONFIG_ANDROID_BINDER_DEVICES).
for device in binder hwbinder vndbinder; do
  [ -e "/dev/binderfs/${device}" ] || die "binderfs has no ${device} device"
done

# The initrd loaded the only modules this kernel builds (dm-*); nothing may
# load another one for the rest of this boot.
echo 1 > /proc/sys/kernel/modules_disabled || die "cannot lock module loading"
# unprivileged_bpf_disabled starts at 2 (off, but changeable), not 1 (off until
# reboot): the stock ReDroid 14 image's bpfloader sets it to 0, and Android
# reboots when it cannot. The custom AOSP image is to drop that requirement.
sysctl -q -w net.ipv4.ip_forward=1 kernel.kptr_restrict=2 kernel.dmesg_restrict=1 \
  kernel.unprivileged_bpf_disabled=2 kernel.yama.ptrace_scope=2 2>/dev/null || true

# --- Secrets from the provisioner, held in memory only ------------------------
read_secret() {
  local value
  value="$(cat "${secrets}/$1" 2>/dev/null)" || die "secret $1 was not injected"
  [ -n "${value}" ] || die "secret $1 is empty"
  printf '%s' "${value}"
}
node_id="$(read_secret virtroid_node_id)"
node_key="$(read_secret virtroid_node_key)"
region="$(read_secret virtroid_region)"
control_plane="$(read_secret virtroid_control_plane)"
data_key="$(read_secret virtroid_data_key)"
rm -f "${secrets}"/*

# --- This VM's own encrypted data disk -----------------------------------------
[ -b "${data_device}" ] || die "data disk ${data_device} is missing"
if ! cryptsetup isLuks "${data_device}"; then
  log "formatting the data disk"
  printf '%s' "${data_key}" | cryptsetup luksFormat --type luks2 --cipher aes-xts-plain64 \
    --batch-mode --key-file - "${data_device}" || die "cannot format the data disk"
  fresh=1
fi
printf '%s' "${data_key}" | cryptsetup open --key-file - "${data_device}" "${data_mapper}" ||
  die "cannot unlock the data disk"
unset data_key
if [ "${fresh:-0}" = 1 ]; then
  mkfs.ext4 -q -L virtroid-data "/dev/mapper/${data_mapper}" || die "cannot create the data filesystem"
fi
mount -o nosuid,nodev "/dev/mapper/${data_mapper}" /srv/virtroid || die "cannot mount the data disk"
mkdir -p /srv/virtroid/runtimes /srv/virtroid/docker /srv/virtroid/node-tls /srv/virtroid/apks/cache
chmod 0700 /srv/virtroid/node-tls
# The measured apps are copied fresh each boot; the node's download cache
# beside them holds catalog apps it checks against pinned hashes.
cp -f /usr/share/virtroid/apks/* /srv/virtroid/apks/ 2>/dev/null || true

# --- Network identity and firewall ---------------------------------------------
ipv6=""
for _ in $(seq 1 60); do
  ipv6="$(ip -6 -o addr show scope global -tentative 2>/dev/null | awk '{sub("/.*", "", $4); print $4; exit}')"
  [ -n "${ipv6}" ] && break
  sleep 1
done
[ -n "${ipv6}" ] || die "no global IPv6 address"
log "node ${node_id} at [${ipv6}]:${public_port}"

# Inbound: the attestation agent (8443), the node (9443), replies, and the
# runtime bridges. Docker adds its own forwarding and NAT tables.
nft -f - <<EOF || die "cannot load the firewall"
table inet virtroid {
  chain input {
    type filter hook input priority filter; policy drop;
    iif lo accept
    ct state established,related accept
    ct state invalid drop
    meta l4proto { icmp, ipv6-icmp } accept
    iifname { "docker0", "br-*" } accept
    tcp dport { 8443, ${public_port} } accept
  }
}
EOF

# --- Docker, the runtime image, and the node ------------------------------------
# Docker's store is on the data disk, whose encryption does not stop the host
# replaying older blocks. Start it empty, so the image and layers that run are
# always the ones loaded from the measured root below. Runtime data lives in
# bind mounts outside it, and the node recreates its containers.
rm -rf /srv/virtroid/docker
dockerd --data-root /srv/virtroid/docker --exec-root /run/docker --pidfile /run/docker.pid \
  --log-level warn >/var/log/dockerd.log 2>&1 &
dockerd_pid=$!
for _ in $(seq 1 60); do
  docker info >/dev/null 2>&1 && break
  sleep 1
done
docker info >/dev/null 2>&1 || die "Docker did not start"

set -a
# Fixed settings measured into the root (limits, apps), then the runtime image
# this root carries, which overrides anything node.env says about it.
. /etc/virtroid/node.env
. /etc/virtroid/runtime.env
set +a
# The Android image ships inside the measured root; nothing is pulled.
docker load -i /usr/share/virtroid/runtime-image.tar >/dev/null || die "cannot load the runtime image"
docker image inspect "${NODE_RUNTIME_IMAGE}" >/dev/null 2>&1 || die "runtime image does not match NODE_RUNTIME_IMAGE"
docker network create virtroid-guest >/dev/null || die "cannot create the runtime network"

export NODE_ID="${node_id}" NODE_NAME="${node_id}" NODE_PRIVATE_KEY_B64="${node_key}" NODE_REGION="${region}"
export CONTROL_PLANE_URL="${control_plane}" NODE_PUBLIC_CONTROL_PLANE_URL="${control_plane}"
export NODE_ADVERTISE_ADDR="${ipv6}" NODE_PUBLIC_ADDR="[${ipv6}]:${public_port}" NODE_PUBLIC_TLS_BIND="[::]:${public_port}"
export NODE_TLS_DIR=/srv/virtroid/node-tls NODE_RUNTIME_ROOT=/srv/virtroid/runtimes
export NODE_DOCKER_NETWORK=virtroid-guest NODE_RUNTIME_NETWORK_MODE=shared
# Without attestation a phone refuses this VM, so fail loudly instead.
export NODE_SNP_ATTESTATION=required
unset node_key

# Supervise: if the node or Docker exits, so does init, and the VM powers off.
virtnoded &
node_pid=$!
wait -n "${node_pid}" "${dockerd_pid}"
log "virtnoded or dockerd exited; shutting down"
kill "${node_pid}" "${dockerd_pid}" 2>/dev/null
exit 1
