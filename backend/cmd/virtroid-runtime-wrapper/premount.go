//go:build linux

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// A runtime's APEXes are activated by apexd, which puts each payload filesystem
// on a loop (or dm) block device and mount()s it. Inside a user namespace the
// kernel refuses to mount a filesystem from a block device, so apexd-bootstrap
// activated nothing and init rebooted. Instead the VM mounts each payload
// (outside the namespace, where it is allowed) and the container bind-mounts
// it (a bind of an already-mounted source is allowed in a user namespace);
// apexd does the bind when it finds the payload under apexPremountDir (see the
// system/apex patches).
//
// What the VM mounts comes only from the image, copied out of it by the node
// into a directory of its own (premountImage), never from a container's
// rootfs: that includes the container's writable layer, and a compromised
// runtime could otherwise leave crafted filesystem images there for the VM's
// kernel to mount at its next start. Each prepared set is named after its
// image, and the node names the set a container gets (apexSetAnnotation); the
// wrapper only checks the container's APEX listing against the set's. (Picking
// a set by the listing alone was wrong: a rebuilt APEX often keeps its size,
// so a new image could have been given an older image's payloads.)

// apexPremountDir is the in-container path apexd looks under, and the mount
// point the wrapper binds the prepared set onto. A leading dot keeps it out of
// a casual listing of /dev (a tmpfs the container already has).
const apexPremountDir = "/dev/.vapex"

// apexDirs are the directories apexd scans for pre-installed APEXes. On our
// image system_ext and product are symlinks into system.
var apexDirs = []string{"system/apex", "system_ext/apex", "product/apex", "vendor/apex", "odm/apex"}

// apexSetAnnotation names, in a container's spec, the prepared APEX set it
// gets: the node sets it when it creates the container.
const apexSetAnnotation = "io.virtroid.apex-set"

var setNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

var (
	// stagingRoot holds one directory of mounted payloads per APEX set, and
	// beside each a ".ready" marker, holding the set's listing, once every
	// payload in it is mounted.
	stagingRoot = "/run/virtroid/vapex"
	// sourceRoot holds the node's copies of each image's APEX files, on the
	// VM's data disk, readable by its root only.
	sourceRoot = "/srv/virtroid/apex-src"
)

const (
	ext4SuperblockMagicOffset = 0x438      // 1024 + 0x38
	ext4Magic                 = 0xEF53     // s_magic, little-endian uint16
	erofsSuperblockMagic      = 0xE0F5E1E2 // at byte 1024, little-endian uint32
	erofsMagicOffset          = 1024
)

// apexFile is one pre-installed APEX: its path under the root, as apexd would
// name it (e.g. vendor/apex/x.apex), and its size.
type apexFile struct {
	rel  string
	size int64
}

// openInRoot opens rel under the directory root with every symlink resolved
// as if root were "/" (RESOLVE_IN_ROOT): an absolute or ".." symlink in an
// image or a container's rootfs can't lead out of it.
func openInRoot(rootFd int, rel string, flags int) (*os.File, error) {
	fd, err := unix.Openat2(rootFd, rel, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, &os.PathError{Op: "openat2", Path: rel, Err: err}
	}
	return os.NewFile(uintptr(fd), rel), nil
}

func openRoot(root string) (int, error) {
	fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: root, Err: err}
	}
	return fd, nil
}

// listApexes lists root's pre-installed APEX files, sorted. Only regular
// files count: a symlink named *.apex is skipped, not followed.
func listApexes(root string) ([]apexFile, error) {
	rootFd, err := openRoot(root)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFd)
	var files []apexFile
	seen := map[string]bool{}
	for _, dir := range apexDirs {
		d, err := openInRoot(rootFd, dir, unix.O_RDONLY|unix.O_DIRECTORY)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// Two names for one directory (a symlink to another listed one) would
		// list its APEXes twice.
		var st unix.Stat_t
		if err := unix.Fstat(int(d.Fd()), &st); err != nil {
			d.Close()
			return nil, err
		}
		id := fmt.Sprintf("%d:%d", st.Dev, st.Ino)
		if seen[id] {
			d.Close()
			continue
		}
		seen[id] = true
		names, err := d.Readdirnames(-1)
		if err != nil {
			d.Close()
			return nil, err
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".apex") {
				continue
			}
			var fst unix.Stat_t
			if err := unix.Fstatat(int(d.Fd()), name, &fst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				d.Close()
				return nil, err
			}
			if fst.Mode&unix.S_IFMT != unix.S_IFREG {
				continue
			}
			files = append(files, apexFile{rel: dir + "/" + name, size: fst.Size})
		}
		d.Close()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, nil
}

