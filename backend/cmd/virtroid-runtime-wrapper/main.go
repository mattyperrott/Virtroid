//go:build linux

// Command virtroid-runtime-wrapper is an OCI runtime that Docker calls in place
// of runc for Virtroid's Android runtimes. When the node asks for it (the
// io.virtroid.userns annotation) it puts the container in a user namespace of
// its own, so a compromised runtime's root is an unprivileged uid on the VM
// and the kernel itself, not only seccomp and dropped capabilities, refuses
// privileged operations (userns.go). Docker knows nothing of the namespace and
// sets up none of what such a container needs, so on "create" this wrapper
// rewrites the bundle's config.json and prepares the rest: it puts the
// container's rootfs on id-mapped layers (rootfs.go), makes the cgroup2 mount
// writable and the container's cgroup delegable (Android's init splits it into
// per-service and per-app groups), bind-mounts the image's premounted APEX
// payloads in (apexd then binds them instead of mounting loop devices, which a
// user namespace forbids; see the system/apex patches and premount.go), gives
// the container device nodes of its own (devices.go), and attaches netd's BPF
// programs, which the VM loaded, to its cgroup (bpf.go). Every other verb is
// passed straight through to runc.
//
// The node runs "virtroid-runtime-wrapper premount-image <image>" when it
// loads an image, before any runtime of it starts: that copies the image's
// APEX files out of it and mounts their payloads, once per image.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "premount-image":
			// The node, when it loads an image: virtroid-runtime-wrapper
			// premount-image <image>. Copies the image's APEX files out of it
			// and premounts them from the copy.
			if len(os.Args) != 3 {
				fatal("usage: virtroid-runtime-wrapper premount-image <image>")
			}
			docker := os.Getenv("VIRTROID_DOCKER")
			if docker == "" {
				docker = "docker"
			}
			set, err := premountImage(docker, os.Args[2])
			if err != nil {
				fatal("premount-image: %v", err)
			}
			// The set's name alone on stdout, for the node's annotation.
			fmt.Println(set)
			return
		case "userns-hold":
			holdUserns()
		case "bpf-mount", "bpf-release":
			// The node, per runtime: a fresh bpffs at bpfRoot/<name> before
			// the loading pass, gone once the runtime is.
			if len(os.Args) != 3 {
				fatal("usage: virtroid-runtime-wrapper %s <name>", os.Args[1])
			}
			var err error
			if os.Args[1] == "bpf-mount" {
				err = mountRuntimeBpffs(os.Args[2])
			} else {
				err = releaseRuntimeBpffs(os.Args[2])
			}
			if err != nil {
				fatal("%s: %v", os.Args[1], err)
			}
			return
		case "bpf-copy":
			// The node, after the loading pass: bpf-copy <src> <name> pins
			// what the image's loaders pinned in src into the runtime's bpffs,
			// owners moved into the runtimes' id range, and marks it filled.
			if len(os.Args) != 4 {
				fatal("usage: virtroid-runtime-wrapper bpf-copy <src> <name>")
			}
			uidMap, err := usernsRange(subuidPath)
			if err != nil {
				fatal("bpf-copy: %v", err)
			}
			gidMap, err := usernsRange(subgidPath)
			if err != nil {
				fatal("bpf-copy: %v", err)
			}
			if uidMap.HostID != gidMap.HostID {
				fatal("bpf-copy: the uid range starts at %d and the gid range at %d", uidMap.HostID, gidMap.HostID)
			}
			dst, err := runtimeBpffsDir(os.Args[3])
			if err != nil {
				fatal("bpf-copy: %v", err)
			}
			n, err := bpfCopy(os.Args[2], dst, uidMap.HostID)
			if err == nil {
				err = markPreloaded(dst)
			}
			if err != nil {
				fatal("bpf-copy: %v", err)
			}
			fmt.Printf("pinned %d objects in %s\n", n, dst)
			return
		case "premount":
			// From a directory already laid out like an image's root (the
			// node's copy): virtroid-runtime-wrapper premount <source> <set>.
			if len(os.Args) != 4 {
				fatal("usage: virtroid-runtime-wrapper premount <source> <set>")
			}
			names, err := premountApexes(os.Args[2], stagingRoot, os.Args[3])
			if err != nil {
				fatal("premount: %v", err)
			}
			fmt.Printf("premounted %d APEX payloads as set %s under %s\n", len(names), os.Args[3], filepath.Join(stagingRoot, os.Args[3]))
			return
		}
	}

	runc := os.Getenv("VIRTROID_RUNC")
	if runc == "" {
		runc = "/bin/runc"
	}
	verb, bundle, id := parseInvocation(os.Args[1:])
	setRuntimeLog(runcGlobals(os.Args[1:], verb))

	if verb == "delete" {
		// runc first; the container's own device nodes go once it is gone.
		cmd := exec.Command(runc, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		code := 0
		if err := cmd.Run(); err != nil {
			code = 1
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			}
		}
		if code == 0 && id != "" {
			if err := removeDevices(id); err != nil {
				warn("remove devices: %v", err)
			}
			// Docker unmounts the overlay at its own path once it is done
			// with it; the bind runc had goes now.
			if err := unbindRootfs(id); err != nil {
				warn("unbind the id-mapped rootfs: %v", err)
			}
		}
		os.Exit(code)
	}

	if verb == "create" {
		bpffs, err := prepareBundle(bundle, id)
		if err != nil {
			fatal("prepare bundle: %v", err)
		}
		// runc create is synchronous and makes the container's cgroup, so run it
		// as a child; once the cgroup exists (and before the container's init
		// runs), attach netd's programs to it and delegate it to the
		// container, then return runc's own exit status.
		code, cgroupBase, cgroupPath := runCreate(runc, os.Args[1:], bundle)
		if code != 0 && id != "" {
			// No container: nothing will delete it, so its id-mapped rootfs
			// goes now.
			if err := releaseIdmappedRootfs(id); err != nil {
				warn("release the id-mapped rootfs: %v", err)
			}
		}
		if code == 0 && bpffs != "" {
			if cgroupPath == "" {
				err = errors.New("no cgroup path to attach netd's programs to")
			} else {
				err = attachNetdPrograms(cgroupPath, bpffs)
			}
			if err != nil {
				// Without them netd can't run: undo the create and fail it.
				deleteArgs := append(runcGlobals(os.Args[1:], verb), "delete", "--force", id)
				if out, derr := exec.Command(runc, deleteArgs...).CombinedOutput(); derr != nil {
					warn("delete %s: %v: %s", id, derr, out)
				} else {
					removeDevices(id)
					releaseIdmappedRootfs(id)
				}
				fatal("%v", err)
			}
		}
		if code == 0 && cgroupPath != "" {
			if err := delegateCgroup(cgroupPath, cgroupBase); err != nil {
				// Non-fatal: the container may still boot with the controllers
				// the parent already delegated; log and let it try.
				warn("delegate cgroup: %v", err)
			}
		}
		os.Exit(code)
	}

	// Everything else behaves exactly like runc.
	if err := execRunc(runc, os.Args[1:]); err != nil {
		fatal("exec runc: %v", err)
	}
}

