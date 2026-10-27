package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"virtroid/backend/internal/config"
)

const testProcDevices = `Character devices:
  1 mem
  5 /dev/tty
 10 misc
136 pts
241 binder
254 dma_heap

Block devices:
  7 loop
253 device-mapper
254 virtblk
`

// testDevRoot is a VM's /dev as the node sees it: what Android uses, and
// the VM's own disks, which a runtime must not get.
func testDevRoot(t *testing.T, loops int) string {
	t.Helper()
	root := t.TempDir()
	files := []string{"ashmem", "kmsg", "dma_heap/system", "fuse", "net/tun", "loop-control", "vda", "vdb", "dm-0", "dm-1", "mapper/control", "ttyS0"}
	for i := range loops {
		files = append(files, fmt.Sprintf("loop%d", i))
	}
	for _, name := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// createTestContainer creates a container for an image with the given
// labels and returns the HostConfig the node sent Docker.
func createTestContainer(t *testing.T, labels map[string]string, devRoot string) (map[string]any, error) {
	t.Helper()
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "shared")
	t.Setenv("NODE_RUNTIME_IMAGE", "localhost/virtroid-runtime:test")
	procDevices := filepath.Join(t.TempDir(), "devices")
	if err := os.WriteFile(procDevices, []byte(testProcDevices), 0o644); err != nil {
		t.Fatal(err)
	}
	node := &nodeAgent{
		cfg:             config.NodeConfig{DockerNetworkName: "virtroid-test", RuntimeRoot: t.TempDir()},
		runtimeDevRoot:  devRoot,
		procDevicesPath: procDevices,
	}
	var requestBody map[string]any
	node.docker = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		respond := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/images/localhost/virtroid-runtime:test/json":
			payload, _ := json.Marshal(map[string]any{"Config": map[string]any{"Labels": labels}})
			return respond(http.StatusOK, string(payload))
		case req.Method == http.MethodPost && req.URL.Path == "/containers/create":
			if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
				t.Fatalf("decode Docker create request: %v", err)
			}
			return respond(http.StatusCreated, `{}`)
		}
		t.Fatalf("unexpected Docker request %s %s", req.Method, req.URL.Path)
		return nil, nil
	})}
	err := node.createContainer(context.Background(), "virtroid-runtime-test", runtimeAssignment{
		ID: "11111111-1111-1111-1111-111111111111", WidthPx: 720, HeightPx: 1280, DensityDpi: 320,
	}, filepath.Join(t.TempDir(), "data"), 5555, sessionPersona{})
	if err != nil {
		return nil, err
	}
	hostConfig, _ := requestBody["HostConfig"].(map[string]any)
	return hostConfig, nil
}

func stringList(values any) []string {
	var out []string
	list, _ := values.([]any)
	for _, value := range list {
		out = append(out, fmt.Sprint(value))
	}
	return out
}

func TestStockRuntimeStaysPrivileged(t *testing.T) {
	hostConfig, err := createTestContainer(t, nil, testDevRoot(t, 64))
	if err != nil {
		t.Fatal(err)
	}
	if hostConfig["Privileged"] != true || hostConfig["CapDrop"] != nil || hostConfig["SecurityOpt"] != nil {
		t.Fatalf("stock ReDroid's container: %v", hostConfig)
	}
}

