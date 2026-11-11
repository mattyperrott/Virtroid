//go:build linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

// A runtime's network BPF programs. In a user namespace a runtime's root may
// not load BPF programs or attach them to its cgroup, and the VM keeps
// unprivileged BPF locked off. So the node loads them for each runtime: it
// mounts a bpffs for it under bpfRoot, has the image's own loaders fill it in
// a short loading pass outside any user namespace (only the image's files go
// in), copies the pins over with bpfCopy, marks it preloadedMarker, and names
// it in bpffsAnnotation. The wrapper binds it in at /sys/fs/bpf, and once runc
// has made the container's cgroup, attaches netd's cgroup programs to it and
// marks netdAttachedMarker. The runtime's loader and netd then only finish
// (the Connectivity patches), and the runtime never loads a program.

const (
	bpffsAnnotation    = "io.virtroid.bpffs"
	preloadedMarker    = "virtroid_preloaded"
	netdAttachedMarker = "virtroid_netd_attached"
	bpfFsMagic         = 0xcafe4a11
)

var bpfRoot = "/run/virtroid/bpf"

// netd's cgroup programs, pinned under netd_shared, and their attach types
// (packages/modules/Connectivity bpf/netd/BpfHandler.cpp; uapi
// bpf_attach_type values).
var netdPrograms = []struct {
	pin  string
	kind uint32
}{
	{"prog_netd_cgroupskb_egress_stats", 1},          // BPF_CGROUP_INET_EGRESS
	{"prog_netd_cgroupskb_ingress_stats", 0},         // BPF_CGROUP_INET_INGRESS
	{"prog_netd_cgroupsock_inet_create", 2},          // BPF_CGROUP_INET_SOCK_CREATE
	{"prog_netd_cgroupsockrelease_inet_release", 34}, // BPF_CGROUP_INET_SOCK_RELEASE
	{"prog_netd_connect4_inet4_connect", 10},         // BPF_CGROUP_INET4_CONNECT
	{"prog_netd_connect6_inet6_connect", 11},         // BPF_CGROUP_INET6_CONNECT
	{"prog_netd_recvmsg4_udp4_recvmsg", 19},          // BPF_CGROUP_UDP4_RECVMSG
	{"prog_netd_recvmsg6_udp6_recvmsg", 20},          // BPF_CGROUP_UDP6_RECVMSG
	{"prog_netd_sendmsg4_udp4_sendmsg", 14},          // BPF_CGROUP_UDP4_SENDMSG
	{"prog_netd_sendmsg6_udp6_sendmsg", 15},          // BPF_CGROUP_UDP6_SENDMSG
	{"prog_netd_getsockopt_prog", 21},                // BPF_CGROUP_GETSOCKOPT
	{"prog_netd_setsockopt_prog", 22},                // BPF_CGROUP_SETSOCKOPT
	{"prog_netd_bind4_inet4_bind", 8},                // BPF_CGROUP_INET4_BIND
	{"prog_netd_bind6_inet6_bind", 9},                // BPF_CGROUP_INET6_BIND
}

// runtimeBpffs is the bpffs the node named for the container (by name, under
// bpfRoot), or "" if none; it must be one the node has filled
// (preloadedMarker).
func runtimeBpffs(raw map[string]any) (string, error) {
	annotations, _ := raw["annotations"].(map[string]any)
	name, named := annotations[bpffsAnnotation].(string)
	if !named {
		return "", nil
	}
	path, err := runtimeBpffsDir(name)
	if err != nil {
		return "", err
	}
	if !exists(filepath.Join(path, preloadedMarker)) {
		return "", fmt.Errorf("bpffs %s has not been loaded", path)
	}
	return path, nil
}

// runtimeBpffsDir is bpfRoot/<name>, checked to be a bpffs mounted there.
func runtimeBpffsDir(name string) (string, error) {
	if !setNamePattern.MatchString(name) {
		return "", fmt.Errorf("bad bpffs name %q", name)
	}
	path := filepath.Join(bpfRoot, name)
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", fmt.Errorf("bpffs %s: %w", path, err)
	}
	if uint32(st.Type) != bpfFsMagic {
		return "", fmt.Errorf("%s is not a bpffs", path)
	}
	mounted, err := isMountPoint(path)
	if err != nil {
		return "", err
	}
	if !mounted {
		return "", fmt.Errorf("%s is not a mount of its own", path)
	}
	return path, nil
}

