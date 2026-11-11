package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runtimes whose image says userns-1 run in a user namespace: dockerd starts
// them through virtroid-runtime-wrapper (--add-runtime), which, asked by
// usernsAnnotation, puts them in one with their root an unprivileged uid on
// the VM (virtroid-runtime's range in /etc/subuid) and their rootfs id-mapped
// to match, and sets up what Docker doesn't there. Docker itself doesn't know
// of the namespace, so its image store keeps the image's real owners for
// everything else that runs on it. Android can't do two things in a user namespace that
// it does on a phone, so the VM does them, with the wrapper:
//   - its APEX payloads, which only a host root may mount: the wrapper's
//     premount-image mounts them once per image, and each container names its
//     image's set (apexSetAnnotation);
//   - its network BPF programs, which only a host root may load and attach:
//     before each start the node mounts a bpffs for the runtime, has the
//     image's own loaders fill it in a short loading pass outside any user
//     namespace (a container of the same image, no network and no data), and
//     copies the pins over (bpf-copy); the wrapper binds it in and attaches
//     netd's programs to the runtime's cgroup (bpffsAnnotation).
// The runtime's root can then change none of the VM kernel's settings.

const (
	// The name dockerd knows the wrapper by (deploy/aleph/nix/vm-init.sh).
	usernsRuntimeName = "virtroid-userns"

	usernsAnnotation  = "io.virtroid.userns"
	apexSetAnnotation = "io.virtroid.apex-set"
	bpffsAnnotation   = "io.virtroid.bpffs"
	runtimeBpfRoot    = "/run/virtroid/bpf"

	// The loading pass: the image boots far enough for its loaders to finish
	// (a few seconds) and is removed.
	bpfLoadTimeout      = 90 * time.Second
	bpfLoaderNameSuffix = "-bpfload"
)

var wrapperNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// usernsRuntimeUlimits are the container's limits, the highest any of its
// services asks for: in a user namespace they can't raise their own. The
// zygote gives apps stock Android's (frameworks/base 0004).
func usernsRuntimeUlimits() []map[string]any {
	return []map[string]any{
		{"Name": "memlock", "Soft": -1, "Hard": -1}, // keystore2: unlimited, netd: 1 GiB
		{"Name": "nice", "Soft": 40, "Hard": 40},    // init: setrlimit nice 40 40
		{"Name": "rtprio", "Soft": 10, "Hard": 10},  // audio and camera services: 10
	}
}

func runtimeWrapperPath() string {
	if path := os.Getenv("NODE_RUNTIME_WRAPPER"); path != "" {
		return path
	}
	return "virtroid-runtime-wrapper"
}