// setListing is an APEX set's listing (paths and sizes), recorded in its
// ready marker: a container's must match the set it is given.
func setListing(files []apexFile) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s:%d\n", f.rel, f.size)
	}
	return b.String()
}

func readyMarker(staging string) string { return staging + ".ready" }

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// premountApexes mounts every pre-installed APEX payload in source (the
// node's copy of an image's APEX files) read-only under
// stagingRoot/<set>/<manifest-name>, then marks the set ready with its
// listing. It is idempotent and safe to run concurrently. It returns the names
// it mounted.
func premountApexes(source, stagingRoot, set string) ([]string, error) {
	if !setNamePattern.MatchString(set) {
		return nil, fmt.Errorf("bad APEX set name %q", set)
	}
	files, err := listApexes(source)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s has no pre-installed APEXes", source)
	}
	staging := filepath.Join(stagingRoot, set)
	if err := os.MkdirAll(stagingRoot, 0o711); err != nil {
		return nil, err
	}
	// Docker may create several runtimes at once; without the lock two could
	// both find a payload unmounted and stack two mounts.
	unlock, err := lockFile(staging + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	// A set's name is its image's id, so a ready set of that name must have
	// been made from the same files: refuse before mounting anything if not.
	marker := readyMarker(staging)
	if data, err := os.ReadFile(marker); err == nil && string(data) != setListing(files) {
		return nil, fmt.Errorf("APEX set %s is already prepared from other APEX files", set)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, err
	}
	rootFd, err := openRoot(source)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootFd)
	var names []string
	from := map[string]string{}
	for _, f := range files {
		name, err := premountOne(rootFd, f.rel, staging)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.rel, err)
		}
		if other, dup := from[name]; dup {
			return nil, fmt.Errorf("%s and %s are both %s", other, f.rel, name)
		}
		from[name] = f.rel
		names = append(names, name)
	}
	sort.Strings(names)
	if !exists(marker) {
		tmp := marker + ".tmp"
		if err := os.WriteFile(tmp, []byte(setListing(files)), 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, marker); err != nil {
			return nil, err
		}
	}
	return names, nil
}

func premountOne(rootFd int, rel, staging string) (string, error) {
	f, err := openInRoot(rootFd, rel, unix.O_RDONLY|unix.O_NOFOLLOW)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return "", fmt.Errorf("open apex: %w", err)
	}

	var payload *zip.File
	var manifest *zip.File
	for _, zf := range zr.File {
		switch zf.Name {
		case "apex_payload.img":
			payload = zf
		case "apex_manifest.pb":
			manifest = zf
		}
	}
	if payload == nil {
		return "", errors.New("no apex_payload.img")
	}
	if payload.Method != zip.Store {
		return "", fmt.Errorf("apex_payload.img is compressed (method %d); this APEX is not directly mountable", payload.Method)
	}
	name, err := apexManifestName(manifest)
	if err != nil {
		return "", err
	}
	offset, err := payload.DataOffset()
	if err != nil {
		return "", fmt.Errorf("payload offset: %w", err)
	}
	size := int64(payload.UncompressedSize64)
	if offset < 0 || size <= 0 || offset+size > info.Size() {
		return "", fmt.Errorf("payload [%d, +%d) is not inside the file (%d bytes)", offset, size, info.Size())
	}

	target := filepath.Join(staging, name)
	if mounted, err := isMountPoint(target); err != nil {
		return "", err
	} else if mounted {
		return name, nil // already premounted (idempotent)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}

	fsType, err := payloadFsType(f, offset)
	if err != nil {
		return "", err
	}
	loop, err := attachLoop(f, offset, size)
	if err != nil {
		return "", err
	}
	// The loop device is set to clear itself (LO_FLAGS_AUTOCLEAR) when its last
	// opener goes, so it must stay open until the mount holds it: closing it
	// first would detach it and leave the mount an empty device. After a
	// mount, the device is freed when the mount goes; after a failed mount,
	// this close frees it.
	defer loop.Close()
	flags := uintptr(unix.MS_RDONLY | unix.MS_NOATIME | unix.MS_NODEV | unix.MS_NOSUID)
	if err := unix.Mount(loop.Name(), target, fsType, flags, ""); err != nil {
		return "", fmt.Errorf("mount %s (%s) at %s: %w", loop.Name(), fsType, target, err)
	}
	return name, nil
}

