//go:build linux

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseInvocation(t *testing.T) {
	cwd, _ := os.Getwd()
	cases := []struct {
		name             string
		args             []string
		verb, bundle, id string
	}{
		{"docker create", []string{"--root", "/run/docker/runtime-runc/moby", "--log", "/b/log.json", "--log-format", "json",
			"create", "--bundle", "/b/1", "--pid-file", "/b/1/init.pid", "abc"}, "create", "/b/1", "abc"},
		{"bundle with =", []string{"--root", "/r", "create", "--bundle=/b/2", "abc"}, "create", "/b/2", "abc"},
		{"short -b", []string{"create", "-b", "/b/3", "abc"}, "create", "/b/3", "abc"},
		{"other verb", []string{"--root", "/r", "start", "abc"}, "start", cwd, "abc"},
		// A container named like a verb doesn't change the verb.
		{"id named create", []string{"--root", "/r", "delete", "--force", "create"}, "delete", cwd, "create"},
		{"no id", []string{"--root", "/r", "list"}, "list", cwd, ""},
		{"flag last", []string{"--root", "/r", "features", "--json"}, "features", cwd, ""},
	}
	for _, c := range cases {
		verb, bundle, id := parseInvocation(c.args)
		if verb != c.verb || bundle != c.bundle || id != c.id {
			t.Errorf("%s: got (%q, %q, %q), want (%q, %q, %q)", c.name, verb, bundle, id, c.verb, c.bundle, c.id)
		}
	}
}

func TestMapID(t *testing.T) {
	maps := []idMap{{ContainerID: 0, HostID: 1000000, Size: 65536}, {ContainerID: 65536, HostID: 3000000, Size: 10}}
	for id, want := range map[uint32]uint32{0: 1000000, 2000: 1002000, 65535: 1065535, 65536: 3000000, 65545: 3000009} {
		if got, err := mapID(maps, id); err != nil || got != want {
			t.Errorf("mapID(%d) = %d, %v; want %d", id, got, err, want)
		}
	}
	if _, err := mapID(maps, 65546); err == nil {
		t.Error("an unmapped id mapped")
	}
	if _, err := mapID(nil, 0); err == nil {
		t.Error("no mappings, yet root mapped")
	}
}

func TestDeviceDir(t *testing.T) {
	if dir, err := deviceDir("abc123"); err != nil || dir != devicesRoot+"/abc123" {
		t.Errorf("got %q, %v", dir, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../x"} {
		if _, err := deviceDir(bad); err == nil {
			t.Errorf("id %q accepted", bad)
		}
	}
}

func TestDecodeDeviceRejectsOddPaths(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "dev/kmsg", "/dev/../etc/x", "/dev/a!b", "/dev//kmsg"} {
		entry := map[string]any{"path": path, "type": "c", "major": json.Number("1"), "minor": json.Number("3")}
		if _, err := decodeDevice(entry); err == nil {
			t.Errorf("%q accepted", path)
		}
	}
	if _, err := decodeDevice(map[string]any{"path": "/dev/x", "type": "c"}); err == nil {
		t.Error("device without major/minor accepted")
	}
	// Out of range is refused, not truncated: uid 4294967296 would have
	// become 0, the container's root.
	for key, value := range map[string]string{"uid": "4294967296", "gid": "-1", "fileMode": "1.5", "major": "4294967296"} {
		entry := map[string]any{"path": "/dev/x", "type": "c", "major": json.Number("1"), "minor": json.Number("3")}
		entry[key] = json.Number(value)
		if d, err := decodeDevice(entry); err == nil {
			t.Errorf("%s %s accepted as %+v", key, value, d)
		}
	}
	if _, err := decodeDevice(map[string]any{"path": "/dev/x", "type": "c", "major": 1.0, "minor": 3.0}); err == nil {
		t.Error("a non-json.Number major accepted")
	}
}