func TestOurRuntimeRunsUnprivileged(t *testing.T) {
	hostConfig, err := createTestContainer(t, map[string]string{runtimeProfileLabel: runtimeProfileUnprivileged}, testDevRoot(t, 64))
	if err != nil {
		t.Fatal(err)
	}
	if privileged, _ := hostConfig["Privileged"].(bool); privileged {
		t.Fatal("our image's runtime is privileged")
	}
	if drop := stringList(hostConfig["CapDrop"]); !slices.Equal(drop, []string{"ALL"}) {
		t.Fatalf("CapDrop = %v", drop)
	}
	capabilities := stringList(hostConfig["CapAdd"])
	for _, needed := range []string{"SYS_ADMIN", "NET_ADMIN", "DAC_READ_SEARCH", "SYS_BOOT"} {
		if !slices.Contains(capabilities, needed) {
			t.Fatalf("no %s in %v", needed, capabilities)
		}
	}
	for _, withheld := range []string{"SYS_MODULE", "SYS_RAWIO", "SYS_TIME", "SYSLOG", "AUDIT_CONTROL", "MAC_ADMIN", "MAC_OVERRIDE", "ALL"} {
		if slices.Contains(capabilities, withheld) {
			t.Fatalf("%s granted: %v", withheld, capabilities)
		}
	}

	devices, _ := hostConfig["Devices"].([]any)
	loops := 0
	for _, entry := range devices {
		device := entry.(map[string]any)
		path := fmt.Sprint(device["PathOnHost"])
		if path != device["PathInContainer"] {
			t.Fatalf("device %v", device)
		}
		switch {
		case strings.HasPrefix(path, "/dev/loop") && path != "/dev/loop-control":
			loops++
		case path == "/dev/kmsg":
			if device["CgroupPermissions"] != "w" {
				t.Fatalf("the kernel log is readable: %v", device)
			}
		case slices.Contains([]string{"/dev/ashmem", "/dev/dma_heap/system", "/dev/fuse", "/dev/net/tun", "/dev/loop-control"}, path):
		default:
			t.Fatalf("device %s given to a runtime", path)
		}
	}
	if loops != 64 || len(devices) != 64+6 {
		t.Fatalf("%d loops of %d devices", loops, len(devices))
	}
	if rules := stringList(hostConfig["DeviceCgroupRules"]); !slices.Equal(rules, []string{"c 241:* rwm"}) {
		t.Fatalf("DeviceCgroupRules = %v (binder's major is 241)", rules)
	}

	options := stringList(hostConfig["SecurityOpt"])
	if !slices.Contains(options, "writable-cgroups=true") || len(options) != 2 || !strings.HasPrefix(options[0], "seccomp=") {
		t.Fatalf("SecurityOpt = %v", options)
	}
	var profile struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names  []string `json:"names"`
			Action string   `json:"action"`
			Args   []struct {
				Index    int    `json:"index"`
				Value    uint64 `json:"value"`
				ValueTwo uint64 `json:"valueTwo"`
				Op       string `json:"op"`
			} `json:"args"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(options[0], "seccomp=")), &profile); err != nil {
		t.Fatalf("seccomp profile: %v", err)
	}
	denied := map[string]bool{}
	for _, rule := range profile.Syscalls {
		if rule.Action != "SCMP_ACT_ERRNO" {
			t.Fatalf("rule %+v", rule)
		}
		for _, name := range rule.Names {
			if name == "ioctl" {
				if len(rule.Args) != 1 || rule.Args[0].Index != 1 || rule.Args[0].ValueTwo != 0x8004587D ||
					rule.Args[0].Value != 0xFFFFFFFF || rule.Args[0].Op != "SCMP_CMP_MASKED_EQ" {
					t.Fatalf("ioctl rule %+v must refuse only the filesystem shutdown", rule.Args)
				}
				name = "ioctl(FS_IOC_SHUTDOWN)"
			} else if len(rule.Args) != 0 {
				t.Fatalf("rule %+v", rule)
			}
			denied[name] = true
		}
	}
	for _, name := range []string{"ioctl(FS_IOC_SHUTDOWN)", "open_by_handle_at", "kexec_load", "kexec_file_load", "init_module", "finit_module", "clock_settime", "settimeofday", "swapon", "iopl"} {
		if !denied[name] {
			t.Fatalf("%s allowed", name)
		}
	}
	if profile.DefaultAction != "SCMP_ACT_ALLOW" || denied["ioctl"] || denied["mount"] || denied["bpf"] || denied["unshare"] {
		t.Fatalf("the profile refuses what Android needs: %+v", denied)
	}
}

func TestUnprivilegedRuntimeNeedsItsDevices(t *testing.T) {
	labels := map[string]string{runtimeProfileLabel: runtimeProfileUnprivileged}
	if _, err := createTestContainer(t, labels, testDevRoot(t, 39)); err == nil || !strings.Contains(err.Error(), "loop devices") {
		t.Fatalf("39 loop devices: %v", err)
	}
	devRoot := testDevRoot(t, 64)
	if err := os.Remove(filepath.Join(devRoot, "ashmem")); err != nil {
		t.Fatal(err)
	}
	if _, err := createTestContainer(t, labels, devRoot); err == nil || !strings.Contains(err.Error(), "ashmem") {
		t.Fatalf("no ashmem: %v", err)
	}
	// The DMA heap and tun are optional: a VM without them still starts.
	devRoot = testDevRoot(t, 64)
	_ = os.RemoveAll(filepath.Join(devRoot, "dma_heap"))
	if _, err := createTestContainer(t, labels, devRoot); err != nil {
		t.Fatalf("no DMA heap: %v", err)
	}
	if _, err := characterDeviceMajor("Character devices:\n  1 mem\n\nBlock devices:\n253 binder\n", "binder"); err == nil {
		t.Fatal("a block device taken for binder")
	}
}

// The boot test starts its own runtimes as the node does: the same
// capabilities, seccomp profile and devices.
func TestBootTestConfinesRuntimesAsTheNodeDoes(t *testing.T) {
	script, err := os.ReadFile("../../../deploy/aleph/test/boot-test.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	_, rest, ok := strings.Cut(text, `runtime_caps="`)
	capabilities, _, _ := strings.Cut(rest, `"`)
	if !ok || !slices.Equal(strings.Fields(capabilities), unprivilegedRuntimeCapabilities) {
		t.Fatalf("boot-test.sh's capabilities %q", capabilities)
	}
	_, rest, ok = strings.Cut(text, "<<'SECCOMP'\n")
	profile, _, _ := strings.Cut(rest, "\nSECCOMP\n")
	var theirs, ours any
	if err := json.Unmarshal([]byte(profile), &theirs); !ok || err != nil {
		t.Fatalf("boot-test.sh's seccomp profile: %v", err)
	}
	_ = json.Unmarshal([]byte(runtimeSeccompProfile()), &ours)
	if fmt.Sprint(theirs) != fmt.Sprint(ours) {
		t.Fatalf("boot-test.sh's seccomp profile differs from the node's:\n%s\n%s", profile, runtimeSeccompProfile())
	}
	for _, device := range unprivilegedRuntimeDevices {
		want := device.path
		if device.permissions != "rwm" {
			want = device.path + ":" + device.path + ":" + device.permissions
		}
		if !strings.Contains(text, want) {
			t.Fatalf("boot-test.sh gives no %s", want)
		}
	}
	for _, option := range []string{"--cap-drop ALL", "writable-cgroups=true", `c \$binder:* rwm`, runtimeProfileUnprivileged, runtimeProfileLabel} {
		if !strings.Contains(text, option) {
			t.Fatalf("boot-test.sh has no %s", option)
		}
	}
}