// apexManifestName reads the module name (field 1 of ApexManifest, a protobuf)
// from apex_manifest.pb. The name is how apexd keys the mount point, so it must
// match, not the file's basename.
func apexManifestName(manifest *zip.File) (string, error) {
	if manifest == nil {
		return "", errors.New("no apex_manifest.pb")
	}
	rc, err := manifest.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 1<<16))
	if err != nil {
		return "", err
	}
	// Field 1, wire type 2 (length-delimited) => tag byte 0x0a, then a varint
	// length, then the UTF-8 name. The name is the first field apexer writes.
	if len(data) < 2 || data[0] != 0x0a {
		return "", errors.New("apex_manifest.pb: name field not first")
	}
	length, n := binary.Uvarint(data[1:])
	if n <= 0 || int(length) > len(data)-1-n {
		return "", errors.New("apex_manifest.pb: bad name length")
	}
	name := string(data[1+n : 1+n+int(length)])
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return "", fmt.Errorf("apex_manifest.pb: implausible name %q", name)
	}
	return name, nil
}

func payloadFsType(f io.ReaderAt, offset int64) (string, error) {
	buf := make([]byte, 0x440)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return "", err
	}
	if binary.LittleEndian.Uint32(buf[erofsMagicOffset:]) == erofsSuperblockMagic {
		return "erofs", nil
	}
	if binary.LittleEndian.Uint16(buf[ext4SuperblockMagicOffset:]) == ext4Magic {
		return "ext4", nil
	}
	return "", errors.New("payload is neither ext4 nor erofs")
}

// lockFile takes an exclusive lock on path (made if missing) and returns its
// release.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}

// loopAttachAttempts bounds the retries when another process takes the free
// loop device between asking for it and configuring it.
const loopAttachAttempts = 8

// attachLoop binds backing[offset:offset+size] to a free loop device,
// read-only and set to clear itself, and returns the device, open. The caller
// must keep it open until the device is mounted (see premountOne).
func attachLoop(backing *os.File, offset, size int64) (*os.File, error) {
	control, err := os.OpenFile("/dev/loop-control", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open loop-control: %w", err)
	}
	defer control.Close()
	for attempt := 1; ; attempt++ {
		loop, err := configureFreeLoop(control, backing, offset, size)
		if errors.Is(err, unix.EBUSY) && attempt < loopAttachAttempts {
			continue
		}
		return loop, err
	}
}

func configureFreeLoop(control, backing *os.File, offset, size int64) (*os.File, error) {
	number, err := unix.IoctlRetInt(int(control.Fd()), unix.LOOP_CTL_GET_FREE)
	if err != nil {
		return nil, fmt.Errorf("get free loop: %w", err)
	}
	loopPath := fmt.Sprintf("/dev/loop%d", number)
	loop, err := os.OpenFile(loopPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", loopPath, err)
	}

	config := unix.LoopConfig{
		Fd: uint32(backing.Fd()),
		Info: unix.LoopInfo64{
			Offset:    uint64(offset),
			Sizelimit: uint64(size),
			Flags:     unix.LO_FLAGS_READ_ONLY | unix.LO_FLAGS_AUTOCLEAR,
		},
	}
	copy(config.Info.File_name[:len(config.Info.File_name)-1], backing.Name())
	if err := unix.IoctlLoopConfigure(int(loop.Fd()), &config); err != nil {
		loop.Close()
		return nil, fmt.Errorf("configure %s: %w", loopPath, err)
	}
	return loop, nil
}

// isMountPoint reports whether path is the root of a mount. The kernel says
// so (STATX_ATTR_MOUNT_ROOT); comparing device numbers with the parent's
// would miss a bind or id-mapped mount of a directory on the same
// filesystem.
func isMountPoint(path string) (bool, error) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS, &stx); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, &os.PathError{Op: "statx", Path: path, Err: err}
	}
	if stx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT == 0 {
		return false, fmt.Errorf("statx %s: the kernel does not say whether it is a mount", path)
	}
	return stx.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0, nil
}

