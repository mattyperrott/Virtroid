//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// The rootfs of a container in a user namespace. Docker mounts it as an
// overlay of the image's layers (containerd's snapshots) under a writable
// layer of the container's own. Inside the namespace the image's files must
// read as their own owners, so as base+id on the VM, while on disk they keep
// their own ids: the image's layers are shared with every container of the
// image (the BPF loading pass's, outside any user namespace, among them), and
// the writable layer is what Docker shows, copies and commits.
//
// An overlay mount can't be id-mapped, but its layers can. So idmapRootfs
// replaces Docker's overlay at the rootfs path with an overlay of the same
// layers, each an id-mapped clone that is never attached anywhere (the
// overlay is given them as file descriptors). Only one overlay is ever
// mounted on the writable layer: Docker's goes before the new one is made, as
// two overlays sharing an upper layer is undefined and making one resets the
// work directory under the other. Docker sees the new overlay at the path it
// mounted its own and unmounts it when the container is done, as it would its
// own. Through it the VM's root owns nothing: a write there as the VM's root
// (a docker cp into the rootfs) fails instead of landing as an owner outside
// the container's range.
//
// runc gets a bind of the overlay at idmappedRootDir/<id> as the rootfs: the
// container's root must reach the rootfs by its path, and Docker's
// directories on the way to its own are the VM root's alone. The bind holds
// the overlay as long as it is mounted, so it goes when the container does.

var (
	mountinfoPath = "/proc/self/mountinfo"
	// idmappedRootDir holds, per container, the bind of its overlay that runc
	// gets as its rootfs (<id>) and the record of both mounts (<id>.json).
	idmappedRootDir = "/run/virtroid/rootfs"
)

// SECBIT_NO_SETUID_FIXUP (linux/securebits.h).
const secbitNoSetuidFixup = 1 << 2

type mountinfoEntry struct {
	id         uint64
	root       string
	mountPoint string
	mountOpts  string
	fsType     string
	superOpts  string
}

// parseMountinfo reads /proc/<pid>/mountinfo (proc_pid_mountinfo(5)). Lines
// it can't read are left out; the caller looks up the one it needs by id.
func parseMountinfo(data []byte) []mountinfoEntry {
	var entries []mountinfoEntry
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, " ")
		// id parent major:minor root mount-point options [optional...] - type source super-options
		sep := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || len(fields) < sep+4 {
			continue
		}
		id, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		entries = append(entries, mountinfoEntry{
			id:         id,
			root:       unescapeOctal(fields[3]),
			mountPoint: unescapeOctal(fields[4]),
			mountOpts:  fields[5],
			fsType:     fields[sep+1],
			superOpts:  fields[sep+3],
		})
	}
	return entries
}

// unescapeOctal undoes the kernel's \ooo escapes in mountinfo.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	octal := func(c byte) bool { return c >= '0' && c <= '7' }
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && octal(s[i+1]) && octal(s[i+2]) && octal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// overlayLayers is an overlay mount as mountinfo shows it.
type overlayLayers struct {
	lowers      []string // top first, as overlayfs lists them
	upper, work string
	options     []string // its other options: "key" or "key=value"
	attrs       int      // the mount's MOUNT_ATTR_* flags
}

