//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// In a user namespace the container's root is an unprivileged uid on the VM:
// it can open only the VM's world-accessible device nodes (of a runtime's, only
// /dev/ashmem is), and the kernel won't let it make device nodes of its own.
// Opening the VM's shared nodes up would open them to every process on the VM,
// so instead each runtime gets nodes of its own, made here on the host, owned
// by the container's mapped root, and bound in at the paths the spec asks for.
// The device cgroup still decides which devices the container may open.

// devicesRoot holds every runtime's nodes, on a tmpfs of its own that allows
// device nodes (the VM's /run may be mounted nodev) but not set-id programs.
var devicesRoot = "/run/virtroid/devices"

type ociDevice struct {
	Path     string  `json:"path"`
	Type     string  `json:"type"`
	Major    int64   `json:"major"`
	Minor    int64   `json:"minor"`
	FileMode *uint32 `json:"fileMode,omitempty"`
	UID      *uint32 `json:"uid,omitempty"`
	GID      *uint32 `json:"gid,omitempty"`
}

// deviceDir is the directory for one container's nodes.
func deviceDir(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\x00") || id == "." || id == ".." {
		return "", fmt.Errorf("bad container id %q", id)
	}
	return filepath.Join(devicesRoot, id), nil
}

// ensureDevicesRoot mounts devicesRoot's tmpfs unless it is mounted already.
func ensureDevicesRoot() error {
	if err := os.MkdirAll(devicesRoot, 0o711); err != nil {
		return err
	}
	mounted, err := isMountPoint(devicesRoot)
	if err != nil || mounted {
		return err
	}
	return unix.Mount("tmpfs", devicesRoot, "tmpfs", unix.MS_NOSUID|unix.MS_NOEXEC, "mode=0711,size=1m")
}

// privateDevices replaces the spec's device entries (which runc would bind
// from the VM's /dev) with binds of nodes made in dir for this container,
// owned by the host ids the entry's uid and gid (default root) map to.
func privateDevices(raw map[string]any, dir string, uidMaps, gidMaps []idMap) error {
	linux, _ := raw["linux"].(map[string]any)
	if linux == nil {
		return nil
	}
	entries, _ := linux["devices"].([]any)
	if len(entries) == 0 {
		return nil
	}
	if err := ensureDevicesRoot(); err != nil {
		return fmt.Errorf("devices tmpfs: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return err
	}
	mounts, _ := raw["mounts"].([]any)
	for _, entry := range entries {
		device, err := decodeDevice(entry)
		if err != nil {
			return err
		}
		node, err := makeNode(dir, device, uidMaps, gidMaps)
		if err != nil {
			return fmt.Errorf("device %s: %w", device.Path, err)
		}
		mounts = append(mounts, map[string]any{
			"destination": device.Path,
			"type":        "bind",
			"source":      node,
			"options":     []any{"bind", "nosuid", "noexec"},
		})
	}
	raw["mounts"] = mounts
	linux["devices"] = []any{}
	return nil
}

func decodeDevice(entry any) (ociDevice, error) {
	m, _ := entry.(map[string]any)
	if m == nil {
		return ociDevice{}, errors.New("device entry is not an object")
	}
	var d ociDevice
	d.Path, _ = m["path"].(string)
	d.Type, _ = m["type"].(string)
	// Numbers are json.Number (decodeSpec). Each must fit its kernel type;
	// out of range is an error, not a silent truncation.
	number := func(key string) (uint32, bool, error) {
		v, present := m[key]
		if !present {
			return 0, false, nil
		}
		n, ok := v.(json.Number)
		if !ok {
			return 0, false, fmt.Errorf("device %q: %s is not a number", d.Path, key)
		}
		x, err := strconv.ParseUint(n.String(), 10, 32)
		if err != nil {
			return 0, false, fmt.Errorf("device %q: %s %s: %w", d.Path, key, n, err)
		}
		return uint32(x), true, nil
	}
	major, okMajor, err := number("major")
	if err != nil {
		return d, err
	}
	minor, okMinor, err := number("minor")
	if err != nil {
		return d, err
	}
	if !okMajor || !okMinor {
		return d, fmt.Errorf("device %q: no major/minor", d.Path)
	}
	d.Major, d.Minor = int64(major), int64(minor)
	for key, field := range map[string]**uint32{"fileMode": &d.FileMode, "uid": &d.UID, "gid": &d.GID} {
		x, ok, err := number(key)
		if err != nil {
			return d, err
		}
		if ok {
			*field = &x
		}
	}
	if !filepath.IsAbs(d.Path) || filepath.Clean(d.Path) != d.Path || !strings.HasPrefix(d.Path, "/dev/") {
		return d, fmt.Errorf("device path %q is not under /dev", d.Path)
	}
	if strings.Contains(d.Path, "!") {
		return d, fmt.Errorf("device path %q has a '!'", d.Path)
	}
	return d, nil
}

// makeNode makes device's node in dir and returns its path, named by
// flatName. dir starts empty, so two entries for one path fail here (EEXIST)
// rather than one silently replacing the other.
func makeNode(dir string, device ociDevice, uidMaps, gidMaps []idMap) (string, error) {
	var kind uint32
	switch device.Type {
	case "c", "u":
		kind = unix.S_IFCHR
	case "b":
		kind = unix.S_IFBLK
	case "p":
		kind = unix.S_IFIFO
	default:
		return "", fmt.Errorf("unsupported device type %q", device.Type)
	}
	mode := uint32(0o666)
	if device.FileMode != nil {
		mode = *device.FileMode & 0o7777
	}
	var uid, gid uint32
	if device.UID != nil {
		uid = *device.UID
	}
	if device.GID != nil {
		gid = *device.GID
	}
	hostUID, err := mapID(uidMaps, uid)
	if err != nil {
		return "", err
	}
	hostGID, err := mapID(gidMaps, gid)
	if err != nil {
		return "", err
	}
	node := filepath.Join(dir, flatName(device.Path))
	dev := int(unix.Mkdev(uint32(device.Major), uint32(device.Minor)))
	if err := unix.Mknod(node, kind|mode, dev); err != nil {
		return "", err
	}
	// mknod applies the umask; set the mode the spec asks for.
	if err := unix.Chmod(node, mode); err != nil {
		return "", err
	}
	if err := unix.Lchown(node, int(hostUID), int(hostGID)); err != nil {
		return "", err
	}
	return node, nil
}

// flatName turns /dev/net/tun into "net!tun", keeping every node directly in
// dir. decodeDevice refuses paths with "!", so distinct paths get distinct
// names.
func flatName(path string) string {
	return strings.ReplaceAll(strings.TrimPrefix(path, "/dev/"), "/", "!")
}

// removeDevices drops a deleted container's nodes.
func removeDevices(id string) error {
	dir, err := deviceDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
