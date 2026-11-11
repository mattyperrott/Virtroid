//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// TestWriteRuntimeDataFile: a file the node saves into a runtime's data
// directory has its owner and mode and replaces what was there, and nothing
// the runtime put on the way (a symlink, a symlinked directory, another mount)
// takes the write out of that directory.
func TestWriteRuntimeDataFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give the file a runtime's owner")
	}
	data := t.TempDir()
	outside := t.TempDir()
	download := filepath.Join(data, "media", "0", "Download")
	if err := os.MkdirAll(download, 0o770); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	leftovers := func(dir string) {
		t.Helper()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".virtroid-write-") {
				t.Errorf("temporary file %s left in %s", e.Name(), dir)
			}
		}
	}

	// Owner and mode, then a second write replaces the first.
	if err := writeRuntimeDataFile(data, "media/0/Download/a.jpg", []byte("one"), 0o660, 1023, 1023); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeDataFile(data, "media/0/Download/a.jpg", []byte("two"), 0o660, 10079, 1023); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(download, "a.jpg")
	var st syscall.Stat_t
	if err := syscall.Lstat(a, &st); err != nil {
		t.Fatal(err)
	}
	if got := read(a); got != "two" {
		t.Errorf("content %q, want the second write", got)
	}
	if st.Uid != 10079 || st.Gid != 1023 || st.Mode&0o7777 != 0o660 || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		t.Errorf("owner %d:%d mode %o, want 10079:1023 regular 660", st.Uid, st.Gid, st.Mode)
	}

	// A symlink at the file's name is replaced, its target untouched.
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("the VM's"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(download, "b.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeDataFile(data, "media/0/Download/b.jpg", []byte("import"), 0o660, 1023, 1023); err != nil {
		t.Fatal(err)
	}
	if got := read(victim); got != "the VM's" {
		t.Errorf("the symlink's target became %q", got)
	}
	if info, err := os.Lstat(filepath.Join(download, "b.jpg")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("b.jpg is %v, %v: want the imported file in place of the link", info, err)
	}

	// A symlinked directory on the way, relative or absolute, is refused.
	if err := os.Symlink(outside, filepath.Join(data, "media", "0", "Linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../..", filepath.Join(download, "up")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"media/0/Linked/c.jpg", "media/0/Download/up/c.jpg"} {
		if err := writeRuntimeDataFile(data, rel, []byte("x"), 0o660, 1023, 1023); err == nil {
			t.Errorf("%s: written through a symlink", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "c.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file appeared outside the data directory: %v", err)
	}

	// Another mount on the way is refused.
	mounted := filepath.Join(data, "media", "0", "Mounted")
	if err := os.Mkdir(mounted, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", mounted, "tmpfs", 0, "size=1m"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Unmount(mounted, unix.MNT_DETACH) })
	if err := writeRuntimeDataFile(data, "media/0/Mounted/d.jpg", []byte("x"), 0o660, 1023, 1023); err == nil {
		t.Error("written into another mount")
	}

	// Paths that are not plainly below the data directory.
	for _, rel := range []string{"", "/etc/e.jpg", "../e.jpg", "..", "media/../../e.jpg", "media/0/Download/", "media/0/Download/.", "media//0/e.jpg"} {
		if err := writeRuntimeDataFile(data, rel, []byte("x"), 0o660, 1023, 1023); err == nil {
			t.Errorf("%q accepted", rel)
		}
	}
	// A missing directory, a directory at the name and a bad owner fail and
	// leave nothing behind.
	if err := os.Mkdir(filepath.Join(download, "f.jpg"), 0o770); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		rel      string
		uid, gid int
	}{{"media/0/Missing/g.jpg", 1023, 1023}, {"media/0/Download/f.jpg", 1023, 1023}, {"media/0/Download/h.jpg", -1, 1023}} {
		if err := writeRuntimeDataFile(data, c.rel, []byte("x"), 0o660, c.uid, c.gid); err == nil {
			t.Errorf("%s as %d:%d: no error", c.rel, c.uid, c.gid)
		}
	}
	if _, err := os.Lstat(filepath.Join(download, "h.jpg")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("h.jpg exists after a refused owner: %v", err)
	}
	leftovers(download)
}
