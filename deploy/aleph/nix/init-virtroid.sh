#!/bin/busybox sh
# /init of the Virtroid runtime VM's measured initrd. Adapted from aleph-vm's
# nix/init-instance.sh (MIT licence, Copyright (c) 2021-2024 ALEPH.IM SAS).
#
# Where Aleph's instance image unlocks an owner-supplied encrypted root, this
# one verifies a public, read-only root against the dm-verity root hash baked
# into this initrd. The initrd is part of the SEV-SNP launch measurement, so
# the measurement covers every byte of the root: a host or a compromised
# control plane cannot hand the VM a different disk.
#
# Per-VM secrets (node identity, data-disk key) arrive afterwards through the
# attestation agent in owner-auth mode: only the provisioner key measured on
# the cmdline (owner=) can inject them, and only after verifying this VM.

# shellcheck disable=SC1091  # /bin/init-common.sh only exists inside the initrd
. /bin/init-common.sh

ROOTHASH=@ROOTHASH@
HASH_OFFSET=@HASH_OFFSET@

fatal() {
    echo "init: FATAL: $*"
    exec /bin/busybox poweroff -f
}

owner=$(/bin/busybox sed -n 's/.*\bowner=\(0x[0-9a-fA-F]*\).*/\1/p' /proc/cmdline)
[ -n "$owner" ] || fatal "booted without owner=0x... on the cmdline"

for module in dm-mod dm-bufio dm-verity dm-crypt; do
    /bin/busybox insmod "/lib/modules/${module}.ko" 2>&1 || fatal "cannot load ${module}"
done
/bin/busybox mkdir -p /dev/mapper /run/cryptsetup
/bin/busybox mknod /dev/mapper/control c 10 236 2>/dev/null

wait_for_rootfs_blkdev
[ -n "$blkdev" ] || fatal "no root disk"

# The hash tree sits on the same disk, after the filesystem.
/bin/veritysetup open "$blkdev" virtroid-root "$blkdev" "$ROOTHASH" --hash-offset="$HASH_OFFSET" 2>&1 ||
    fatal "root disk does not match the measured root hash"
/bin/busybox mkdir -p /mnt/root
/bin/busybox mount -t ext4 -o ro,nodev /dev/mapper/virtroid-root /mnt/root || fatal "cannot mount the root"
echo "init: root verified (${ROOTHASH})"

/bin/busybox mkdir -m 0700 -p /tmp/secrets
echo "init: starting attestation agent (owner-auth mode, owner=${owner})"
run_attest_agent --owner "$owner"

# The provisioner injects the VM's secrets after attesting it. Wait for all of
# them: the agent writes each atomically, the data key last in sorted order
# is not guaranteed, so check every file.
echo "init: waiting for the provisioner's secrets"
waited=0
while true; do
    ready=1
    for name in virtroid_node_id virtroid_node_key virtroid_region virtroid_control_plane virtroid_data_key; do
        [ -s "/tmp/secrets/${name}" ] || ready=0
    done
    [ "$ready" = 1 ] && break
    /bin/busybox sleep 1
    waited=$((waited + 1))
    [ $((waited % 60)) -eq 0 ] && echo "init: still waiting for secrets (${waited}s)"
done

prepare_chroot /mnt/root
[ -x /mnt/root/sbin/init ] || fatal "no /sbin/init in the root"
echo "init: starting /sbin/init"
/bin/busybox chroot /mnt/root /sbin/init &
guest_pid=$!
wait "$guest_pid"
echo "init: /sbin/init exited; powering off"
exec /bin/busybox poweroff -f