// parseInvocation finds the OCI verb, the bundle directory and the container
// id. runc's CLI is "<global flags> <verb> <verb flags> <id>": the verb is the
// first token that names a known operation, the bundle comes from --bundle/-b
// (default: the current directory, as runc does), and the id is the last
// argument after the verb when it isn't a flag.
func parseInvocation(args []string) (verb, bundle, id string) {
	verbs := map[string]bool{
		"create": true, "start": true, "state": true, "kill": true,
		"delete": true, "exec": true, "run": true, "pause": true,
		"resume": true, "update": true, "events": true, "list": true,
		"ps": true, "spec": true, "checkpoint": true, "restore": true,
		"features": true,
	}
	bundle, _ = os.Getwd()
	verbAt := -1
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--bundle" || a == "-b":
			if i+1 < len(args) {
				bundle = args[i+1]
				i++
			}
		case len(a) > 9 && a[:9] == "--bundle=":
			bundle = a[9:]
		case verb == "" && verbs[a]:
			verb, verbAt = a, i
		}
	}
	if last := len(args) - 1; verbAt >= 0 && last > verbAt && !strings.HasPrefix(args[last], "-") {
		id = args[last]
	}
	return verb, bundle, id
}

type ociSpec struct {
	Root struct {
		Path string `json:"path"`
	} `json:"root"`
	Mounts []ociMount `json:"mounts"`
	Linux  struct {
		CgroupsPath string  `json:"cgroupsPath"`
		UIDMappings []idMap `json:"uidMappings"`
		GIDMappings []idMap `json:"gidMappings"`
	} `json:"linux"`
	raw map[string]any
}

type ociMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options"`
}