// runWrapper runs one of the wrapper's node commands and returns its stdout.
func runWrapper(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, runtimeWrapperPath(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("virtroid-runtime-wrapper %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// premountRuntimeImage prepares image's APEX set and returns its name.
func premountRuntimeImage(ctx context.Context, image string) (string, error) {
	out, err := runWrapper(ctx, "premount-image", image)
	if err != nil {
		return "", err
	}
	lines := strings.Split(out, "\n")
	set := strings.TrimSpace(lines[len(lines)-1])
	if !wrapperNamePattern.MatchString(set) {
		return "", fmt.Errorf("premount-image %s: unexpected set name %q", image, set)
	}
	return set, nil
}

// usernsAnnotations asks the wrapper to put a new runtime container in a user
// namespace and names its image's APEX set and its bpffs (named after the
// container).
func usernsAnnotations(set, containerName string) map[string]string {
	return map[string]string{usernsAnnotation: "1", apexSetAnnotation: set, bpffsAnnotation: containerName}
}

type usernsContainerInfo struct {
	Config struct {
		Image string   `json:"Image"`
		Cmd   []string `json:"Cmd"`
	} `json:"Config"`
	HostConfig struct {
		Runtime     string            `json:"Runtime"`
		Annotations map[string]string `json:"Annotations"`
	} `json:"HostConfig"`
	State struct {
		Running bool `json:"Running"`
		Pid     int  `json:"Pid"`
	} `json:"State"`
}

func (n *nodeAgent) inspectUsernsInfo(ctx context.Context, containerName string) (usernsContainerInfo, error) {
	var info usernsContainerInfo
	status, body, err := n.doDockerRequest(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(containerName)+"/json", nil)
	if err != nil {
		return info, err
	}
	if status == http.StatusNotFound {
		return info, errContainerNotFound
	}
	if status >= 300 {
		return info, fmt.Errorf("docker inspect %s: status=%d body=%s", containerName, status, truncateBody(body))
	}
	return info, json.Unmarshal(body, &info)
}

func truncateBody(body []byte) string {
	if len(body) > 2048 {
		body = body[:2048]
	}
	return string(body)
}

// loadRuntimeBpf fills a runtime container's bpffs before it starts, if it
// runs in a user namespace; otherwise it does nothing.
func (n *nodeAgent) loadRuntimeBpf(ctx context.Context, containerName string) (err error) {
	info, err := n.inspectUsernsInfo(ctx, containerName)
	if err != nil {
		return err
	}
	if info.HostConfig.Runtime != usernsRuntimeName {
		return nil
	}
	name := info.HostConfig.Annotations[bpffsAnnotation]
	set := info.HostConfig.Annotations[apexSetAnnotation]
	if name == "" || set == "" {
		return fmt.Errorf("container %s runs in a user namespace but names no bpffs or APEX set", containerName)
	}
	if _, err := runWrapper(ctx, "bpf-mount", name); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			n.releaseRuntimeBpf(context.WithoutCancel(ctx), name)
		}
	}()

	loader := name + bpfLoaderNameSuffix
	if err := n.stopAndRemoveContainer(ctx, loader); err != nil && !errors.Is(err, errContainerNotFound) {
		return fmt.Errorf("remove an earlier loading pass: %w", err)
	}
	body, err := n.bpfLoaderBody(info.Config.Image, info.Config.Cmd, set, name)
	if err != nil {
		return err
	}
	status, resp, err := n.doDockerRequest(ctx, http.MethodPost, "http://docker/containers/create?name="+url.QueryEscape(loader), body)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("create the BPF loading pass: status=%d body=%s", status, truncateBody(resp))
	}
	defer func() {
		if rmErr := n.stopAndRemoveContainer(context.WithoutCancel(ctx), loader); rmErr != nil && !errors.Is(rmErr, errContainerNotFound) && err == nil {
			err = fmt.Errorf("remove the BPF loading pass: %w", rmErr)
		}
	}()
	if err := n.startContainer(ctx, loader); err != nil {
		return fmt.Errorf("start the BPF loading pass: %w", err)
	}
	deadline := time.Now().Add(bpfLoadTimeout)
	for {
		state, err := n.inspectUsernsInfo(ctx, loader)
		if err != nil {
			return err
		}
		if !state.State.Running {
			return errors.New("the BPF loading pass stopped before its loaders finished")
		}
		out, _ := n.execInContainerCapture(ctx, loader, "", nil, []string{"/system/bin/getprop", "bpf.progs_loaded"})
		if strings.TrimSpace(out) == "1" {
			if _, err := runWrapper(ctx, "bpf-copy", filepath.Join("/proc", strconv.Itoa(state.State.Pid), "root/sys/fs/bpf"), name); err != nil {
				return err
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the BPF loading pass did not finish in %s", bpfLoadTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// bpfLoaderBody is the loading pass's container: the runtime's image and
// boot arguments, outside any user namespace (only it may load programs) and
// otherwise confined as an unprivileged runtime, with no network, no data and
// no viewer, so only the image's own files go into what it loads.
func (n *nodeAgent) bpfLoaderBody(image string, cmd []string, set, name string) ([]byte, error) {
	hostConfig := map[string]any{
		"NetworkMode": "none",
		"Memory":      int64(2 << 30),
		"MemorySwap":  int64(2 << 30),
		"PidsLimit":   int64(4096),
	}
	if err := n.confineRuntime(hostConfig, runtimeProfileUnprivileged); err != nil {
		return nil, err
	}
	// Its APEXes too come from the VM's premounted set, through the wrapper.
	hostConfig["Runtime"] = usernsRuntimeName
	hostConfig["Annotations"] = map[string]string{apexSetAnnotation: set}
	return json.Marshal(map[string]any{
		"Image": image,
		"Cmd":   cmd,
		"Labels": map[string]string{
			"io.virtroid.managed":    "true",
			"io.virtroid.bpf-loader": name,
		},
		"HostConfig": hostConfig,
	})
}

// releaseRuntimeBpf drops a runtime's bpffs, if it has one. Its programs and
// maps go once no container holds them.
func (n *nodeAgent) releaseRuntimeBpf(ctx context.Context, name string) {
	if !wrapperNamePattern.MatchString(name) {
		return
	}
	if _, err := os.Stat(filepath.Join(runtimeBpfRoot, name)); errors.Is(err, os.ErrNotExist) {
		return
	}
	if _, err := runWrapper(ctx, "bpf-release", name); err != nil {
		fmt.Fprintf(os.Stderr, "virtnoded: release the BPF filesystem of %s: %v\n", name, err)
	}
}