// --- The node's copy of an image's APEX files ----------------------------------

var imageIDPattern = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)

// premountImage prepares image's APEX set for its runtimes: it copies the
// image's pre-installed APEX files out of it (once per image, kept under
// sourceRoot) and premounts them from that copy. The node runs it when it
// loads an image, before any runtime of it starts. It returns the set's name,
// the image's id, which the node gives each runtime of the image in
// apexSetAnnotation.
func premountImage(docker, image string) (string, error) {
	out, err := exec.Command(docker, "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", image, commandError(err))
	}
	m := imageIDPattern.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		return "", fmt.Errorf("image %s: unexpected id %q", image, strings.TrimSpace(string(out)))
	}
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(sourceRoot, 0o700); err != nil {
		return "", err
	}
	source := filepath.Join(sourceRoot, m[1])
	unlock, err := lockFile(source + ".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	if !exists(filepath.Join(source, ".complete")) {
		if err := copyImageApexes(docker, image, m[1], source); err != nil {
			return "", err
		}
	}
	if _, err := premountApexes(source, stagingRoot, m[1]); err != nil {
		return "", err
	}
	return m[1], nil
}

// copyImageApexes copies each of apexDirs out of image into source, each
// through a container of its own that is created and never started.
func copyImageApexes(docker, image, id, source string) error {
	tmp := source + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(source); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	copied := 0
	for i, dir := range apexDirs {
		name := fmt.Sprintf("virtroid-apex-copy-%s-%d-%d", id[:12], os.Getpid(), i)
		n, err := copyApexDirVia(docker, image, name, dir, filepath.Join(tmp, filepath.FromSlash(dir)))
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		copied += n
	}
	if copied == 0 {
		return fmt.Errorf("image %s has no pre-installed APEXes", image)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o400); err != nil {
		return err
	}
	return os.Rename(tmp, source)
}

// copyApexDirVia copies dir of image through a new container, name, removed
// before it returns. One container per directory: Docker mounts a stopped
// container's overlay for each copy and lets that mount go in the background,
// so a second copy through the same container can mount its upper directory
// while the first mount still holds it. The kernel then logs "upperdir is
// in-use as upperdir/workdir of another mount", the sign of the hazard the
// runtimes' id-mapped rootfs must never cause, which the boot test fails on.
func copyApexDirVia(docker, image, name, dir, dest string) (int, error) {
	if out, err := exec.Command(docker, "create", "--name", name, "--network", "none", image).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("create a container of %s: %v: %s", image, err, bytes.TrimSpace(out))
	}
	defer exec.Command(docker, "rm", "-f", name).Run()
	return copyApexDir(docker, name, dir, dest)
}

// copyApexDir copies the *.apex files of one directory of the container (dir
// absent: none) into dest. It asks for dir's contents (dir/.), so Docker
// resolves every symlink on the way, dir itself included, within the
// container's root, in that one request (docker cp --follow-link would make a
// second request first, with the overlap copyApexDirVia describes).
func copyApexDir(docker, container, dir, dest string) (int, error) {
	cmd := exec.Command(docker, "cp", container+":/"+dir+"/.", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	n, extractErr := extractApexTar(stdout, dest)
	io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if waitErr != nil {
		msg := stderr.String()
		if strings.Contains(msg, "Could not find the file") || strings.Contains(msg, "No such container:path") {
			return 0, os.RemoveAll(dest)
		}
		return 0, fmt.Errorf("docker cp: %v: %s", waitErr, strings.TrimSpace(msg))
	}
	return n, extractErr
}

// extractApexTar writes the top-level regular *.apex files of a docker cp
// archive of a directory's contents (entries ./NAME) into dest, readable by
// root only. Anything else in it is skipped.
func extractApexTar(r io.Reader, dest string) (int, error) {
	tr := tar.NewReader(r)
	n := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		name := path.Clean(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || strings.Contains(name, "/") ||
			!strings.HasSuffix(name, ".apex") || name == ".apex" {
			continue
		}
		if err := os.MkdirAll(dest, 0o700); err != nil {
			return n, err
		}
		f, err := os.OpenFile(filepath.Join(dest, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
		if err != nil {
			return n, err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, err
		}
		n++
	}
}

func commandError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return fmt.Errorf("%v: %s", err, bytes.TrimSpace(exit.Stderr))
	}
	return err
}