type idMap struct {
	ContainerID uint32 `json:"containerID"`
	HostID      uint32 `json:"hostID"`
	Size        uint32 `json:"size"`
}

// mapID is the host id a container uid or gid maps to.
func mapID(maps []idMap, id uint32) (uint32, error) {
	for _, m := range maps {
		if id >= m.ContainerID && uint64(id) < uint64(m.ContainerID)+uint64(m.Size) {
			return m.HostID + (id - m.ContainerID), nil
		}
	}
	return 0, fmt.Errorf("container id %d is not mapped", id)
}

// decodeSpec reads config.json for rewriting. Numbers stay json.Number, so
// they are written back exactly as they came: through a float64, an unlimited
// rlimit (18446744073709551615, as Docker writes it) came back as
// 18446744073709552000, which runc refused.
func decodeSpec(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("config.json: data after the spec")
	}
	if raw == nil {
		return nil, errors.New("config.json: not an object")
	}
	return raw, nil
}

// prepareBundle rewrites config.json: a writable cgroup2 mount, a bind of the
// image's premounted APEX payloads at apexPremountDir (id-mapped to the
// container's user namespace so the image's root-owned files read as root
// inside), and, in a user namespace, device nodes of the container's own. In
// a user namespace the wrapper makes, the rootfs is moved onto id-mapped
// layers and runc given a bind of it. Nothing in the rootfs is opened but its
// APEX files' names and sizes, to find its image's prepared set.
func prepareBundle(bundle, id string) (string, error) {
	path := filepath.Join(bundle, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	raw, err := decodeSpec(data)
	if err != nil {
		return "", err
	}
	var spec ociSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return "", err
	}

	rootfs := spec.Root.Path
	if !filepath.IsAbs(rootfs) {
		rootfs = filepath.Join(bundle, rootfs)
	}

	// The node asked for a user namespace: the wrapper makes it (Docker
	// doesn't know of it).
	ownUserns := wantsUserns(raw)
	var uidMap, gidMap idMap
	if ownUserns {
		if uidMap, err = usernsRange(subuidPath); err != nil {
			return "", err
		}
		if gidMap, err = usernsRange(subgidPath); err != nil {
			return "", err
		}
		if err := addUserns(raw, uidMap, gidMap); err != nil {
			return "", err
		}
		if err := fitSysctls(raw, gidMap); err != nil {
			return "", err
		}
		spec.Linux.UIDMappings = []idMap{uidMap}
		spec.Linux.GIDMappings = []idMap{gidMap}
	}

	userns := len(spec.Linux.UIDMappings) > 0 && len(spec.Linux.GIDMappings) > 0
	staging, err := preparedStaging(raw, rootfs, userns)
	if err != nil {
		return "", err
	}
	rewriteSpec(raw, staging, userns)
	bpffs, err := runtimeBpffs(raw)
	if err != nil {
		return "", err
	}
	if bpffs != "" {
		bindBpffs(raw, bpffs)
	} else if userns && staging != "" {
		return "", fmt.Errorf("no BPF filesystem named for this container (annotation %s): in a user namespace Android's own loader can't fill one", bpffsAnnotation)
	}
	if userns {
		dir, err := deviceDir(id)
		if err != nil {
			return "", err
		}
		if err := privateDevices(raw, dir, spec.Linux.UIDMappings, spec.Linux.GIDMappings); err != nil {
			return "", err
		}
	}

	if ownUserns {
		// Last, so nothing above can fail with the mounts left behind.
		mapped, err := idmapRootfs(rootfs, id, uidMap, gidMap)
		if err != nil {
			return "", fmt.Errorf("rootfs: %w", err)
		}
		root, _ := raw["root"].(map[string]any)
		if root == nil {
			releaseIdmappedRootfs(id)
			return "", errors.New("the spec has no root")
		}
		root["path"] = mapped
	}

	out, err := json.Marshal(raw)
	if err == nil {
		err = os.WriteFile(path, out, 0o600)
	}
	if err != nil {
		if ownUserns {
			releaseIdmappedRootfs(id)
		}
		return "", err
	}
	return bpffs, nil
}

