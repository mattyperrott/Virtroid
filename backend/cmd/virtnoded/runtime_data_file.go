//go:build linux

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

// writeRuntimeDataFile writes contents to rel, a path below a runtime's data
// directory (the one its container gets as /data), owned by uid:gid with mode.
// The node writes it itself rather than through Docker's archive API: for a
// runtime in a user namespace Docker cannot write into the container's rootfs
// (its id-mapped overlay has no id for the VM's root), and refuses the whole
// copy even below /data. The file's owner is the runtime's own id either way:
// its /data is this directory, plainly bound or id-mapped to the same numbers.
//
// Everything below dataDir belongs to the runtime, which may have put symlinks
// there. So no symlink is followed and no other mount entered on the way to
// rel's directory (openat2 RESOLVE_BENEATH, NO_SYMLINKS, NO_XDEV), the file is
// made new beside rel and only renamed over it once written and owned; a
// symlink at rel itself is replaced, not followed.
func writeRuntimeDataFile(dataDir, rel string, contents []byte, mode os.FileMode, uid, gid int) error {
	if rel == "" || path.IsAbs(rel) || path.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("runtime data path %q is not a clean relative path", rel)
	}
	dir, name := path.Split(rel)
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("runtime data path %q names no file", rel)
	}
	if uid < 0 || gid < 0 {
		return errors.New("runtime data file owner is invalid")
	}
	root, err := unix.Open(dataDir, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: dataDir, Err: err}
	}
	defer unix.Close(root)
	if dir == "" {
		dir = "."
	}
	dirfd, err := unix.Openat2(root, dir, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return &os.PathError{Op: "openat2", Path: path.Join(dataDir, dir), Err: err}
	}
	defer unix.Close(dirfd)

	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".virtroid-write-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &os.PathError{Op: "create", Path: path.Join(dataDir, dir, tmp), Err: err}
	}
	f := os.NewFile(uintptr(fd), path.Join(dataDir, dir, tmp))
	err = fillFile(f, contents, mode, uid, gid)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if rerr := unix.Renameat(dirfd, tmp, dirfd, name); rerr != nil {
			err = &os.LinkError{Op: "rename", Old: tmp, New: path.Join(dataDir, rel), Err: rerr}
		}
	}
	if err != nil {
		unix.Unlinkat(dirfd, tmp, 0)
		return err
	}
	return nil
}

// fillFile writes contents to f, then gives it its owner and mode and syncs it.
func fillFile(f *os.File, contents []byte, mode os.FileMode, uid, gid int) error {
	if _, err := f.Write(contents); err != nil {
		return err
	}
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	return f.Sync()
}
