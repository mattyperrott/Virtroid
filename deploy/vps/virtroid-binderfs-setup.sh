#!/usr/bin/env bash
set -euo pipefail

modprobe binder_linux devices=binder,hwbinder,vndbinder

mkdir -p /dev/binderfs

if ! mountpoint -q /dev/binderfs; then
  mount -t binder binder /dev/binderfs
fi

ensure_binder_node() {
  local node="$1"
  if [ -e "/dev/binderfs/${node}" ]; then
    return 0
  fi

  python3 - "$node" <<'PY'
import ctypes
import fcntl
import os
import sys

IOC_NRBITS = 8
IOC_TYPEBITS = 8
IOC_SIZEBITS = 14

IOC_NRSHIFT = 0
IOC_TYPESHIFT = IOC_NRSHIFT + IOC_NRBITS
IOC_SIZESHIFT = IOC_TYPESHIFT + IOC_TYPEBITS
IOC_DIRSHIFT = IOC_SIZESHIFT + IOC_SIZEBITS

IOC_WRITE = 1
IOC_READ = 2

def ioc(direction: int, ioctl_type: int, number: int, size: int) -> int:
    return (
        (direction << IOC_DIRSHIFT)
        | (ioctl_type << IOC_TYPESHIFT)
        | (number << IOC_NRSHIFT)
        | (size << IOC_SIZESHIFT)
    )

class BinderfsDevice(ctypes.Structure):
    _fields_ = [
        ("name", ctypes.c_char * 256),
        ("major", ctypes.c_uint32),
        ("minor", ctypes.c_uint32),
    ]

request = ioc(IOC_READ | IOC_WRITE, ord("b"), 1, ctypes.sizeof(BinderfsDevice))
device = BinderfsDevice()
name = sys.argv[1].encode("ascii")
if len(name) > 255:
    raise SystemExit("binderfs node name too long")
device.name = name

fd = os.open("/dev/binderfs/binder-control", os.O_RDONLY | os.O_CLOEXEC)
try:
    fcntl.ioctl(fd, request, device)
finally:
    os.close(fd)
PY
}

for node in binder hwbinder vndbinder; do
  ensure_binder_node "$node"
done

for node in binder binder-control hwbinder vndbinder; do
  if [ ! -e "/dev/binderfs/${node}" ]; then
    echo "missing binderfs node: ${node}" >&2
    exit 1
  fi
done
