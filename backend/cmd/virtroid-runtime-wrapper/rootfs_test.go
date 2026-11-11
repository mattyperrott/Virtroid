//go:build linux

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseMountinfo(t *testing.T) {
	data := "24 1 0:19 / / rw,relatime - overlay overlay rw,lowerdir=/mnt/lower,upperdir=/mnt/ov/u,workdir=/mnt/ov/w,uuid=on\n" +
		"78 36 0:30 / /srv/a\\040b rw,nosuid,relatime shared:2 master:7 - overlay overlay rw,lowerdir=/x\n" +
		"not a mountinfo line\n" +
		"99 36 8:1 /sub /srv/bind rw - ext4 /dev/vda rw\n"
	got := parseMountinfo([]byte(data))
	want := []mountinfoEntry{
		{24, "/", "/", "rw,relatime", "overlay", "rw,lowerdir=/mnt/lower,upperdir=/mnt/ov/u,workdir=/mnt/ov/w,uuid=on"},
		{78, "/", "/srv/a b", "rw,nosuid,relatime", "overlay", "rw,lowerdir=/x"},
		{99, "/sub", "/srv/bind", "rw", "ext4", "rw"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if got := unescapeOctal(`a\072b\134c\0`); got != `a:b\c\0` {
		t.Errorf("unescape: %q", got)
	}
}

// TestParseOverlay: Docker's rootfs as mountinfo shows it, and what isn't
// one (or can't be rebuilt from what mountinfo shows).
func TestParseOverlay(t *testing.T) {
	const snaps = "/srv/virtroid/docker/containerd/daemon/io.containerd.snapshotter.v1.overlayfs/snapshots"
	docker := mountinfoEntry{
		id: 78, root: "/", mountPoint: "/srv/virtroid/docker/rootfs/overlayfs/c1", mountOpts: "rw,relatime", fsType: "overlay",
		superOpts: "rw,lowerdir=" + snaps + "/16/fs:" + snaps + "/3/fs:" + snaps + `/2/f\072s,upperdir=` + snaps + "/17/fs,workdir=" + snaps + "/17/work,redirect_dir=on,xino=off",
	}
	l, err := parseOverlay(docker)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{snaps + "/16/fs", snaps + "/3/fs", snaps + "/2/f:s"}; !reflect.DeepEqual(l.lowers, want) {
		t.Errorf("lowers %q", l.lowers)
	}
	if l.upper != snaps+"/17/fs" || l.work != snaps+"/17/work" {
		t.Errorf("upper %s work %s", l.upper, l.work)
	}
	if !reflect.DeepEqual(l.options, []string{"redirect_dir=on", "xino=off"}) {
		t.Errorf("options %q", l.options)
	}
	if l.attrs != unix.MOUNT_ATTR_RELATIME {
		t.Errorf("attrs %#x", l.attrs)
	}
	with := func(f func(e *mountinfoEntry)) mountinfoEntry {
		e := docker
		f(&e)
		return e
	}
	sub := func(old, new string) func(e *mountinfoEntry) {
		return func(e *mountinfoEntry) { e.superOpts = strings.Replace(e.superOpts, old, new, 1) }
	}
	for name, e := range map[string]mountinfoEntry{
		"not overlay":       with(func(e *mountinfoEntry) { e.fsType = "ext4" }),
		"a bind inside one": with(func(e *mountinfoEntry) { e.root = "/etc" }),
		"read-only":         with(sub("rw,", "ro,")),
		"data-only layers":  with(sub(snaps+"/3/fs:", snaps+"/3/fs::")),
		"relative layer":    with(sub(snaps+"/16/fs", "l/ABC")),
		"layer as an fd":    with(sub(snaps+"/16/fs", "/")),
		"unclean layer":     with(sub(snaps+"/16/fs", snaps+"/16/../16/fs")),
		"no upper":          with(sub("upperdir="+snaps+"/17/fs,", "")),
		"upper, work apart": with(sub(snaps+"/17/work", snaps+"/18/work")),
		"id-mapped":         with(func(e *mountinfoEntry) { e.mountOpts = "rw,relatime,idmapped" }),
	} {
		if _, err := parseOverlay(e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for opts, want := range map[string]int{
		"rw,nosuid,nodev,noexec,relatime": unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC | unix.MOUNT_ATTR_RELATIME,
		"ro,noatime,nodiratime":           unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOATIME | unix.MOUNT_ATTR_NODIRATIME,
		"rw":                              unix.MOUNT_ATTR_STRICTATIME,
	} {
		if got, err := mountAttrs(opts); err != nil || got != want {
			t.Errorf("%s: %#x, %v; want %#x", opts, got, err, want)
		}
	}
}

// TestRuntimeLog: the wrapper's errors land in runc's --log file in its
// format, where containerd reads a failed create's reason.
func TestRuntimeLog(t *testing.T) {
	old := runtimeLog
	t.Cleanup(func() { runtimeLog = old })
	path := filepath.Join(t.TempDir(), "log.json")
	setRuntimeLog([]string{"--root", "/run/docker/runtime-runc/moby", "--log", path, "--log-format", "json", "--systemd-cgroup"})
	if runtimeLog.path != path || runtimeLog.format != "json" {
		t.Fatalf("%+v", runtimeLog)
	}
	logRuntime("warning", "first")
	logRuntime("error", "virtroid-runtime-wrapper: prepare bundle: rootfs: no")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// As containerd's go-runc reads it: the last error entry's msg.
	var last string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry struct{ Level, Msg, Time string }
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if entry.Time == "" {
			t.Errorf("%q has no time", line)
		}
		if entry.Level == "error" {
			last = entry.Msg
		}
	}
	if last != "virtroid-runtime-wrapper: prepare bundle: rootfs: no" {
		t.Errorf("last error %q", last)
	}

	runtimeLog = old
	text := filepath.Join(t.TempDir(), "log")
	setRuntimeLog([]string{"--log=" + text, "--log-format=text"})
	logRuntime("error", `a "quoted" b`)
	if data, _ := os.ReadFile(text); !strings.Contains(string(data), `level=error msg="a \"quoted\" b"`) {
		t.Errorf("text log %q", data)
	}
	runtimeLog = old
	runtimeLog.path = ""
	logRuntime("error", "nowhere") // no --log: nothing, and no file
}

func kernelAtLeast(t *testing.T, major, minor int) bool {
	t.Helper()
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		t.Fatal(err)
	}
	release := unix.ByteSliceToString(u.Release[:])
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		t.Fatalf("kernel release %q", release)
	}
	maj, _ := strconv.Atoi(parts[0])
	mnr, _ := strconv.Atoi(strings.TrimRightFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	return maj > major || maj == major && mnr >= minor
}

// testDir is a directory for mounts: under VIRTROID_TEST_DIR, to test on the
// filesystem Docker's layers are on, or a temporary one. Others may search it
// (a process without the VM root's fs id walks through it).
func testDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if base := os.Getenv("VIRTROID_TEST_DIR"); base != "" {
		if err := os.MkdirAll(base, 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		if dir, err = os.MkdirTemp(base, "test"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	for d := dir; d != "/"; d = filepath.Dir(d) {
		if fi, err := os.Stat(d); err == nil && fi.Mode().Perm()&0o001 == 0 {
			if err := os.Chmod(d, fi.Mode().Perm()|0o011); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

// asFsid runs f with these fs ids, as a process outside the VM's root: the
// file capabilities go with the ids (no securebits here).
func asFsid(t *testing.T, id int, f func() error) {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with its changed ids
		unix.SetfsgidRetGid(id)
		unix.SetfsuidRetUid(id)
		if cur, _ := unix.SetfsuidRetUid(-1); cur != id {
			errc <- fmt.Errorf("fs uid %d, not %d", cur, id)
			return
		}
		errc <- f()
	}()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func ownerOf(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Uid, st.Gid
}

// TestIdmapRootfs: Docker's overlay at the rootfs path is replaced by one of
// the same layers, id-mapped, and bound where the container's root can reach
// it. Through it the image's files and Docker's own read as base+id and the
// container's root writes, deletes (whiteouts the overlay makes itself) and
// chowns land on disk as the ids themselves, while the VM's root can't write
// there at all; a second create keeps it, a create after Docker remounted
// replaces it, delete unbinds it and release unmounts both.
func TestIdmapRootfs(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mounts: needs root")
	}
	if !kernelAtLeast(t, 6, 13) {
		t.Skip("overlay layers as file descriptors: Linux 6.13 or later")
	}
	base := testDir(t)
	oldDir := idmappedRootDir
	idmappedRootDir = filepath.Join(base, "run", "rootfs")
	t.Cleanup(func() { idmappedRootDir = oldDir })

	snaps := filepath.Join(base, "snapshots")
	l1, l2 := filepath.Join(snaps, "1", "fs"), filepath.Join(snaps, "2", "fs")
	upper, work := filepath.Join(snaps, "3", "fs"), filepath.Join(snaps, "3", "work")
	rootfs := filepath.Join(base, "rootfs", "c1")
	for _, d := range []string{l1, l2, upper, work, rootfs, filepath.Join(l1, "etc"), filepath.Join(l1, "system", "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Docker's directories on the way to its rootfs are the VM root's alone.
	if err := os.Chmod(filepath.Join(base, "rootfs"), 0o700); err != nil {
		t.Fatal(err)
	}
	put := func(path string, mode os.FileMode, uid, gid int) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(l1, "system", "bin", "logd"), 0o550, 1036, 1036)
	put(filepath.Join(l1, "etc", "gone"), 0o644, 0, 0)
	put(filepath.Join(l2, "etc", "hosts"), 0o644, 0, 0)
	put(filepath.Join(upper, "fromdocker"), 0o644, 2000, 2000)

	opts := fmt.Sprintf("lowerdir=%s:%s,upperdir=%s,workdir=%s", l2, l1, upper, work)
	dockerMount := func() uint64 {
		t.Helper()
		if err := unix.Mount("overlay", rootfs, "overlay", unix.MS_NOSUID|unix.MS_NODEV, opts); err != nil {
			t.Fatal(err)
		}
		id, _, err := mountIDAt(rootfs, unix.STATX_MNT_ID_UNIQUE)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	unmountAll := func(path string) {
		for unix.Unmount(path, unix.MNT_DETACH) == nil {
		}
	}
	t.Cleanup(func() {
		unmountAll(filepath.Join(idmappedRootDir, "c1"))
		unmountAll(rootfs)
	})
	docker := dockerMount()
	// Docker's own files, written as the VM's root before the container starts.
	put(filepath.Join(rootfs, "etc", "hostname"), 0o644, 0, 0)

	m := idMap{0, 1000000, 1000000}
	bind, err := idmapRootfs(rootfs, "c1", m, m)
	if err != nil {
		t.Fatal(err)
	}
	if bind != filepath.Join(idmappedRootDir, "c1") {
		t.Errorf("bind at %s", bind)
	}
	ours, root, err := mountIDAt(rootfs, unix.STATX_MNT_ID_UNIQUE)
	if err != nil || !root || ours == docker {
		t.Fatalf("mount %d (Docker's %d), root %v, %v", ours, docker, root, err)
	}
	if n := mountCount(t, rootfs); n != 1 {
		t.Errorf("%d mounts at %s: Docker's overlay should be gone", n, rootfs)
	}
	bound, broot, err := mountIDAt(bind, unix.STATX_MNT_ID_UNIQUE)
	if err != nil || !broot || bound == ours {
		t.Fatalf("bind mount %d, root %v, %v", bound, broot, err)
	}
	data, _ := os.ReadFile("/proc/self/mountinfo")
	for _, path := range []string{rootfs, bind} {
		short, _, _ := mountIDAt(path, unix.STATX_MNT_ID)
		for _, e := range parseMountinfo(data) {
			if e.id == short && (e.fsType != "overlay" || !strings.Contains(e.mountOpts, "nosuid") || !strings.Contains(e.mountOpts, "nodev")) {
				t.Errorf("the mount at %s: %+v", path, e)
			}
		}
	}

	for rel, want := range map[string]uint32{".": 1000000, "system/bin/logd": 1001036, "etc/hosts": 1000000, "etc/hostname": 1000000, "fromdocker": 1002000} {
		for _, path := range []string{rootfs, bind} {
			if uid, gid := ownerOf(t, filepath.Join(path, rel)); uid != want || gid != want {
				t.Errorf("%s: %d:%d through %s, want %d", rel, uid, gid, path, want)
			}
		}
	}

	// The container's root (base on the VM), by the path runc gets.
	asFsid(t, 1000000, func() error {
		if _, err := os.Stat(filepath.Join(rootfs, "etc")); !errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("Docker's path reached without the VM root's ids: %v", err)
		}
		if err := os.WriteFile(filepath.Join(bind, "etc", "new"), []byte("n"), 0o600); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(bind, "etc", "gone")); err != nil {
			return fmt.Errorf("delete a file of the image: %w", err)
		}
		return os.Mkdir(filepath.Join(bind, "made"), 0o755)
	})
	for _, rel := range []string{"etc/new", "made", "etc"} {
		if uid, gid := ownerOf(t, filepath.Join(upper, rel)); uid != 0 || gid != 0 {
			t.Errorf("%s on disk: %d:%d, want 0:0", rel, uid, gid)
		}
	}
	if !exists(filepath.Join(rootfs, "etc", "new")) {
		t.Error("written through the bind, not seen at Docker's path: two overlays")
	}
	var st unix.Stat_t
	if err := unix.Lstat(filepath.Join(upper, "etc", "gone"), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFCHR || st.Rdev != 0 || st.Uid != 0 {
		t.Errorf("etc/gone in the writable layer: mode %o rdev %d uid %d, %v; want a whiteout owned by 0", st.Mode, st.Rdev, st.Uid, err)
	}
	if exists(filepath.Join(rootfs, "etc", "gone")) || !exists(filepath.Join(l1, "etc", "gone")) {
		t.Error("the deleted file: still there through the overlay, or gone from its layer")
	}

	// A chown inside the range lands as the id itself, after a copy-up.
	if err := os.Lchown(filepath.Join(rootfs, "system", "bin", "logd"), 1001037, 1001037); err != nil {
		t.Fatal(err)
	}
	if uid, _ := ownerOf(t, filepath.Join(upper, "system", "bin", "logd")); uid != 1037 {
		t.Errorf("chowned file on disk: %d, want 1037", uid)
	}
	if uid, _ := ownerOf(t, filepath.Join(l1, "system", "bin", "logd")); uid != 1036 {
		t.Errorf("the image's layer changed: %d", uid)
	}
	if b, err := os.ReadFile(filepath.Join(rootfs, "system", "bin", "logd")); err != nil || string(b) != "x" {
		t.Errorf("copied-up contents %q, %v", b, err)
	}

	// The VM's root owns nothing here.
	if err := os.WriteFile(filepath.Join(rootfs, "hostroot"), nil, 0o644); !errors.Is(err, unix.EOVERFLOW) {
		t.Errorf("a write as the VM's root: %v, want EOVERFLOW", err)
	}

	// A second create of the same container keeps both.
	if again, err := idmapRootfs(rootfs, "c1", m, m); err != nil || again != bind {
		t.Fatal(again, err)
	}
	a, _, _ := mountIDAt(rootfs, unix.STATX_MNT_ID_UNIQUE)
	b, _, _ := mountIDAt(bind, unix.STATX_MNT_ID_UNIQUE)
	if a != ours || b != bound || mountCount(t, rootfs) != 1 || mountCount(t, bind) != 1 {
		t.Errorf("rebuilt or stacked: %d %d, %d and %d mounts", a, b, mountCount(t, rootfs), mountCount(t, bind))
	}

	// Delete: the bind goes, the overlay at Docker's path is Docker's to
	// unmount, and its record stays.
	if err := unbindRootfs("c1"); err != nil {
		t.Fatal(err)
	}
	if mounted, _ := isMountPoint(rootfs); !mounted || exists(bind) || !exists(bind+".json") {
		t.Errorf("after delete: Docker's path mounted %v, bind %v, record %v", mounted, exists(bind), exists(bind+".json"))
	}
	// Docker kept it mounted (a start after a stop it didn't unmount at):
	// the next create binds the same overlay again.
	if again, err := idmapRootfs(rootfs, "c1", m, m); err != nil || again != bind {
		t.Fatal("start on the overlay Docker kept:", again, err)
	}
	a, _, _ = mountIDAt(rootfs, unix.STATX_MNT_ID_UNIQUE)
	b, _, _ = mountIDAt(bind, unix.STATX_MNT_ID_UNIQUE)
	if a != ours || b == bound || mountCount(t, rootfs) != 1 || mountCount(t, bind) != 1 {
		t.Errorf("not the same overlay bound anew: %d (was %d), bind %d (was %d), %d and %d mounts", a, ours, b, bound, mountCount(t, rootfs), mountCount(t, bind))
	}
	if rec, err := readRootfsRecord("c1"); err != nil || rec.MountID != ours || rec.BindMountID != b {
		t.Errorf("record %+v, %v", rec, err)
	}
	asFsid(t, 1000000, func() error {
		return os.WriteFile(filepath.Join(bind, "after-restart"), []byte("x"), 0o644)
	})
	if uid, gid := ownerOf(t, filepath.Join(upper, "after-restart")); uid != 0 || gid != 0 {
		t.Errorf("written through the new bind, on disk: %d:%d, want 0:0", uid, gid)
	}
	// One the wrapper made but no longer records is not taken for Docker's.
	if err := forgetRootfs("c1"); err != nil {
		t.Fatal(err)
	}
	if mounted, _ := isMountPoint(rootfs); !mounted || exists(bind) || exists(bind+".json") {
		t.Errorf("after forget: Docker's path mounted %v, bind %v, record %v", mounted, exists(bind), exists(bind+".json"))
	}
	if _, err := idmapRootfs(rootfs, "c1", m, m); err == nil {
		t.Error("the wrapper's own overlay taken for Docker's")
	}

	// Docker unmounts and mounts its overlay again (a restart): a new create
	// replaces what an earlier one left, its bind first.
	unmountAll(rootfs)
	dockerMount()
	if _, err := idmapRootfs(rootfs, "c1", m, m); err != nil {
		t.Fatal(err)
	}
	unmountAll(rootfs)
	dockerMount()
	if _, err := idmapRootfs(rootfs, "c1", m, m); err != nil {
		t.Fatal(err)
	}
	if mountCount(t, rootfs) != 1 || mountCount(t, bind) != 1 {
		t.Errorf("after a remount: %d and %d mounts", mountCount(t, rootfs), mountCount(t, bind))
	}

	// A failed create: both go.
	if err := releaseIdmappedRootfs("c1"); err != nil {
		t.Fatal(err)
	}
	if mounted, _ := isMountPoint(rootfs); mounted || exists(bind) || exists(bind+".json") {
		t.Errorf("after release: mounted %v, bind %v, record %v", mounted, exists(bind), exists(bind+".json"))
	}
	if _, err := idmapRootfs(rootfs, "c1", m, m); err == nil {
		t.Error("a rootfs that is no mount accepted")
	}
	for _, bad := range []string{"", "..", "a/b"} {
		if _, err := idmapRootfs(rootfs, bad, m, m); err == nil {
			t.Errorf("id %q accepted", bad)
		}
	}
	// A bind the container's root couldn't reach is refused.
	dockerMount()
	idmappedRootDir = filepath.Join(base, "rootfs", "binds")
	if _, err := idmapRootfs(rootfs, "c1", m, m); err == nil || !strings.Contains(err.Error(), "closed to the runtime's root") {
		t.Errorf("bind under a closed directory: %v", err)
	}
	if mountCount(t, rootfs) != 1 {
		t.Error("Docker's overlay touched before the bind's directory was checked")
	}
}