// rewriteSpec makes the cgroup2 mount writable (Docker mounts it read-only
// under a user namespace), in a user namespace id-maps /data and every
// read-only bind and, unless staging is "", appends the bind of the
// pre-mounted APEX tree at apexPremountDir. Each payload is its own mount under staging, so the bind is
// recursive, and in a user namespace it is recursively id-mapped (ridmap) so
// the image's root-owned files read as root inside rather than as nobody. It
// replaces an earlier bind at apexPremountDir, so a re-run doesn't stack them.
func rewriteSpec(raw map[string]any, staging string, userns bool) {
	mounts, _ := raw["mounts"].([]any)
	kept := mounts[:0]
	for _, m := range mounts {
		mm, _ := m.(map[string]any)
		if mm == nil {
			kept = append(kept, m)
			continue
		}
		switch dest, _ := mm["destination"].(string); dest {
		case "/sys/fs/cgroup":
			mm["options"] = toAny(setMountRW(toStrings(mm["options"])))
		case apexPremountDir:
			continue
		case "/data":
			// The runtime's data, from the VM's disk, id-mapped: on disk its
			// files keep Android's own ids (as the node's snapshots, restores
			// and adb key write them), and inside they read as the same ids.
			if userns && mm["type"] == "bind" {
				mm["options"] = toAny(addOption(toStrings(mm["options"]), "idmap"))
			}
		default:
			// A read-only bind from the VM is id-mapped too, so its files read
			// inside with their own ids rather than as nobody: the node's
			// root-owned assets as root, the GNSS feed's socket as gps, the
			// only user its HAL lets connect. Read-only ones only: through a
			// writable id-mapped bind the runtime's root would be the VM's
			// root for the files on it. (Docker writes a read-only bind as
			// rro, recursively read-only, where the kernel has that.)
			if opts := toStrings(mm["options"]); userns && mm["type"] == "bind" && (hasOption(opts, "ro") || hasOption(opts, "rro")) {
				mm["options"] = toAny(addOption(opts, "idmap"))
			}
		}
		kept = append(kept, m)
	}
	if staging == "" {
		raw["mounts"] = kept
		return
	}
	options := []string{"rbind", "ro", "nosuid", "nodev"}
	if userns {
		options = append(options, "ridmap")
	}
	raw["mounts"] = append(kept, map[string]any{
		"destination": apexPremountDir,
		"type":        "bind",
		"source":      staging,
		"options":     toAny(options),
	})
}

func hasOption(options []string, option string) bool {
	for _, o := range options {
		if o == option {
			return true
		}
	}
	return false
}

func addOption(options []string, option string) []string {
	for _, o := range options {
		if o == option {
			return options
		}
	}
	return append(options, option)
}

func setMountRW(options []string) []string {
	var out []string
	haveRW := false
	for _, o := range options {
		if o == "ro" {
			continue
		}
		if o == "rw" {
			haveRW = true
		}
		out = append(out, o)
	}
	if !haveRW {
		out = append(out, "rw")
	}
	return out
}

// preparedStaging is the prepared APEX set the node named for this container
// (apexSetAnnotation), checked against the APEX files in its rootfs: only
// their names and sizes are read there, never their contents. "" means bind
// none: no set was named and either the image has no APEXes or the container
// is not in a user namespace (apexd can then mount its own). In a user
// namespace a missing set is an error, as apexd would activate nothing.
func preparedStaging(raw map[string]any, rootfs string, userns bool) (string, error) {
	files, err := listApexes(rootfs)
	if err != nil {
		return "", err
	}
	annotations, _ := raw["annotations"].(map[string]any)
	set, named := annotations[apexSetAnnotation].(string)
	if !named {
		if userns && len(files) > 0 {
			return "", fmt.Errorf("no APEX set named for this container (annotation %s): the node names the one it prepared with premount-image", apexSetAnnotation)
		}
		return "", nil
	}
	if !setNamePattern.MatchString(set) {
		return "", fmt.Errorf("bad APEX set name %q", set)
	}
	staging := filepath.Join(stagingRoot, set)
	listing, err := os.ReadFile(readyMarker(staging))
	if err != nil {
		return "", fmt.Errorf("APEX set %s is not ready: %w", set, err)
	}
	if string(listing) != setListing(files) {
		return "", fmt.Errorf("APEX set %s was prepared for other APEX files than this container's", set)
	}
	return staging, nil
}

// runcGlobals is the global flags before the verb (--root, --log, ...), for
// another runc command against the same state.
func runcGlobals(args []string, verb string) []string {
	for i, a := range args {
		if a == verb {
			return append([]string(nil), args[:i]...)
		}
	}
	return nil
}

