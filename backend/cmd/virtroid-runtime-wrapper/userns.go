//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The node marks a runtime that runs in a user namespace (usernsAnnotation);
// Docker itself knows nothing of it. The wrapper puts it in one: its root
// becomes the first id of virtroid-runtime's range in /etc/subuid and
// /etc/subgid, and its rootfs an overlay of id-mapped clones of the layers
// Docker's was made of (rootfs.go), so the image's files keep their own
// owners on disk (shared with containers outside a user namespace) and read as
// the same owners inside.

const (
	usernsAnnotation = "io.virtroid.userns"
	usernsUser       = "virtroid-runtime"
)

var (
	subuidPath = "/etc/subuid"
	subgidPath = "/etc/subgid"
)

func wantsUserns(raw map[string]any) bool {
	annotations, _ := raw["annotations"].(map[string]any)
	return annotations[usernsAnnotation] == "1"
}

// usernsRange is usernsUser's range in a subuid or subgid file.
func usernsRange(path string) (idMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return idMap{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(strings.TrimSpace(scanner.Text()), ":")
		if len(fields) != 3 || fields[0] != usernsUser {
			continue
		}
		start, err1 := strconv.ParseUint(fields[1], 10, 32)
		count, err2 := strconv.ParseUint(fields[2], 10, 32)
		if err1 != nil || err2 != nil || count == 0 || start == 0 || start+count > 1<<32 {
			return idMap{}, fmt.Errorf("%s: bad range for %s: %q", path, usernsUser, scanner.Text())
		}
		return idMap{ContainerID: 0, HostID: uint32(start), Size: uint32(count)}, nil
	}
	if err := scanner.Err(); err != nil {
		return idMap{}, err
	}
	return idMap{}, fmt.Errorf("%s has no range for %s", path, usernsUser)
}

// addUserns puts the spec's process in a user namespace with these
// mappings. A spec that already has a user namespace is refused: the node
// asked for one, so Docker must not have set up another.
func addUserns(raw map[string]any, uidMap, gidMap idMap) error {
	linux, _ := raw["linux"].(map[string]any)
	if linux == nil {
		linux = map[string]any{}
		raw["linux"] = linux
	}
	namespaces, _ := linux["namespaces"].([]any)
	for _, ns := range namespaces {
		if m, _ := ns.(map[string]any); m != nil && m["type"] == "user" {
			return errors.New("the spec already has a user namespace")
		}
	}
	if linux["uidMappings"] != nil || linux["gidMappings"] != nil {
		return errors.New("the spec already has id mappings")
	}
	linux["namespaces"] = append(namespaces, map[string]any{"type": "user"})
	mapping := func(m idMap) []any {
		return []any{map[string]any{"containerID": m.ContainerID, "hostID": m.HostID, "size": m.Size}}
	}
	linux["uidMappings"] = mapping(uidMap)
	linux["gidMappings"] = mapping(gidMap)
	return nil
}

// holdUserns is the wrapper's "userns-hold" verb: a child in a new user
// namespace that waits for its stdin to close, while its parent takes a
// handle on the namespace.
func holdUserns() {
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// usernsFd returns a handle on a user namespace with these mappings, made by
// a short-lived child.
func usernsFd(uidMap, gidMap idMap) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return -1, err
	}
	cmd := exec.Command(self, "userns-hold")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: int(uidMap.HostID), Size: int(uidMap.Size)}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: int(gidMap.HostID), Size: int(gidMap.Size)}},
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start a user namespace: %w", err)
	}
	defer func() {
		stdin.Close()
		cmd.Wait()
	}()
	fd, err := unix.Open(fmt.Sprintf("/proc/%d/ns/user", cmd.Process.Pid), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open the user namespace: %w", err)
	}
	return fd, nil
}

// fitSysctls fits Docker's sysctls to the namespace. Docker lets every group
// ping (net.ipv4.ping_group_range "0 2147483647"), but the kernel reads the
// range's ids in the writer's user namespace and refuses one it doesn't map,
// which failed the create. Every group the namespace has is the mapping's
// range, so the range is cut to it ("1 0", the kernel's own default, lets no
// group ping).
func fitSysctls(raw map[string]any, gidMap idMap) error {
	linux, _ := raw["linux"].(map[string]any)
	sysctl, _ := linux["sysctl"].(map[string]any)
	const key = "net.ipv4.ping_group_range"
	value, ok := sysctl[key].(string)
	if !ok {
		return nil
	}
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return fmt.Errorf("sysctl %s: %q", key, value)
	}
	lo, err1 := strconv.ParseUint(fields[0], 10, 32)
	hi, err2 := strconv.ParseUint(fields[1], 10, 32)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("sysctl %s: %q", key, value)
	}
	first, last := uint64(gidMap.ContainerID), uint64(gidMap.ContainerID)+uint64(gidMap.Size)-1
	if lo > hi || hi < first || lo > last {
		sysctl[key] = "1 0"
		return nil
	}
	sysctl[key] = fmt.Sprintf("%d %d", max(lo, first), min(hi, last))
	return nil
}
