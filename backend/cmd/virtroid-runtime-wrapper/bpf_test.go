//go:build linux

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestRewriteSpecIdmapsData: in a user namespace /data and the read-only binds
// are id-mapped, a writable bind other than /data is not, and nothing is
// without one.
func TestRewriteSpecIdmapsData(t *testing.T) {
	doc := `{"mounts":[
		{"destination":"/data","type":"bind","source":"/srv/virtroid/runtimes/r1/data","options":["rbind","rprivate"]},
		{"destination":"/etc/hosts","type":"bind","source":"/var/lib/x/hosts","options":["rbind","rprivate"]},
		{"destination":"/opt/virtroid/gnss","type":"bind","source":"/srv/virtroid/runtimes/r1/gnss","options":["rbind","rro","rprivate"]},
		{"destination":"/opt/virtroid/scrcpy-server.jar","type":"bind","source":"/srv/virtroid/assets/scrcpy-server.jar","options":["rbind","ro","rprivate"]},
		{"destination":"/opt/virtroid/ro-tmpfs","type":"tmpfs","source":"tmpfs","options":["ro"]}
	]}`
	for _, userns := range []bool{true, false} {
		raw, err := decodeSpec([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		rewriteSpec(raw, "", userns)
		wantData := []string{"rbind", "rprivate"}
		wantGnss := []string{"rbind", "rro", "rprivate"}
		wantJar := []string{"rbind", "ro", "rprivate"}
		if userns {
			wantData = append(wantData, "idmap")
			wantGnss = append(wantGnss, "idmap")
			wantJar = append(wantJar, "idmap")
		}
		check := func(when string) {
			t.Helper()
			for _, c := range []struct {
				dest string
				want []string
			}{
				{"/data", wantData},
				{"/opt/virtroid/gnss", wantGnss},
				{"/opt/virtroid/scrcpy-server.jar", wantJar},
				{"/etc/hosts", []string{"rbind", "rprivate"}},
				{"/opt/virtroid/ro-tmpfs", []string{"ro"}},
			} {
				m, _ := mountAt(raw, c.dest)
				if got := toStrings(m["options"]); !reflect.DeepEqual(got, c.want) {
					t.Errorf("userns %v, %s: %s options %v, want %v", userns, when, c.dest, got, c.want)
				}
			}
		}
		check("rewritten once")
		rewriteSpec(raw, "", userns)
		check("rewritten twice")
	}
}

func TestBindBpffs(t *testing.T) {
	raw, err := decodeSpec([]byte(`{"mounts":[{"destination":"/sys/fs/bpf","type":"bpf","source":"bpf"},{"destination":"/proc","type":"proc","source":"proc"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	bindBpffs(raw, "/run/virtroid/bpf/r1")
	m, n := mountAt(raw, "/sys/fs/bpf")
	if n != 1 || m["type"] != "bind" || m["source"] != "/run/virtroid/bpf/r1" {
		t.Errorf("%d mounts at /sys/fs/bpf: %v", n, m)
	}
	if !reflect.DeepEqual(toStrings(m["options"]), []string{"bind", "nosuid", "nodev", "noexec"}) {
		t.Errorf("options %v", m["options"])
	}
	if _, n := mountAt(raw, "/proc"); n != 1 {
		t.Error("another mount was dropped")
	}
}

func TestRuncGlobals(t *testing.T) {
	args := []string{"--root", "/run/docker/runtime-runc/moby", "--log", "/run/x/log.json", "create", "--bundle", "/b", "c1"}
	if got := runcGlobals(args, "create"); !reflect.DeepEqual(got, args[:4]) {
		t.Errorf("got %v", got)
	}
	if got := runcGlobals([]string{"create", "c1"}, "create"); len(got) != 0 {
		t.Errorf("no globals: %v", got)
	}
}

// withBpfRoot points bpfRoot at a fresh directory for the test.
func withBpfRoot(t *testing.T) string {
	t.Helper()
	old := bpfRoot
	bpfRoot = filepath.Join(t.TempDir(), "bpf")
	t.Cleanup(func() { bpfRoot = old })
	return bpfRoot
}

func TestRuntimeBpffsNames(t *testing.T) {
	root := withBpfRoot(t)
	for _, bad := range []string{"", "..", "../x", "a/b", "A", "x y"} {
		if _, err := runtimeBpffsDir(bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
		if err := mountRuntimeBpffs(bad); err == nil {
			t.Errorf("mount of %q accepted", bad)
		}
	}
	// A plain directory there is not a bpffs.
	if err := os.MkdirAll(filepath.Join(root, "r1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeBpffsDir("r1"); err == nil {
		t.Error("a plain directory accepted as a bpffs")
	}
	raw := map[string]any{"annotations": map[string]any{bpffsAnnotation: "r1"}}
	if _, err := runtimeBpffs(raw); err == nil {
		t.Error("a plain directory named as a runtime's bpffs accepted")
	}
	if got, err := runtimeBpffs(map[string]any{}); err != nil || got != "" {
		t.Errorf("none named: %q, %v", got, err)
	}
}

// TestRuntimeBpffsMount: a fresh bpffs each time, accepted only once filled,
// gone after release.
func TestRuntimeBpffsMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mounts a bpffs: needs root")
	}
	root := withBpfRoot(t)
	path := filepath.Join(root, "r1")
	t.Cleanup(func() { releaseRuntimeBpffs("r1") })
	if err := mountRuntimeBpffs("r1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "left-over"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Mounting again starts afresh.
	if err := mountRuntimeBpffs("r1"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(path, "left-over")) {
		t.Error("a second mount kept the first one's contents")
	}
	if n := mountCount(t, path); n != 1 {
		t.Errorf("%d mounts at %s, want 1", n, path)
	}
	raw := map[string]any{"annotations": map[string]any{bpffsAnnotation: "r1"}}
	if _, err := runtimeBpffs(raw); err == nil {
		t.Error("an unfilled bpffs accepted")
	}
	if err := markPreloaded(path); err != nil {
		t.Fatal(err)
	}
	if got, err := runtimeBpffs(raw); err != nil || got != path {
		t.Errorf("filled: %q, %v", got, err)
	}
	if err := releaseRuntimeBpffs("r1"); err != nil {
		t.Fatal(err)
	}
	if exists(path) {
		t.Error("released bpffs still there")
	}
	if err := releaseRuntimeBpffs("r1"); err != nil {
		t.Errorf("releasing again: %v", err)
	}
}

type mapCreateAttr struct {
	mapType    uint32
	keySize    uint32
	valueSize  uint32
	maxEntries uint32
}

type objInfoAttr struct {
	bpfFd   uint32
	infoLen uint32
	info    uint64
}

func newArrayMap(t *testing.T) int {
	t.Helper()
	attr := mapCreateAttr{mapType: 2 /* BPF_MAP_TYPE_ARRAY */, keySize: 4, valueSize: 4, maxEntries: 1}
	fd, _, errno := unix.Syscall(unix.SYS_BPF, unix.BPF_MAP_CREATE, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr))
	if errno != 0 {
		t.Fatalf("map create: %v", errno)
	}
	return int(fd)
}

func bpfMapID(t *testing.T, fd int) uint32 {
	t.Helper()
	var info [80]byte
	attr := objInfoAttr{bpfFd: uint32(fd), infoLen: uint32(len(info)), info: uint64(uintptr(unsafe.Pointer(&info[0])))}
	if _, _, errno := unix.Syscall(unix.SYS_BPF, unix.BPF_OBJ_GET_INFO_BY_FD, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr)); errno != 0 {
		t.Fatalf("obj info: %v", errno)
	}
	return *(*uint32)(unsafe.Pointer(&info[4])) // struct bpf_map_info: type, id, ...
}

// TestBpfCopy: the loading pass's pins land in the runtime's bpffs at the
// same paths, as the same objects, with the same modes and owners moved into
// the runtime's range.
func TestBpfCopy(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mounts bpffs and makes a map: needs root")
	}
	root := withBpfRoot(t)
	src := filepath.Join(t.TempDir(), "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("bpf", src, "bpf", 0, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Unmount(src, unix.MNT_DETACH) })
	if err := os.Mkdir(filepath.Join(src, "netd_shared"), 0o1777); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chown(filepath.Join(src, "netd_shared"), 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chmod(filepath.Join(src, "netd_shared"), 0o1777); err != nil {
		t.Fatal(err)
	}
	var srcDir unix.Stat_t
	if err := unix.Lstat(filepath.Join(src, "netd_shared"), &srcDir); err != nil {
		t.Fatal(err)
	}
	m := newArrayMap(t)
	defer unix.Close(m)
	pin := filepath.Join(src, "netd_shared", "map_netd_test")
	if err := bpfObjPin(m, pin); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chown(pin, 1000, 3007); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chmod(pin, 0o460); err != nil {
		t.Fatal(err)
	}
	if err := mountRuntimeBpffs("r1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseRuntimeBpffs("r1") })
	dst := filepath.Join(root, "r1")

	const offset = 1000000
	n, err := bpfCopy(src, dst, offset)
	if err != nil || n != 1 {
		t.Fatalf("copied %d, %v", n, err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(filepath.Join(dst, "netd_shared", "map_netd_test"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Uid != 1000+offset || st.Gid != 3007+offset || st.Mode&0o7777 != 0o460 {
		t.Errorf("pin owner %d:%d mode %o, want %d:%d 460", st.Uid, st.Gid, st.Mode&0o7777, 1000+offset, 3007+offset)
	}
	if err := unix.Lstat(filepath.Join(dst, "netd_shared"), &st); err != nil {
		t.Fatal(err)
	}
	if st.Uid != offset || st.Mode&0o7777 != srcDir.Mode&0o7777 {
		t.Errorf("directory owner %d mode %o, want %d %o", st.Uid, st.Mode&0o7777, offset, srcDir.Mode&0o7777)
	}
	copied, err := bpfObjGet(filepath.Join(dst, "netd_shared", "map_netd_test"))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(copied)
	if a, b := bpfMapID(t, m), bpfMapID(t, copied); a != b {
		t.Errorf("the copy is map %d, not the original %d", b, a)
	}
	if exists(filepath.Join(dst, preloadedMarker)) {
		t.Error("bpfCopy marked the bpffs itself")
	}
	// netd's programs are not there: attaching fails, and leaves no marker.
	cg := t.TempDir()
	if err := attachNetdPrograms(cg, dst); err == nil {
		t.Error("attach without netd's programs succeeded")
	}
	if exists(filepath.Join(dst, netdAttachedMarker)) {
		t.Error("a failed attach left the marker")
	}
}
