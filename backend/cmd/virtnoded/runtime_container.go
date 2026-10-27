package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// How a runtime's container is confined. Stock ReDroid needs --privileged,
// which hands Android every device of the VM (its disks and the data volume
// all runtimes share among them), a writable sysfs and every capability. Our
// own Android image (deploy/aosp) boots without it and says so with a label;
// it then gets only the capabilities its services ask for, only the devices
// Android uses, its own cgroup subtree, and a seccomp profile against the
// calls that reach past the container (deploy/aosp/README.md, hardening).
const (
	runtimeProfileLabel        = "io.virtroid.container-profile"
	runtimeProfileUnprivileged = "unprivileged-1"
	// apexd puts each of the image's 40 APEXes on a loop device.
	runtimeMinLoopDevices = 40
)

// unprivilegedRuntimeCapabilities are what Android's init and its services
// need: the union of the capabilities the image's services declare (init
// gives each its own), and init's own for mounting, devices and cgroups.
// Left out, among others: SYS_MODULE, SYS_RAWIO, SYS_TIME (the VM's clock),
// MAC_ADMIN and MAC_OVERRIDE, SYSLOG (the VM's kernel log, every runtime's)
// and AUDIT_CONTROL, which logd no longer asks for (patches/system/logging).
var unprivilegedRuntimeCapabilities = []string{
	"CHOWN", "DAC_OVERRIDE", "DAC_READ_SEARCH", "FOWNER", "FSETID", "KILL", "SETGID", "SETUID",
	"SETPCAP", "SETFCAP", "NET_BIND_SERVICE", "NET_RAW", "NET_ADMIN", "SYS_CHROOT", "MKNOD",
	"AUDIT_WRITE", "SYS_ADMIN", "SYS_RESOURCE", "SYS_NICE", "SYS_PTRACE", "IPC_LOCK",
	// Android's reboot ends the container: in a PID namespace reboot(2) only
	// stops its init.
	"SYS_BOOT",
	"WAKE_ALARM", "BLOCK_SUSPEND", "BPF", "PERFMON",
}

// The devices Android opens, from a privileged runtime's open files: the rest
// of the VM's (its disks, device-mapper, serial lines) stay out of reach.
// binderfs, which Android mounts itself, needs only a cgroup rule for the
// binder driver's major.
var unprivilegedRuntimeDevices = []struct {
	path        string
	permissions string
	optional    bool
}{
	{"/dev/ashmem", "rwm", false},
	// Android's init and logd write their logs here; reading it would be the
	// VM's kernel log.
	{"/dev/kmsg", "w", false},
	// Media codecs' buffers.
	{"/dev/dma_heap/system", "rwm", true},
	// Android's emulated storage.
	{"/dev/fuse", "rwm", false},
	// Apps' VPNs.
	{"/dev/net/tun", "rwm", true},
	{"/dev/loop-control", "rwm", false},
}

const (
	// _IOR('X', 125, __u32): EXT4_IOC_SHUTDOWN and F2FS_IOC_SHUTDOWN.
	fsIocShutdown = 0x8004587D
	seccompEPERM  = 1
)

// runtimeContainerProfile is the confinement the image asks for; images
// without the label get none.
func (n *nodeAgent) runtimeContainerProfile(ctx context.Context, image string) (string, error) {
	status, body, err := n.doDockerRequest(ctx, http.MethodGet, "http://docker/images/"+url.PathEscape(image)+"/json", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("docker image inspect failed: status=%d body=%s", status, truncateForLog(string(body)))
	}
	var inspect struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(body, &inspect); err != nil {
		return "", fmt.Errorf("decode image inspect: %w", err)
	}
	return inspect.Config.Labels[runtimeProfileLabel], nil
}