func runCreate(runc string, args []string, bundle string) (code int, cgroupBase uint32, cgroupPath string) {
	cgroupPath, cgroupBase = cgroupFromBundle(bundle)
	cmd := exec.Command(runc, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), cgroupBase, cgroupPath
		}
		warn("run runc: %v", err)
		return 1, cgroupBase, cgroupPath
	}
	return 0, cgroupBase, cgroupPath
}

// cgroupFromBundle resolves the container's cgroup2 directory and the host uid
// the container's root maps to, from the rewritten config.json.
func cgroupFromBundle(bundle string) (path string, base uint32) {
	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		return "", 0
	}
	var spec ociSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return "", 0
	}
	if host, err := mapID(spec.Linux.UIDMappings, 0); err == nil {
		base = host
	}
	cg := spec.Linux.CgroupsPath
	if cg == "" {
		return "", base
	}
	// systemd form "slice:prefix:name" is left to runc; the common form is a
	// path relative to the v2 hierarchy root.
	if filepath.IsAbs(cg) {
		path = filepath.Join("/sys/fs/cgroup", cg)
	} else if !isSystemdCgroup(cg) {
		path = filepath.Join("/sys/fs/cgroup", cg)
	}
	return path, base
}

func isSystemdCgroup(s string) bool {
	colons := 0
	for _, c := range s {
		if c == ':' {
			colons++
		}
	}
	return colons == 2
}

// delegateCgroup hands the container's own cgroup to its (unprivileged, mapped)
// root so Android's init can create per-service and per-app sub-groups: the
// directory and the files init writes are chowned to the container's root.
func delegateCgroup(path string, base uint32) error {
	if path == "" {
		return nil
	}
	// Delegation is ownership only (cgroup-v2.rst, "Delegation"): the
	// directory and the files a delegatee writes. Which controllers to enable
	// below is the container's choice. Enabling them here was wrong twice: it
	// wrote the parent's (the VM's) subtree_control, and threaded controllers
	// (pids, cpu) enabled in a cgroup that holds tasks make it a thread root,
	// after which every cgroup Android's init made under it refused processes
	// (EOPNOTSUPP).
	for _, name := range []string{"", "cgroup.procs", "cgroup.threads", "cgroup.subtree_control"} {
		p := filepath.Join(path, name)
		if err := os.Chown(p, int(base), int(base)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("chown %s: %w", p, err)
		}
	}
	return nil
}

func execRunc(runc string, args []string) error {
	argv := append([]string{runc}, args...)
	return syscall.Exec(runc, argv, os.Environ())
}

func toStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// runtimeLog is runc's --log file for this call, if Docker gave one, and its
// --log-format. containerd reads why a create failed from the file's last
// error entry, and reports "unable to retrieve OCI runtime error" without
// one, so the wrapper's own errors go there as runc's would.
var runtimeLog struct{ path, format string }

// setRuntimeLog reads --log and --log-format from runc's global flags.
func setRuntimeLog(globals []string) {
	for i := 0; i < len(globals); i++ {
		a := globals[i]
		value := func() string {
			if i+1 < len(globals) {
				i++
				return globals[i]
			}
			return ""
		}
		switch {
		case a == "--log":
			runtimeLog.path = value()
		case strings.HasPrefix(a, "--log="):
			runtimeLog.path = strings.TrimPrefix(a, "--log=")
		case a == "--log-format":
			runtimeLog.format = value()
		case strings.HasPrefix(a, "--log-format="):
			runtimeLog.format = strings.TrimPrefix(a, "--log-format=")
		}
	}
}

// logRuntime appends an entry to the runtime log as runc writes them, in
// logrus's json or text form.
func logRuntime(level, msg string) {
	if runtimeLog.path == "" {
		return
	}
	f, err := os.OpenFile(runtimeLog.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	now := time.Now().Format(time.RFC3339Nano)
	if runtimeLog.format == "json" {
		line, _ := json.Marshal(map[string]string{"level": level, "msg": msg, "time": now})
		f.Write(append(line, '\n'))
		return
	}
	fmt.Fprintf(f, "time=%q level=%s msg=%q\n", now, level, msg)
}

func warn(format string, a ...any) {
	msg := fmt.Sprintf("virtroid-runtime-wrapper: "+format, a...)
	fmt.Fprintln(os.Stderr, msg)
	logRuntime("warning", msg)
}

func fatal(format string, a ...any) {
	msg := fmt.Sprintf("virtroid-runtime-wrapper: "+format, a...)
	fmt.Fprintln(os.Stderr, msg)
	logRuntime("error", msg)
	os.Exit(1)
}