// mountRuntimeBpffs mounts a fresh bpffs at bpfRoot/<name>, replacing any
// left there (a runtime's earlier start).
func mountRuntimeBpffs(name string) error {
	if !setNamePattern.MatchString(name) {
		return fmt.Errorf("bad bpffs name %q", name)
	}
	if err := os.MkdirAll(bpfRoot, 0o711); err != nil {
		return err
	}
	if err := releaseRuntimeBpffs(name); err != nil {
		return err
	}
	path := filepath.Join(bpfRoot, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	return unix.Mount("bpf", path, "bpf", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "")
}

// releaseRuntimeBpffs unmounts and removes bpfRoot/<name>, if there. Its
// objects go when nothing holds them any more.
func releaseRuntimeBpffs(name string) error {
	if !setNamePattern.MatchString(name) {
		return fmt.Errorf("bad bpffs name %q", name)
	}
	path := filepath.Join(bpfRoot, name)
	for {
		mounted, err := isMountPoint(path)
		if err != nil {
			return err
		}
		if !mounted {
			break
		}
		if err := unix.Unmount(path, unix.MNT_DETACH); err != nil {
			return fmt.Errorf("unmount %s: %w", path, err)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// bindBpffs binds the runtime's bpffs at /sys/fs/bpf, in place of any other
// mount there.
func bindBpffs(raw map[string]any, path string) {
	mounts, _ := raw["mounts"].([]any)
	kept := mounts[:0]
	for _, m := range mounts {
		if mm, _ := m.(map[string]any); mm != nil && mm["destination"] == "/sys/fs/bpf" {
			continue
		}
		kept = append(kept, m)
	}
	raw["mounts"] = append(kept, map[string]any{
		"destination": "/sys/fs/bpf",
		"type":        "bind",
		"source":      path,
		"options":     toAny([]string{"bind", "nosuid", "nodev", "noexec"}),
	})
}

type bpfObjAttr struct {
	pathname  uint64
	bpfFd     uint32
	fileFlags uint32
}

type bpfAttachAttr struct {
	targetFd     uint32
	attachBpfFd  uint32
	attachType   uint32
	attachFlags  uint32
	replaceBpfFd uint32
}

func bpfObjGet(path string) (int, error) {
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return -1, err
	}
	attr := bpfObjAttr{pathname: uint64(uintptr(unsafe.Pointer(p)))}
	fd, _, errno := unix.Syscall(unix.SYS_BPF, unix.BPF_OBJ_GET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr))
	if errno != 0 {
		return -1, &os.PathError{Op: "bpf obj_get", Path: path, Err: errno}
	}
	return int(fd), nil
}

func bpfObjPin(fd int, path string) error {
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return err
	}
	attr := bpfObjAttr{pathname: uint64(uintptr(unsafe.Pointer(p))), bpfFd: uint32(fd)}
	if _, _, errno := unix.Syscall(unix.SYS_BPF, unix.BPF_OBJ_PIN, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr)); errno != 0 {
		return &os.PathError{Op: "bpf obj_pin", Path: path, Err: errno}
	}
	return nil
}

// attachNetdPrograms attaches netd's cgroup programs, pinned in bpffs, to the
// container's cgroup as netd would, then marks netdAttachedMarker. All or
// nothing: the runtime can't attach a missing one itself.
func attachNetdPrograms(cgroupDir, bpffs string) error {
	cg, err := unix.Open(cgroupDir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: cgroupDir, Err: err}
	}
	defer unix.Close(cg)
	for _, p := range netdPrograms {
		fd, err := bpfObjGet(filepath.Join(bpffs, "netd_shared", p.pin))
		if err != nil {
			return err
		}
		attr := bpfAttachAttr{targetFd: uint32(cg), attachBpfFd: uint32(fd), attachType: p.kind}
		_, _, errno := unix.Syscall(unix.SYS_BPF, unix.BPF_PROG_ATTACH, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr))
		unix.Close(fd)
		if errno != 0 {
			return fmt.Errorf("attach %s to %s: %w", p.pin, cgroupDir, errno)
		}
	}
	if err := os.Mkdir(filepath.Join(bpffs, netdAttachedMarker), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// bpfCopy pins in dst (a bpffs) every object pinned under src (the loading
// pass's /sys/fs/bpf), at the same paths, with the same modes and with owners
// moved up by offset into the runtime's id range. The objects are shared, not
// duplicated: a pin is a reference.
func bpfCopy(src, dst string, offset uint32) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			return err
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if rel != "." {
				if err := os.Mkdir(target, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
					return err
				}
			}
		case unix.S_IFREG:
			fd, err := bpfObjGet(path)
			if err != nil {
				return err
			}
			err = bpfObjPin(fd, target)
			unix.Close(fd)
			if err != nil {
				return err
			}
			n++
		case unix.S_IFLNK:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: not a pin, directory or symlink", path)
		}
		if err := unix.Lchown(target, int(st.Uid+offset), int(st.Gid+offset)); err != nil {
			return fmt.Errorf("chown %s: %w", target, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFLNK {
			if err := unix.Chmod(target, st.Mode&0o7777); err != nil {
				return fmt.Errorf("chmod %s: %w", target, err)
			}
		}
		return nil
	})
	return n, err
}

// markPreloaded marks a runtime's bpffs as filled, for runtimeBpffs and the
// runtime's loader.
func markPreloaded(dst string) error {
	if err := os.Mkdir(filepath.Join(dst, preloadedMarker), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}