func truncateForLog(value string) string {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

// confineRuntime sets the container's privileges for its image's profile,
// from the VM's /dev and /proc/devices.
func (n *nodeAgent) confineRuntime(hostConfig map[string]any, profile string) error {
	devRoot, procDevicesPath := n.runtimeDevRoot, n.procDevicesPath
	if devRoot == "" {
		devRoot = "/dev"
	}
	if procDevicesPath == "" {
		procDevicesPath = "/proc/devices"
	}
	procDevices, err := os.ReadFile(procDevicesPath)
	if err != nil && profile == runtimeProfileUnprivileged {
		return fmt.Errorf("unprivileged runtime: %w", err)
	}
	return confineRuntimeHost(hostConfig, profile, devRoot, string(procDevices))
}

func confineRuntimeHost(hostConfig map[string]any, profile, devRoot, procDevices string) error {
	if profile != runtimeProfileUnprivileged {
		hostConfig["Privileged"] = true
		return nil
	}
	devices, rules, err := unprivilegedRuntimeDeviceConfig(devRoot, procDevices)
	if err != nil {
		return fmt.Errorf("unprivileged runtime: %w", err)
	}
	hostConfig["CapDrop"] = []string{"ALL"}
	hostConfig["CapAdd"] = unprivilegedRuntimeCapabilities
	hostConfig["Devices"] = devices
	hostConfig["DeviceCgroupRules"] = rules
	hostConfig["SecurityOpt"] = []string{
		"seccomp=" + runtimeSeccompProfile(),
		// The container's own cgroup subtree, which Android's init splits
		// into its services' and apps' groups.
		"writable-cgroups=true",
	}
	return nil
}

var loopDevicePattern = regexp.MustCompile(`^loop[0-9]+$`)

// unprivilegedRuntimeDeviceConfig lists the devices (from devRoot, the VM's
// /dev) and the binder cgroup rule (its major from procDevices, the VM's
// /proc/devices). Every loop device goes in: apexd takes whichever are free,
// so a runtime can see the ones other runtimes' APEXes sit on, which hold
// only the image's own system files.
func unprivilegedRuntimeDeviceConfig(devRoot, procDevices string) ([]map[string]string, []string, error) {
	var devices []map[string]string
	add := func(path, permissions string) {
		devices = append(devices, map[string]string{
			"PathOnHost": path, "PathInContainer": path, "CgroupPermissions": permissions,
		})
	}
	for _, device := range unprivilegedRuntimeDevices {
		if _, err := os.Stat(filepath.Join(devRoot, strings.TrimPrefix(device.path, "/dev/"))); err != nil {
			if device.optional && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, nil, fmt.Errorf("device %s: %w", device.path, err)
		}
		add(device.path, device.permissions)
	}
	entries, err := os.ReadDir(devRoot)
	if err != nil {
		return nil, nil, err
	}
	var loops []int
	for _, entry := range entries {
		if loopDevicePattern.MatchString(entry.Name()) {
			number, _ := strconv.Atoi(strings.TrimPrefix(entry.Name(), "loop"))
			loops = append(loops, number)
		}
	}
	if len(loops) < runtimeMinLoopDevices {
		return nil, nil, fmt.Errorf("%d loop devices, Android needs %d", len(loops), runtimeMinLoopDevices)
	}
	sort.Ints(loops)
	for _, number := range loops {
		add(fmt.Sprintf("/dev/loop%d", number), "rwm")
	}
	major, err := characterDeviceMajor(procDevices, "binder")
	if err != nil {
		return nil, nil, err
	}
	return devices, []string{fmt.Sprintf("c %d:* rwm", major)}, nil
}

// characterDeviceMajor is a driver's major number from /proc/devices, which
// the kernel assigns at boot.
func characterDeviceMajor(procDevices, driver string) (int, error) {
	inCharacter := false
	for _, line := range strings.Split(procDevices, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "Character devices:":
			inCharacter = true
		case line == "Block devices:":
			inCharacter = false
		case inCharacter:
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == driver {
				return strconv.Atoi(fields[0])
			}
		}
	}
	return 0, fmt.Errorf("no %s driver in /proc/devices", driver)
}

// runtimeSeccompProfile allows what the capabilities allow, except calls
// that reach past the container whatever its capabilities: shutting down a
// filesystem (/data is the VM's data volume), opening files by handle
// (anywhere on that volume, other runtimes' among them), loading kernels or
// modules, setting the VM's clock, swap and process accounting, and raw I/O.
func runtimeSeccompProfile() string {
	deny := func(names ...string) map[string]any {
		return map[string]any{"names": names, "action": "SCMP_ACT_ERRNO", "errnoRet": seccompEPERM}
	}
	shutdown := deny("ioctl")
	// The command is an int, sign-extended to 64 bits by some callers: only
	// its low 32 bits count.
	shutdown["args"] = []map[string]any{{"index": 1, "value": uint64(0xFFFFFFFF), "valueTwo": uint64(fsIocShutdown), "op": "SCMP_CMP_MASKED_EQ"}}
	profile := map[string]any{
		"defaultAction": "SCMP_ACT_ALLOW",
		"architectures": []string{"SCMP_ARCH_X86_64", "SCMP_ARCH_X86", "SCMP_ARCH_X32"},
		"syscalls": []map[string]any{
			shutdown,
			deny("open_by_handle_at"),
			deny("kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module"),
			deny("settimeofday", "clock_settime", "clock_settime64", "clock_adjtime", "clock_adjtime64", "stime"),
			deny("swapon", "swapoff", "acct", "iopl", "ioperm", "syslog"),
		},
	}
	encoded, _ := json.Marshal(profile)
	return string(encoded)
}