// parseOverlay reads an overlay's layers and options from its mountinfo line.
// Only a writable overlay of absolute layer paths is taken: Docker's rootfs
// is one, and a path the kernel shows otherwise (a layer given as a file
// descriptor, which it shows by its path inside a detached mount) can't be
// opened again.
func parseOverlay(e mountinfoEntry) (overlayLayers, error) {
	var l overlayLayers
	if e.fsType != "overlay" {
		return l, fmt.Errorf("%s is %s, not an overlay", e.mountPoint, e.fsType)
	}
	if e.root != "/" {
		return l, fmt.Errorf("%s is a bind of %s inside an overlay, not an overlay", e.mountPoint, e.root)
	}
	for _, opt := range strings.Split(e.superOpts, ",") {
		key, value, _ := strings.Cut(opt, "=")
		switch key {
		case "rw":
		case "ro":
			return l, fmt.Errorf("the overlay at %s is read-only", e.mountPoint)
		case "lowerdir":
			if strings.Contains(value, "::") {
				return l, fmt.Errorf("the overlay at %s has data-only layers", e.mountPoint)
			}
			// Each layer is escaped on its own (a ':' in it as \072).
			for _, p := range strings.Split(value, ":") {
				l.lowers = append(l.lowers, unescapeOctal(p))
			}
		case "upperdir":
			l.upper = unescapeOctal(value)
		case "workdir":
			l.work = unescapeOctal(value)
		default:
			l.options = append(l.options, unescapeOctal(opt))
		}
	}
	if len(l.lowers) == 0 || l.upper == "" || l.work == "" {
		return l, fmt.Errorf("the overlay at %s has no writable layer", e.mountPoint)
	}
	for _, p := range append(append([]string(nil), l.lowers...), l.upper, l.work) {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return l, fmt.Errorf("the overlay at %s has a layer at %q", e.mountPoint, p)
		}
	}
	// The upper and work directories must be on one mount: one id-mapped
	// clone of their parent holds both.
	if filepath.Dir(l.upper) != filepath.Dir(l.work) || filepath.Dir(l.upper) == "/" {
		return l, fmt.Errorf("the overlay at %s keeps its upper (%s) and work (%s) directories apart", e.mountPoint, l.upper, l.work)
	}
	attrs, err := mountAttrs(e.mountOpts)
	if err != nil {
		return l, fmt.Errorf("the overlay at %s: %w", e.mountPoint, err)
	}
	l.attrs = attrs
	return l, nil
}

// mountAttrs turns a mount's per-mount options in mountinfo into MOUNT_ATTR_*
// flags. Anything else (an id-mapped mount) is refused.
func mountAttrs(opts string) (int, error) {
	attrs := unix.MOUNT_ATTR_STRICTATIME // shown as neither relatime nor noatime
	for _, o := range strings.Split(opts, ",") {
		switch o {
		case "rw":
		case "ro":
			attrs |= unix.MOUNT_ATTR_RDONLY
		case "nosuid":
			attrs |= unix.MOUNT_ATTR_NOSUID
		case "nodev":
			attrs |= unix.MOUNT_ATTR_NODEV
		case "noexec":
			attrs |= unix.MOUNT_ATTR_NOEXEC
		case "nodiratime":
			attrs |= unix.MOUNT_ATTR_NODIRATIME
		case "nosymfollow":
			attrs |= unix.MOUNT_ATTR_NOSYMFOLLOW
		case "relatime":
			attrs = attrs&^unix.MOUNT_ATTR__ATIME | unix.MOUNT_ATTR_RELATIME
		case "noatime":
			attrs = attrs&^unix.MOUNT_ATTR__ATIME | unix.MOUNT_ATTR_NOATIME
		default:
			return 0, fmt.Errorf("mount option %q", o)
		}
	}
	return attrs, nil
}

// mountIDAt is the id of the mount at path and whether path is its root. With
// STATX_MNT_ID it is the id mountinfo lists; STATX_MNT_ID_UNIQUE is never
// reused.
func mountIDAt(path string, mask int) (uint64, bool, error) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT, mask, &st); err != nil {
		return 0, false, &os.PathError{Op: "statx", Path: path, Err: err}
	}
	if st.Mask&uint32(mask) == 0 || st.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT == 0 {
		return 0, false, fmt.Errorf("statx %s: the kernel gave no mount id", path)
	}
	return st.Mnt_id, st.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0, nil
}

// overlayAt is the overlay mounted at path (a resolved path), as mountinfo
// shows it.
func overlayAt(path string) (overlayLayers, error) {
	id, root, err := mountIDAt(path, unix.STATX_MNT_ID)
	if err != nil {
		return overlayLayers{}, err
	}
	if !root {
		return overlayLayers{}, fmt.Errorf("%s is not a mount", path)
	}
	data, err := os.ReadFile(mountinfoPath)
	if err != nil {
		return overlayLayers{}, err
	}
	for _, e := range parseMountinfo(data) {
		if e.id != id {
			continue
		}
		if e.mountPoint != path {
			return overlayLayers{}, fmt.Errorf("mount %d is at %s, not %s", id, e.mountPoint, path)
		}
		return parseOverlay(e)
	}
	return overlayLayers{}, fmt.Errorf("the mount at %s (%d) is not in %s", path, id, mountinfoPath)
}

