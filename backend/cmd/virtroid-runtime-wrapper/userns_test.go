//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain lets the test binary stand in for the wrapper as its own
// "userns-hold" child (usernsFd re-executes itself).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "userns-hold" {
		holdUserns()
	}
	os.Exit(m.Run())
}

func TestUsernsRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subuid")
	write := func(text string) {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("dockremap:100000:65536\nvirtroid-runtime:1000000:1000000\n")
	if got, err := usernsRange(path); err != nil || got != (idMap{0, 1000000, 1000000}) {
		t.Errorf("got %+v, %v", got, err)
	}
	for _, bad := range []string{"", "dockremap:100000:65536\n", "virtroid-runtime:0:1000\n", "virtroid-runtime:1000000:0\n", "virtroid-runtime:4294967295:2\n", "virtroid-runtime:x:1\n"} {
		write(bad)
		if got, err := usernsRange(path); err == nil {
			t.Errorf("%q accepted as %+v", bad, got)
		}
	}
}

func TestAddUserns(t *testing.T) {
	raw, err := decodeSpec([]byte(`{"linux":{"namespaces":[{"type":"pid"},{"type":"mount"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := idMap{0, 1000000, 1000000}, idMap{0, 2000000, 500000}
	if err := addUserns(raw, uid, gid); err != nil {
		t.Fatal(err)
	}
	linux := raw["linux"].(map[string]any)
	if n := len(linux["namespaces"].([]any)); n != 3 {
		t.Errorf("%d namespaces", n)
	}
	m := linux["gidMappings"].([]any)[0].(map[string]any)
	if m["hostID"] != uint32(2000000) || m["size"] != uint32(500000) || m["containerID"] != uint32(0) {
		t.Errorf("gid mapping %v", m)
	}
	// Twice, or on a spec that already has one, is refused.
	if err := addUserns(raw, uid, gid); err == nil {
		t.Error("a second user namespace accepted")
	}
	raw2, _ := decodeSpec([]byte(`{"linux":{"uidMappings":[{"containerID":0,"hostID":5,"size":1}]}}`))
	if err := addUserns(raw2, uid, gid); err == nil {
		t.Error("a spec with mappings accepted")
	}
	if !wantsUserns(map[string]any{"annotations": map[string]any{usernsAnnotation: "1"}}) || wantsUserns(map[string]any{}) {
		t.Error("wantsUserns")
	}
}

// TestFitSysctls: Docker's ping_group_range is cut to the groups the
// namespace maps, which the kernel requires.
func TestFitSysctls(t *testing.T) {
	m := idMap{0, 1000000, 1000000}
	for value, want := range map[string]string{
		"0 2147483647":      "0 999999",
		"100 200":           "100 200",
		"1 0":               "1 0",
		"2000000 3000000":   "1 0",
		"999999 4294967295": "999999 999999",
	} {
		raw := map[string]any{"linux": map[string]any{"sysctl": map[string]any{"net.ipv4.ping_group_range": value, "net.ipv4.ip_unprivileged_port_start": "0"}}}
		if err := fitSysctls(raw, m); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		sysctl := raw["linux"].(map[string]any)["sysctl"].(map[string]any)
		if sysctl["net.ipv4.ping_group_range"] != want || sysctl["net.ipv4.ip_unprivileged_port_start"] != "0" {
			t.Errorf("%q: %v, want %q", value, sysctl, want)
		}
	}
	for _, bad := range []string{"", "0", "a b", "0 1 2", "-1 5"} {
		raw := map[string]any{"linux": map[string]any{"sysctl": map[string]any{"net.ipv4.ping_group_range": bad}}}
		if err := fitSysctls(raw, m); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := fitSysctls(map[string]any{}, m); err != nil {
		t.Error(err)
	}
}
