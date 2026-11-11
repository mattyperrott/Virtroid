package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fakeWrapper stands in for virtroid-runtime-wrapper: premount-image prints
// set, and every call is recorded in the returned file.
func fakeWrapper(t *testing.T, set string) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + calls + "\n[ \"$1\" = premount-image ] && echo " + set + "\nexit 0\n"
	path := filepath.Join(dir, "virtroid-runtime-wrapper")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NODE_RUNTIME_WRAPPER", path)
	return calls
}

const testImageSet = "4cbdbb634be1fdafeef3dfc03fdc241c4a34b8fc9baf07b11ea98bc24927e7db"

// TestUsernsRuntimeContainer: an image that boots in a user namespace runs
// through the wrapper, which is asked for the namespace, with its APEX set
// and bpffs named, the limits its services need and no loop devices, and is
// otherwise confined as an unprivileged runtime.
func TestUsernsRuntimeContainer(t *testing.T) {
	calls := fakeWrapper(t, testImageSet)
	for _, loops := range []int{64, 0} {
		hostConfig, err := createTestContainer(t, map[string]string{runtimeProfileLabel: runtimeProfileUserns}, testDevRoot(t, loops))
		if err != nil {
			t.Fatalf("%d loop devices: %v", loops, err)
		}
		if hostConfig["Runtime"] != usernsRuntimeName || hostConfig["Privileged"] != nil {
			t.Errorf("runtime %v, privileged %v", hostConfig["Runtime"], hostConfig["Privileged"])
		}
		annotations, _ := hostConfig["Annotations"].(map[string]any)
		if annotations[usernsAnnotation] != "1" || annotations[apexSetAnnotation] != testImageSet || annotations[bpffsAnnotation] != "virtroid-runtime-test" {
			t.Errorf("annotations %v", annotations)
		}
		if hostConfig["UsernsMode"] != nil {
			t.Errorf("Docker was asked for a user namespace (%v): the wrapper makes it", hostConfig["UsernsMode"])
		}
		if got := stringList(hostConfig["SecurityOpt"]); len(got) != 2 || !strings.HasPrefix(got[0], "seccomp=") || got[1] != "writable-cgroups=true" {
			t.Errorf("security options %v", got)
		}
		if got := stringList(hostConfig["CapAdd"]); !slices.Equal(got, unprivilegedRuntimeCapabilities) {
			t.Errorf("capabilities %v", got)
		}
		devices, _ := hostConfig["Devices"].([]any)
		for _, d := range devices {
			path := d.(map[string]any)["PathInContainer"].(string)
			if strings.HasPrefix(path, "/dev/loop") {
				t.Errorf("loop device %s given to a runtime in a user namespace", path)
			}
		}
		if len(devices) == 0 {
			t.Error("no devices")
		}
		var ulimits []map[string]any
		raw, _ := json.Marshal(hostConfig["Ulimits"])
		_ = json.Unmarshal(raw, &ulimits)
		want := map[string][2]float64{"memlock": {-1, -1}, "nice": {40, 40}, "rtprio": {10, 10}}
		if len(ulimits) != len(want) {
			t.Errorf("ulimits %v", ulimits)
		}
		for _, u := range ulimits {
			w, ok := want[u["Name"].(string)]
			if !ok || u["Soft"] != w[0] || u["Hard"] != w[1] {
				t.Errorf("ulimit %v", u)
			}
		}
	}
	data, _ := os.ReadFile(calls)
	if !strings.Contains(string(data), "premount-image localhost/virtroid-runtime:test") {
		t.Errorf("the image's APEX set was not prepared: %q", data)
	}
}

// TestUnprivilegedRuntimeKeepsItsLoops: the existing profile is unchanged.
func TestUnprivilegedRuntimeKeepsItsLoops(t *testing.T) {
	hostConfig, err := createTestContainer(t, map[string]string{runtimeProfileLabel: runtimeProfileUnprivileged}, testDevRoot(t, 64))
	if err != nil {
		t.Fatal(err)
	}
	if hostConfig["Runtime"] != nil || hostConfig["Annotations"] != nil || hostConfig["Ulimits"] != nil {
		t.Errorf("runtime %v, annotations %v, ulimits %v", hostConfig["Runtime"], hostConfig["Annotations"], hostConfig["Ulimits"])
	}
	if !slices.Contains(stringList(hostConfig["SecurityOpt"]), "writable-cgroups=true") {
		t.Error("writable-cgroups dropped")
	}
	loops := 0
	devices, _ := hostConfig["Devices"].([]any)
	for _, d := range devices {
		if strings.HasPrefix(d.(map[string]any)["PathInContainer"].(string), "/dev/loop") {
			loops++
		}
	}
	if loops != 65 { // 64 loop devices and loop-control
		t.Errorf("%d loop devices, want 65", loops)
	}
}

// TestBpfLoaderBody: the loading pass is the runtime's image and arguments,
// outside any user namespace but otherwise unprivileged, with no network, no
// data, no ports and no bpffs of the runtime's: only the image goes in.
func TestBpfLoaderBody(t *testing.T) {
	procDevices := filepath.Join(t.TempDir(), "devices")
	if err := os.WriteFile(procDevices, []byte(testProcDevices), 0o644); err != nil {
		t.Fatal(err)
	}
	node := &nodeAgent{runtimeDevRoot: testDevRoot(t, 64), procDevicesPath: procDevices}
	cmd := []string{"androidboot.use_memfd=1", "androidboot.redroid_fps=30"}
	payload, err := node.bpfLoaderBody("localhost/virtroid-runtime:test", cmd, testImageSet, "virtroid-runtime-test")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	if body["Image"] != "localhost/virtroid-runtime:test" || !reflect.DeepEqual(stringList(body["Cmd"]), cmd) {
		t.Errorf("image %v cmd %v", body["Image"], body["Cmd"])
	}
	hostConfig := body["HostConfig"].(map[string]any)
	if hostConfig["NetworkMode"] != "none" || hostConfig["Runtime"] != usernsRuntimeName {
		t.Errorf("network %v runtime %v", hostConfig["NetworkMode"], hostConfig["Runtime"])
	}
	if hostConfig["Privileged"] != nil || !slices.Equal(stringList(hostConfig["CapDrop"]), []string{"ALL"}) {
		t.Errorf("privileged %v capdrop %v", hostConfig["Privileged"], hostConfig["CapDrop"])
	}
	if !reflect.DeepEqual(hostConfig["Annotations"], map[string]any{apexSetAnnotation: testImageSet}) {
		t.Errorf("annotations %v: only the APEX set", hostConfig["Annotations"])
	}
	for _, key := range []string{"Binds", "Mounts", "PortBindings"} {
		if hostConfig[key] != nil {
			t.Errorf("the loading pass has %s: %v", key, hostConfig[key])
		}
	}
}