// rootfsRecord is what idmapRootfs mounted for a container. Mount ids are
// STATX_MNT_ID_UNIQUE.
type rootfsRecord struct {
	Path        string `json:"path"`     // Docker's rootfs, where the overlay is
	MountID     uint64 `json:"mount_id"` // the overlay's mount there
	BindMountID uint64 `json:"bind_mount_id"`
}

func validContainerID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\x00")
}

// rootfsPaths are the container's bind of its overlay and its record.
func rootfsPaths(id string) (bind, record string, err error) {
	if !validContainerID(id) {
		return "", "", fmt.Errorf("bad container id %q", id)
	}
	return filepath.Join(idmappedRootDir, id), filepath.Join(idmappedRootDir, id+".json"), nil
}

func readRootfsRecord(id string) (rootfsRecord, error) {
	var rec rootfsRecord
	_, path, err := rootfsPaths(id)
	if err != nil {
		return rec, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("%s: %w", path, err)
	}
	return rec, nil
}

func writeRootfsRecord(id string, rec rootfsRecord) error {
	_, path, err := rootfsPaths(id)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(rec)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// idmapRootfs swaps Docker's overlay at rootfs for one of the same layers,
// each id-mapped with these mappings (on-disk id k reads as base+k), binds it
// at idmappedRootDir/<id> and records both. It returns the bind, runc's
// rootfs. What it already made for this container is kept: the bind, and the
// overlay itself when Docker kept it mounted from the container's last run.
func idmapRootfs(rootfs, id string, uidMap, gidMap idMap) (string, error) {
	bind, _, err := rootfsPaths(id)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(rootfs)
	if err != nil {
		return "", err
	}
	current, root, err := mountIDAt(path, unix.STATX_MNT_ID_UNIQUE)
	if err != nil {
		return "", err
	}
	if rec, err := readRootfsRecord(id); err == nil && root && rec.Path == path && rec.MountID == current {
		if b, broot, err := mountIDAt(bind, unix.STATX_MNT_ID_UNIQUE); err == nil && broot && b == rec.BindMountID {
			return bind, nil
		}
		// Our overlay, still at Docker's path after a delete: Docker doesn't
		// always unmount a container's rootfs when it stops (it counts its
		// own uses, and a failed copy into the container leaves one
		// counted). It is this container's, on its own layers, so it is
		// bound again rather than built anew.
		if err := ensureSearchable(idmappedRootDir); err != nil {
			return "", err
		}
		if err := bindOverlay(id, path, rec); err != nil {
			return "", err
		}
		return bind, nil
	}
	// A bind left from an earlier create would keep that create's overlay
	// alive on the same writable layer: it goes first.
	if err := forgetRootfs(id); err != nil {
		return "", err
	}
	if err := ensureSearchable(idmappedRootDir); err != nil {
		return "", err
	}
	layers, err := overlayAt(path)
	if err != nil {
		return "", fmt.Errorf("Docker's rootfs: %w", err)
	}
	ns, err := usernsFd(uidMap, gidMap)
	if err != nil {
		return "", err
	}
	defer unix.Close(ns)
	mnt, err := buildIdmappedOverlay(path, layers, ns, uidMap.HostID, gidMap.HostID)
	if err != nil {
		return "", err
	}
	defer unix.Close(mnt)
	if err := unix.MoveMount(mnt, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return "", fmt.Errorf("mount the id-mapped overlay at %s: %w", path, err)
	}
	rec := rootfsRecord{Path: path}
	if rec.MountID, _, err = mountIDAt(path, unix.STATX_MNT_ID_UNIQUE); err == nil {
		err = bindOverlay(id, path, rec)
	}
	if err != nil {
		forgetRootfs(id)
		unix.Unmount(path, unix.MNT_DETACH)
		return "", err
	}
	return bind, nil
}

// bindOverlay binds the overlay at path (rec.MountID) at the container's
// bind, replacing any other bind there, and records both.
func bindOverlay(id, path string, rec rootfsRecord) error {
	bind, _, err := rootfsPaths(id)
	if err != nil {
		return err
	}
	if err := unmountAll(bind); err != nil {
		return err
	}
	if err := os.Mkdir(bind, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	clone, err := unix.OpenTree(unix.AT_FDCWD, path, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		return fmt.Errorf("bind the overlay: %w", err)
	}
	defer unix.Close(clone)
	if err := unix.MoveMount(clone, "", unix.AT_FDCWD, bind, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("bind the overlay at %s: %w", bind, err)
	}
	// The clone is of whatever is mounted at path now: it must still be ours.
	if got, _, err := mountIDAt(path, unix.STATX_MNT_ID_UNIQUE); err != nil || got != rec.MountID {
		unmountAll(bind)
		return fmt.Errorf("the overlay at %s changed while it was bound", path)
	}
	if rec.BindMountID, _, err = mountIDAt(bind, unix.STATX_MNT_ID_UNIQUE); err != nil {
		return err
	}
	return writeRootfsRecord(id, rec)
}

// ensureSearchable makes dir (mode 0711) and checks that everyone may pass
// through it and every directory above: the container's root is no owner of
// any.
func ensureSearchable(dir string) error {
	if err := os.MkdirAll(dir, 0o711); err != nil {
		return err
	}
	for d := dir; ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err != nil {
			return err
		}
		if fi.Mode().Perm()&0o001 == 0 {
			return fmt.Errorf("%s (mode %o) is closed to the runtime's root on the way to its rootfs", d, fi.Mode().Perm())
		}
		if d == "/" || d == "." {
			return nil
		}
	}
}

// buildIdmappedOverlay makes the overlay, detached, and unmounts Docker's at
// path just before. Its thread's credentials are changed for it, so it runs
// on a thread of its own that ends with it.
func buildIdmappedOverlay(path string, l overlayLayers, ns int, uid, gid uint32) (int, error) {
	type result struct {
		fd  int
		err error
	}
	done := make(chan result, 1)
	go func() {
		// Never unlocked: the runtime ends the thread with the goroutine
		// rather than reuse its changed credentials.
		runtime.LockOSThread()
		fd, err := makeIdmappedOverlay(path, l, ns, uid, gid)
		done <- result{fd, err}
	}()
	r := <-done
	return r.fd, r.err
}

func makeIdmappedOverlay(path string, l overlayLayers, ns int, uid, gid uint32) (int, error) {
	var fds []int
	defer func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}()

	// The overlay does its own work in the layers (whiteouts, its work
	// directory, copy-ups) with the credentials it is made with. With the
	// fs ids of the container's root as the VM sees it (base), what it
	// writes there is owned by 0 on disk, as under Docker's overlay, and the
	// VM's root stays without an owner in the layers. It keeps this
	// process's capabilities, which it needs (reading and writing its
	// trusted.overlay.* attributes among them): changing the fs ids away from
	// 0 drops the file capabilities unless SECBIT_NO_SETUID_FIXUP is set
	// (capabilities(7), "Effect of user ID changes on capabilities").
	bits, err := unix.PrctlRetInt(unix.PR_GET_SECUREBITS, 0, 0, 0, 0)
	if err != nil {
		return -1, fmt.Errorf("read the securebits: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_SECUREBITS, uintptr(bits|secbitNoSetuidFixup), 0, 0, 0); err != nil {
		return -1, fmt.Errorf("keep capabilities across the fs id change: %w", err)
	}
	unix.SetfsgidRetGid(int(gid))
	unix.SetfsuidRetUid(int(uid))
	// setfsuid(2) and setfsgid(2) only report the id they replaced: asking
	// to set an invalid one returns the current one.
	if cur, _ := unix.SetfsuidRetUid(-1); cur != int(uid) {
		return -1, fmt.Errorf("fs uid is %d, not %d", cur, uid)
	}
	if cur, _ := unix.SetfsgidRetGid(-1); cur != int(gid) {
		return -1, fmt.Errorf("fs gid is %d, not %d", cur, gid)
	}

	fsfd, err := unix.Fsopen("overlay", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("fsopen overlay: %w", err)
	}
	fds = append(fds, fsfd)
	setFd := func(key string, fd int, what string) error {
		if err := unix.FsconfigSetFd(fsfd, key, fd); err != nil {
			return fsError(fsfd, key+" "+what, err)
		}
		return nil
	}
	clone := func(dir string) (int, error) {
		fd, err := unix.OpenTree(unix.AT_FDCWD, dir, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
		if err != nil {
			return -1, fmt.Errorf("clone the layer %s: %w", dir, err)
		}
		fds = append(fds, fd)
		attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_IDMAP, Userns_fd: uint64(ns)}
		if err := unix.MountSetattr(fd, "", unix.AT_EMPTY_PATH, &attr); err != nil {
			return -1, fmt.Errorf("id-map the layer %s: %w", dir, err)
		}
		return fd, nil
	}
	for _, dir := range l.lowers {
		fd, err := clone(dir)
		if err != nil {
			return -1, err
		}
		if err := setFd("lowerdir+", fd, dir); err != nil {
			return -1, err
		}
	}
	top, err := clone(filepath.Dir(l.upper))
	if err != nil {
		return -1, err
	}
	for _, d := range []struct{ key, dir string }{{"upperdir", l.upper}, {"workdir", l.work}} {
		fd, err := unix.Openat(top, filepath.Base(d.dir), unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, fmt.Errorf("open %s: %w", d.dir, err)
		}
		fds = append(fds, fd)
		if err := setFd(d.key, fd, d.dir); err != nil {
			return -1, err
		}
	}
	for _, o := range l.options {
		key, value, hasValue := strings.Cut(o, "=")
		if hasValue {
			err = unix.FsconfigSetString(fsfd, key, value)
		} else {
			err = unix.FsconfigSetFlag(fsfd, key)
		}
		if err != nil {
			return -1, fsError(fsfd, o, err)
		}
	}

	if err := unix.Unmount(path, 0); err != nil {
		return -1, fmt.Errorf("unmount Docker's overlay at %s: %w", path, err)
	}
	if mounted, err := isMountPoint(path); err != nil || mounted {
		return -1, fmt.Errorf("%s is still a mount with Docker's overlay gone (%v)", path, err)
	}
	if err := unix.FsconfigCreate(fsfd); err != nil {
		return -1, fsError(fsfd, "create", err)
	}
	mnt, err := unix.Fsmount(fsfd, unix.FSMOUNT_CLOEXEC, l.attrs)
	if err != nil {
		return -1, fmt.Errorf("fsmount the overlay: %w", err)
	}
	return mnt, nil
}

// fsError adds what the filesystem logged on its fsopen(2) descriptor.
func fsError(fsfd int, what string, err error) error {
	var logged []string
	buf := make([]byte, 1024)
	for len(logged) < 16 {
		n, rerr := unix.Read(fsfd, buf)
		if rerr != nil || n <= 0 {
			break
		}
		logged = append(logged, strings.TrimSpace(string(buf[:n])))
	}
	if len(logged) > 0 {
		return fmt.Errorf("overlay %s: %w (%s)", what, err, strings.Join(logged, "; "))
	}
	return fmt.Errorf("overlay %s: %w", what, err)
}

// releaseIdmappedRootfs unmounts what idmapRootfs made for the container,
// the overlay at Docker's path too if it is still the one put there: for a
// create that failed after it.
func releaseIdmappedRootfs(id string) error {
	rec, err := readRootfsRecord(id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := forgetRootfs(id); err != nil {
		return err
	}
	if err == nil {
		if current, root, err := mountIDAt(rec.Path, unix.STATX_MNT_ID_UNIQUE); err == nil && root && current == rec.MountID {
			if err := unix.Unmount(rec.Path, unix.MNT_DETACH); err != nil {
				return fmt.Errorf("unmount %s: %w", rec.Path, err)
			}
		}
	}
	return nil
}

// unbindRootfs unmounts the container's bind of its overlay, on delete: the
// bind would keep the overlay alive, which Docker unmounts at its own path
// once nothing of its own uses it. The record of the overlay is kept, without
// the bind, so that if Docker keeps it mounted the next create binds it again.
func unbindRootfs(id string) error {
	bind, _, err := rootfsPaths(id)
	if err != nil {
		return err
	}
	if err := unmountAll(bind); err != nil {
		return err
	}
	if err := os.Remove(bind); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rec, err := readRootfsRecord(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	rec.BindMountID = 0
	return writeRootfsRecord(id, rec)
}

// forgetRootfs unmounts the container's bind and forgets both it and the
// overlay it bound.
func forgetRootfs(id string) error {
	bind, record, err := rootfsPaths(id)
	if err != nil {
		return err
	}
	if err := unmountAll(bind); err != nil {
		return err
	}
	for _, p := range []string{bind, record} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// unmountAll unmounts every mount stacked at path.
func unmountAll(path string) error {
	for {
		mounted, err := isMountPoint(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !mounted {
			return nil
		}
		if err := unix.Unmount(path, unix.MNT_DETACH); err != nil {
			return fmt.Errorf("unmount %s: %w", path, err)
		}
	}
}