// TestDecodeSpecKeepsNumbers: numbers come back out exactly as Docker wrote
// them, however large (an unlimited rlimit went through a float64 once and
// runc refused the spec).
func TestDecodeSpecKeepsNumbers(t *testing.T) {
	doc := `{"process":{"rlimits":[{"type":"RLIMIT_MEMLOCK","hard":18446744073709551615,"soft":18446744073709551615}]},
	  "mounts":[{"destination":"/sys/fs/cgroup","type":"cgroup","source":"cgroup","options":["ro"]}],
	  "linux":{"resources":{"memory":{"limit":3221225472,"swap":9223372036854775807}}}}`
	raw, err := decodeSpec([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	rewriteSpec(raw, "/run/virtroid/vapex/k", true)
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"hard":18446744073709551615`, `"soft":18446744073709551615`, `"limit":3221225472`, `"swap":9223372036854775807`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%s lost: %s", want, out)
		}
	}
	for _, bad := range []string{`{} {}`, `[]`, `null`, `{`} {
		if _, err := decodeSpec([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPrivateDevices(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("makes device nodes: needs root")
	}
	doc := `{"mounts":[{"destination":"/dev","type":"tmpfs","source":"tmpfs"}],
	  "linux":{"devices":[
	    {"path":"/dev/kmsg","type":"c","major":1,"minor":11,"fileMode":438,"uid":0,"gid":0},
	    {"path":"/dev/net/tun","type":"c","major":10,"minor":200,"fileMode":432},
	    {"path":"/dev/loop0","type":"b","major":7,"minor":0,"uid":1000,"gid":6}
	  ],"resources":{"devices":[{"allow":false,"access":"rwm"}]}}}`
	raw, err := decodeSpec([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "c1")
	maps := []idMap{{ContainerID: 0, HostID: 1000000, Size: 65536}}
	if err := privateDevices(raw, dir, maps, maps); err != nil {
		t.Fatal(err)
	}
	linux := raw["linux"].(map[string]any)
	if n := len(linux["devices"].([]any)); n != 0 {
		t.Errorf("%d device entries left for runc to bind from the VM", n)
	}
	if linux["resources"] == nil {
		t.Error("the device cgroup rules were dropped")
	}
	want := map[string]struct {
		node      string
		kind      uint32
		mode      uint32
		uid, gid  uint32
		maj, mino uint32
	}{
		"/dev/kmsg":    {"kmsg", unix.S_IFCHR, 0o666, 1000000, 1000000, 1, 11},
		"/dev/net/tun": {"net!tun", unix.S_IFCHR, 0o660, 1000000, 1000000, 10, 200},
		"/dev/loop0":   {"loop0", unix.S_IFBLK, 0o666, 1001000, 1000006, 7, 0},
	}
	for dest, w := range want {
		m, n := mountAt(raw, dest)
		if n != 1 || m["type"] != "bind" || m["source"] != filepath.Join(dir, w.node) {
			t.Errorf("%s: %d mounts, %v", dest, n, m)
			continue
		}
		var st unix.Stat_t
		if err := unix.Lstat(filepath.Join(dir, w.node), &st); err != nil {
			t.Errorf("%s: %v", dest, err)
			continue
		}
		if st.Mode&unix.S_IFMT != w.kind || st.Mode&0o7777 != w.mode || st.Uid != w.uid || st.Gid != w.gid ||
			unix.Major(st.Rdev) != w.maj || unix.Minor(st.Rdev) != w.mino {
			t.Errorf("%s: mode %o uid %d gid %d dev %d:%d", dest, st.Mode, st.Uid, st.Gid, unix.Major(st.Rdev), unix.Minor(st.Rdev))
		}
	}
	// Redone for a re-created container: starts again from an empty directory.
	raw2, err := decodeSpec([]byte(`{"linux":{"devices":[{"path":"/dev/kmsg","type":"c","major":1,"minor":11}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := privateDevices(raw2, dir, maps, maps); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "loop0")); !os.IsNotExist(err) {
		t.Error("a stale node from the earlier run survived")
	}
}

func TestSetMountRW(t *testing.T) {
	if got := setMountRW([]string{"nosuid", "ro", "nodev"}); !reflect.DeepEqual(got, []string{"nosuid", "nodev", "rw"}) {
		t.Errorf("ro mount: got %v", got)
	}
	if got := setMountRW([]string{"rw", "nosuid"}); !reflect.DeepEqual(got, []string{"rw", "nosuid"}) {
		t.Errorf("rw mount: got %v", got)
	}
}

func specWithMounts(t *testing.T) map[string]any {
	t.Helper()
	doc := `{"mounts":[
		{"destination":"/proc","type":"proc","source":"proc"},
		{"destination":"/sys/fs/cgroup","type":"cgroup","source":"cgroup","options":["nosuid","noexec","nodev","relatime","ro"]}
	]}`
	raw, err := decodeSpec([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mountAt(raw map[string]any, dest string) (map[string]any, int) {
	var found map[string]any
	n := 0
	for _, m := range raw["mounts"].([]any) {
		mm := m.(map[string]any)
		if mm["destination"] == dest {
			found = mm
			n++
		}
	}
	return found, n
}

func TestRewriteSpec(t *testing.T) {
	raw := specWithMounts(t)
	rewriteSpec(raw, "/run/virtroid/vapex/k", true)

	cg, _ := mountAt(raw, "/sys/fs/cgroup")
	if got := toStrings(cg["options"]); !reflect.DeepEqual(got, []string{"nosuid", "noexec", "nodev", "relatime", "rw"}) {
		t.Errorf("cgroup options: %v", got)
	}
	apex, n := mountAt(raw, apexPremountDir)
	if n != 1 {
		t.Fatalf("%d APEX binds, want 1", n)
	}
	if apex["source"] != "/run/virtroid/vapex/k" || apex["type"] != "bind" {
		t.Errorf("APEX bind: %v", apex)
	}
	if got := toStrings(apex["options"]); !reflect.DeepEqual(got, []string{"rbind", "ro", "nosuid", "nodev", "ridmap"}) {
		t.Errorf("APEX bind options in a user namespace: %v", got)
	}

	// A re-run (runc create retried on the same bundle) replaces, not stacks.
	rewriteSpec(raw, "/run/virtroid/vapex/k2", true)
	if apex, n := mountAt(raw, apexPremountDir); n != 1 || apex["source"] != "/run/virtroid/vapex/k2" {
		t.Errorf("after re-run: %d binds, %v", n, apex)
	}
	// It still serialises as an OCI spec.
	if _, err := json.Marshal(raw); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteSpecWithoutUserNamespace(t *testing.T) {
	raw := specWithMounts(t)
	rewriteSpec(raw, "/s", false)
	apex, _ := mountAt(raw, apexPremountDir)
	if got := toStrings(apex["options"]); !reflect.DeepEqual(got, []string{"rbind", "ro", "nosuid", "nodev"}) {
		t.Errorf("without a user namespace nothing is id-mapped: %v", got)
	}
}

// writeApex writes a minimal APEX: a stored payload and a manifest.
func writeApex(t *testing.T, path string, manifest, payload []byte, method uint16) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range []struct {
		name   string
		data   []byte
		method uint16
	}{{"apex_manifest.pb", manifest, zip.Deflate}, {"apex_payload.img", payload, method}} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.name, Method: e.method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func manifestFor(name string) []byte {
	// Field 1 (name), then field 2 (version, varint 1) as apexer writes it.
	return append(append([]byte{0x0a, byte(len(name))}, name...), 0x10, 0x01)
}

func openEntry(t *testing.T, path, name string) (*zip.ReadCloser, *zip.File) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			return zr, f
		}
	}
	zr.Close()
	t.Fatalf("no %s", name)
	return nil, nil
}

func TestApexManifestName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.apex")
	writeApex(t, path, manifestFor("com.android.runtime"), []byte("payload"), zip.Store)
	zr, f := openEntry(t, path, "apex_manifest.pb")
	defer zr.Close()
	if name, err := apexManifestName(f); err != nil || name != "com.android.runtime" {
		t.Errorf("got %q, %v", name, err)
	}

	bad := map[string][]byte{
		"name not first": {0x10, 0x01},
		"overlong":       {0x0a, 0x7f, 'a'},
		"path in name":   manifestFor("../etc"),
	}
	for label, data := range bad {
		p := filepath.Join(dir, "bad.apex")
		writeApex(t, p, data, []byte("payload"), zip.Store)
		zr, f := openEntry(t, p, "apex_manifest.pb")
		if name, err := apexManifestName(f); err == nil {
			t.Errorf("%s: accepted %q", label, name)
		}
		zr.Close()
	}
	if _, err := apexManifestName(nil); err == nil {
		t.Error("missing manifest accepted")
	}
}

// mountCount is how many mounts sit at path.
func mountCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) > 4 && fields[4] == path {
			n++
		}
	}
	return n
}

// TestDelegateCgroupOnlyChowns: delegation hands over the cgroup and the files
// a delegatee writes, and enables no controllers, here or in the parent. (Doing
// so made the container's cgroup a thread root, and Android then could not put
// a single service in a cgroup of its own.)
func TestDelegateCgroupOnlyChowns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chowns files: needs root")
	}
	parent := t.TempDir()
	cg := filepath.Join(parent, "runtime")
	if err := os.Mkdir(cg, 0o755); err != nil {
		t.Fatal(err)
	}
	for dir, files := range map[string][]string{
		parent: {"cgroup.subtree_control"},
		cg:     {"cgroup.controllers", "cgroup.procs", "cgroup.threads", "cgroup.subtree_control", "memory.max"},
	} {
		for _, f := range files {
			content := ""
			if f == "cgroup.controllers" {
				content = "cpuset cpu io memory pids\n"
			}
			if err := os.WriteFile(filepath.Join(dir, f), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	const base = 1000000
	if err := delegateCgroup(cg, base); err != nil {
		t.Fatal(err)
	}
	owner := func(p string) uint32 {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err != nil {
			t.Fatal(err)
		}
		return st.Uid
	}
	for _, name := range []string{"", "cgroup.procs", "cgroup.threads", "cgroup.subtree_control"} {
		if got := owner(filepath.Join(cg, name)); got != base {
			t.Errorf("%q owned by %d, want %d", name, got, base)
		}
	}
	// A limit set from outside stays the VM's.
	if got := owner(filepath.Join(cg, "memory.max")); got != 0 {
		t.Errorf("memory.max owned by %d: the container could raise its own limit", got)
	}
	for _, dir := range []string{parent, cg} {
		if data, _ := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control")); len(data) != 0 {
			t.Errorf("%s: subtree_control written: %q", dir, data)
		}
	}
	// Not delegating (no cgroup path) is a no-op.
	if err := delegateCgroup("", base); err != nil {
		t.Error(err)
	}
}

func TestCgroupFromBundle(t *testing.T) {
	bundle := t.TempDir()
	write := func(cg string, maps bool) {
		spec := map[string]any{"linux": map[string]any{"cgroupsPath": cg}}
		if maps {
			spec["linux"].(map[string]any)["uidMappings"] = []any{map[string]any{"containerID": 0, "hostID": 1000000, "size": 65536}}
		}
		data, _ := json.Marshal(spec)
		if err := os.WriteFile(filepath.Join(bundle, "config.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("/docker/abc", true)
	if path, base := cgroupFromBundle(bundle); path != "/sys/fs/cgroup/docker/abc" || base != 1000000 {
		t.Errorf("cgroupfs path: %q %d", path, base)
	}
	write("system.slice:docker:abc", true)
	if path, _ := cgroupFromBundle(bundle); path != "" {
		t.Errorf("systemd cgroup resolved to %q; it should be left to runc", path)
	}
	write("/docker/abc", false)
	if _, base := cgroupFromBundle(bundle); base != 0 {
		t.Errorf("no user namespace, base %d", base)
	}
}

func TestPayloadFsType(t *testing.T) {
	const offset = 4096
	image := func(set func(b []byte)) *bytes.Reader {
		b := make([]byte, offset+0x1000)
		set(b[offset:])
		return bytes.NewReader(b)
	}
	erofs := image(func(b []byte) { binary.LittleEndian.PutUint32(b[erofsMagicOffset:], erofsSuperblockMagic) })
	ext4 := image(func(b []byte) { binary.LittleEndian.PutUint16(b[ext4SuperblockMagicOffset:], ext4Magic) })
	for want, r := range map[string]*bytes.Reader{"erofs": erofs, "ext4": ext4} {
		if got, err := payloadFsType(r, offset); err != nil || got != want {
			t.Errorf("%s: got %q, %v", want, got, err)
		}
	}
	if got, err := payloadFsType(image(func([]byte) {}), offset); err == nil {
		t.Errorf("unknown payload accepted as %q", got)
	}
}

func TestPremountRefusesCompressedPayload(t *testing.T) {
	// A compressed payload can't be put on a loop device at an offset; this
	// must fail before anything is mounted.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "system", "apex"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeApex(t, filepath.Join(dir, "system", "apex", "c.apex"), manifestFor("com.android.c"), make([]byte, 4096), zip.Deflate)
	rootFd, err := openRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rootFd)
	if _, err := premountOne(rootFd, "system/apex/c.apex", filepath.Join(dir, "staging")); err == nil {
		t.Fatal("compressed payload accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "staging", "com.android.c")); !os.IsNotExist(err) {
		t.Errorf("a mount point was made for a payload that can't be mounted: %v", err)
	}
}

// writeFiles makes files (path -> size) under root, and symlinks (path ->
// target).
func writeFiles(t *testing.T, root string, files map[string]int, links map[string]string) {
	t.Helper()
	for rel, size := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range links {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
}

// TestListApexesStaysInRoot: every directory apexd scans is listed, through
// the image's own symlinks, and nothing outside the root is: a container
// could point any of them anywhere.
func TestListApexesStaysInRoot(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "rootfs")
	writeFiles(t, root, map[string]int{
		"system/apex/a.apex":            10,
		"system/apex/notes.txt":         3,
		"system/system_ext/apex/b.apex": 20,
		"vendor/apex/c.apex":            30,
	}, map[string]string{
		"system_ext":           "/system/system_ext", // as on our image
		"product":              "/system",            // product/apex is then system/apex again
		"odm":                  "../outside",         // tries to leave the root: rootfs/../outside
		"vendor/apex/l.apex":   "../../system/apex/a.apex",
		"vendor/apex/abs.apex": "/etc/hostname",
	})
	writeFiles(t, dir, map[string]int{"outside/apex/evil.apex": 40}, nil)
	files, err := listApexes(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []apexFile{{"system/apex/a.apex", 10}, {"system_ext/apex/b.apex", 20}, {"vendor/apex/c.apex", 30}}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("got %v, want %v", files, want)
	}
	// A root whose apex directories are absent lists nothing, without error.
	if files, err := listApexes(t.TempDir()); err != nil || len(files) != 0 {
		t.Errorf("empty root: %v, %v", files, err)
	}
}

// TestExtractApexTar: from a docker cp archive of a directory's contents
// (dir/.) only the directory's own regular *.apex files are written.
func TestExtractApexTar(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(hdr *tar.Header, body string) {
		hdr.Size = int64(len(body))
		if hdr.Mode == 0 {
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "./x.apex", Typeflag: tar.TypeReg}, "payload")
	add(&tar.Header{Name: "./sub/y.apex", Typeflag: tar.TypeReg}, "nested")
	add(&tar.Header{Name: "../z.apex", Typeflag: tar.TypeReg}, "climbs")
	add(&tar.Header{Name: "./sub/../../v.apex", Typeflag: tar.TypeReg}, "climbs too")
	add(&tar.Header{Name: "/etc/u.apex", Typeflag: tar.TypeReg}, "absolute")
	add(&tar.Header{Name: "./l.apex", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"}, "")
	add(&tar.Header{Name: "./h.apex", Typeflag: tar.TypeLink, Linkname: "./x.apex"}, "")
	add(&tar.Header{Name: "./d.apex", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "./.apex", Typeflag: tar.TypeReg}, "unnamed")
	add(&tar.Header{Name: "./notes.txt", Typeflag: tar.TypeReg}, "text")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "vendor", "apex")
	n, err := extractApexTar(&buf, dest)
	if err != nil || n != 1 {
		t.Fatalf("extracted %d, %v", n, err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 1 || entries[0].Name() != "x.apex" {
		t.Fatalf("dest holds %v, %v", entries, err)
	}
	info, _ := entries[0].Info()
	if info.Mode().Perm() != 0o400 {
		t.Errorf("mode %v, want root-only", info.Mode())
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "x.apex")); string(got) != "payload" {
		t.Errorf("content %q", got)
	}
}

// writeAlignedApex writes an APEX laid out as apexer and zipalign lay one out:
// the payload first, stored, its data at byte 4096, then the manifest.
func writeAlignedApex(t *testing.T, path string, manifest, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	const name = "apex_payload.img"
	// Local header (30 bytes) + name + extra = 4096. The extra is one padding
	// field (id 0xd935, as zipalign writes): 4 bytes of header, then zeros.
	pad := 4096 - 30 - len(name)
	extra := make([]byte, pad)
	binary.LittleEndian.PutUint16(extra[0:], 0xd935)
	binary.LittleEndian.PutUint16(extra[2:], uint16(pad-4))
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if w, err = zw.CreateHeader(&zip.FileHeader{Name: "apex_manifest.pb", Method: zip.Deflate}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// ext4Image makes a small ext4 image holding bin/hello.
func ext4Image(t *testing.T, mkfs, dir, hello string) []byte {
	t.Helper()
	content := filepath.Join(dir, "content")
	if err := os.MkdirAll(filepath.Join(content, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(content, "bin", "hello"), []byte(hello), 0o755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "payload.img")
	if out, err := exec.Command(mkfs, "-q", "-F", "-b", "4096", "-d", content, img, "4M").CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v\n%s", err, out)
	}
	payload, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestSetListing(t *testing.T) {
	a := setListing([]apexFile{{"system/apex/a.apex", 10}, {"vendor/apex/b.apex", 20}})
	if a != "system/apex/a.apex:10\nvendor/apex/b.apex:20\n" {
		t.Errorf("listing %q", a)
	}
	if c := setListing([]apexFile{{"system/apex/a.apex", 11}, {"vendor/apex/b.apex", 20}}); a == c {
		t.Error("a changed size kept the listing")
	}
}

// specAnnotated is a spec naming an APEX set (or none, with "").
func specAnnotated(set string) map[string]any {
	raw := map[string]any{}
	if set != "" {
		raw["annotations"] = map[string]any{apexSetAnnotation: set, "other": "x"}
	}
	return raw
}

func makeReadySet(t *testing.T, set, listing string) string {
	t.Helper()
	staging := filepath.Join(stagingRoot, set)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyMarker(staging), []byte(listing), 0o644); err != nil {
		t.Fatal(err)
	}
	return staging
}

// TestPreparedStaging: a container gets the set the node named for it, once
// that set is ready and its listing is the container's; in a user namespace
// no set is an error, outside one it is no bind.
func TestPreparedStaging(t *testing.T) {
	old := stagingRoot
	stagingRoot = t.TempDir()
	t.Cleanup(func() { stagingRoot = old })
	rootfs := t.TempDir()
	writeFiles(t, rootfs, map[string]int{"system/apex/a.apex": 10, "vendor/apex/b.apex": 20}, nil)
	listing := "system/apex/a.apex:10\nvendor/apex/b.apex:20\n"

	if _, err := preparedStaging(specAnnotated(""), rootfs, true); err == nil {
		t.Error("user namespace without a named set accepted")
	}
	if got, err := preparedStaging(specAnnotated(""), rootfs, false); err != nil || got != "" {
		t.Errorf("no user namespace, no set: %q, %v", got, err)
	}
	if _, err := preparedStaging(specAnnotated("imga"), rootfs, true); err == nil {
		t.Error("a named set that isn't ready was used")
	}
	// Two sets with the same listing, as two images whose rebuilt APEX kept
	// its size: the container gets the one named, never the other.
	a := makeReadySet(t, "imga", listing)
	b := makeReadySet(t, "imgb", listing)
	for _, userns := range []bool{true, false} {
		if got, err := preparedStaging(specAnnotated("imgb"), rootfs, userns); err != nil || got != b {
			t.Errorf("userns %v: %q, %v; want %q", userns, got, err, b)
		}
		if got, err := preparedStaging(specAnnotated("imga"), rootfs, userns); err != nil || got != a {
			t.Errorf("userns %v: %q, %v; want %q", userns, got, err, a)
		}
	}
	// A set made from other APEX files is refused.
	makeReadySet(t, "imgc", "system/apex/a.apex:10\n")
	if _, err := preparedStaging(specAnnotated("imgc"), rootfs, true); err == nil {
		t.Error("a set with another listing was used")
	}
	for _, bad := range []string{"../imga", "imga/..", "", "IMGA", "a b"} {
		raw := map[string]any{"annotations": map[string]any{apexSetAnnotation: bad}}
		if _, err := preparedStaging(raw, rootfs, true); err == nil {
			t.Errorf("set name %q accepted", bad)
		}
	}
	// An image without APEXes needs none.
	if got, err := preparedStaging(specAnnotated(""), t.TempDir(), true); err != nil || got != "" {
		t.Errorf("no APEXes: %q, %v", got, err)
	}
}

// TestPremountMountsPayload mounts real ext4 payloads through loop devices,
// as the wrapper does on a VM, from both system/apex and vendor/apex: each
// once, even when two premounts race, with its files readable, then marks
// the set ready with its listing. A set of that name is not remade from
// other files.
func TestPremountMountsPayload(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mounts a loop device: needs root")
	}
	if _, err := os.Stat("/dev/loop-control"); err != nil {
		t.Skip("no loop devices here")
	}
	mkfs, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		t.Skip("no mkfs.ext4 here")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	apexes := map[string]string{
		"system/apex/com.example.test.apex":   "com.example.test",
		"vendor/apex/com.example.vendor.apex": "com.example.vendor",
	}
	for rel, name := range apexes {
		payload := ext4Image(t, mkfs, filepath.Join(dir, name), "from "+name+"\n")
		writeAlignedApex(t, filepath.Join(source, rel), manifestFor(name), payload)
		zr, entry := openEntry(t, filepath.Join(source, rel), "apex_payload.img")
		if offset, err := entry.DataOffset(); err != nil || offset != 4096 {
			t.Fatalf("payload at %d (%v), want 4096: the test APEX is not laid out like a real one", offset, err)
		}
		zr.Close()
	}
	staging := filepath.Join(dir, "staging")
	const set = "imgtest"
	set1 := filepath.Join(staging, set)
	t.Cleanup(func() {
		for _, name := range apexes {
			target := filepath.Join(set1, name)
			for mountCount(t, target) > 0 {
				if err := unix.Unmount(target, 0); err != nil {
					t.Errorf("unmount %s: %v", target, err)
					break
				}
			}
		}
	})

	// Two at once, as when Docker creates two runtimes from one image.
	type result struct {
		names []string
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			names, err := premountApexes(source, staging, set)
			results <- result{names, err}
		}()
	}
	failed := false
	for range 2 {
		r := <-results
		if r.err == nil && !reflect.DeepEqual(r.names, []string{"com.example.test", "com.example.vendor"}) {
			r.err = fmt.Errorf("names %v", r.names)
		}
		if r.err != nil {
			t.Errorf("premount: %v", r.err)
			failed = true
		}
	}
	if failed {
		t.FailNow()
	}
	for _, name := range apexes {
		target := filepath.Join(set1, name)
		if n := mountCount(t, target); n != 1 {
			t.Fatalf("%d mounts at %s, want 1", n, target)
		}
		got, err := os.ReadFile(filepath.Join(target, "bin", "hello"))
		if err != nil || string(got) != "from "+name+"\n" {
			t.Fatalf("%s: payload file %q, %v", name, got, err)
		}
		var st unix.Statfs_t
		if err := unix.Statfs(target, &st); err != nil {
			t.Fatal(err)
		}
		if st.Flags&unix.ST_RDONLY == 0 || st.Flags&unix.ST_NODEV == 0 || st.Flags&unix.ST_NOSUID == 0 {
			t.Errorf("%s: mount flags %#x: want read-only, nodev and nosuid", name, st.Flags)
		}
	}
	files, err := listApexes(source)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(readyMarker(set1)); string(got) != setListing(files) {
		t.Errorf("ready marker %q, want the set's listing", got)
	}

	// Again, later: the existing mounts are kept, not stacked.
	if _, err := premountApexes(source, staging, set); err != nil {
		t.Fatal(err)
	}
	for _, name := range apexes {
		if n := mountCount(t, filepath.Join(set1, name)); n != 1 {
			t.Fatalf("after a second premount: %d mounts of %s, want 1", n, name)
		}
	}
	// The same name from other files is refused, before anything is mounted.
	payload := ext4Image(t, mkfs, filepath.Join(dir, "extra"), "extra\n")
	writeAlignedApex(t, filepath.Join(source, "system/apex/com.example.extra.apex"), manifestFor("com.example.extra"), payload)
	if _, err := premountApexes(source, staging, set); err == nil {
		t.Error("a ready set was remade from other files")
	}
	if exists(filepath.Join(set1, "com.example.extra")) {
		t.Error("the refused file got a mount point")
	}
	if _, err := premountApexes(source, staging, "../escape"); err == nil {
		t.Error("a set name with a path accepted")
	}
}

// TestPremountRefusesDuplicateNames: two APEXes that are one module would
// both want one mount point.
func TestPremountRefusesDuplicateNames(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("mounts a loop device: needs root")
	}
	mkfs, err := exec.LookPath("mkfs.ext4")
	if err != nil {
		t.Skip("no mkfs.ext4 here")
	}
	if _, err := os.Stat("/dev/loop-control"); err != nil {
		t.Skip("no loop devices here")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	payload := ext4Image(t, mkfs, dir, "x\n")
	writeAlignedApex(t, filepath.Join(source, "system/apex/a.apex"), manifestFor("com.example.same"), payload)
	writeAlignedApex(t, filepath.Join(source, "vendor/apex/b.apex"), manifestFor("com.example.same"), payload)
	staging := filepath.Join(dir, "staging")
	t.Cleanup(func() {
		target := filepath.Join(staging, "dup", "com.example.same")
		for mountCount(t, target) > 0 {
			if unix.Unmount(target, 0) != nil {
				break
			}
		}
	})
	if _, err := premountApexes(source, staging, "dup"); err == nil {
		t.Fatal("two APEXes with one name accepted")
	}
	if exists(readyMarker(filepath.Join(staging, "dup"))) {
		t.Error("a set with a duplicate was marked ready")
	}
}
