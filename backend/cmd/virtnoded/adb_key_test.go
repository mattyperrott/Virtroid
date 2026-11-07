package main

import (
	"os"
	"path/filepath"
	"testing"
)

type chownCall struct {
	path     string
	uid, gid int
}

func recordChowns(calls *[]chownCall) func(*os.Root, string, int, int) error {
	return func(_ *os.Root, name string, uid, gid int) error {
		*calls = append(*calls, chownCall{filepath.Base(name), uid, gid})
		return nil
	}
}

func TestAuthorizeADBKeyWritesOnlyTheNodeKey(t *testing.T) {
	dataDir := t.TempDir()
	var calls []chownCall
	if err := authorizeADBKey(dataDir, []byte("QAAAAOld user@old-vm\n"), recordChowns(&calls)); err != nil {
		t.Fatal(err)
	}
	if err := authorizeADBKey(dataDir, []byte("  QAAAANew root@virtroid  \n"), recordChowns(&calls)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, adbKeysPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "QAAAANew root@virtroid\n" {
		t.Fatalf("adb_keys = %q, want only the new key", got)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "misc", "adb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("misc/adb holds %d entries, want only adb_keys", len(entries))
	}
}

func TestAuthorizeADBKeyUsesAndroidOwnersAndModes(t *testing.T) {
	dataDir := t.TempDir()
	var calls []chownCall
	if err := authorizeADBKey(dataDir, []byte("QAAAAKey root@virtroid"), recordChowns(&calls)); err != nil {
		t.Fatal(err)
	}
	want := map[string]chownCall{
		"misc": {"misc", androidSystemUID, androidMiscGID},
		"adb":  {"adb", androidSystemUID, androidShellGID},
	}
	for _, call := range calls {
		if expected, ok := want[call.path]; ok && call != expected {
			t.Fatalf("chown %s = %d:%d, want %d:%d", call.path, call.uid, call.gid, expected.uid, expected.gid)
		}
		if call.path != "misc" && call.path != "adb" && (call.uid != androidSystemUID || call.gid != androidShellGID) {
			t.Fatalf("adb_keys owned %d:%d, want system:shell", call.uid, call.gid)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("chown called %d times, want misc, misc/adb and the keys file", len(calls))
	}
	for path, mode := range map[string]os.FileMode{
		"misc":      os.ModeDir | os.ModeSticky | 0o771,
		"misc/adb":  os.ModeDir | os.ModeSetgid | 0o750,
		adbKeysPath: 0o640,
	} {
		info, err := os.Stat(filepath.Join(dataDir, path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != mode {
			t.Fatalf("%s mode = %v, want %v", path, info.Mode(), mode)
		}
	}
}

func TestAuthorizeADBKeyRejectsAnythingButOneKey(t *testing.T) {
	for _, key := range []string{"", "  \n", "QAAAAOne a@b\nQAAAATwo c@d"} {
		if err := authorizeADBKey(t.TempDir(), []byte(key), recordChowns(new([]chownCall))); err == nil {
			t.Fatalf("authorizeADBKey(%q) succeeded, want an error", key)
		}
	}
}

// The guest owns /data, so it can leave a link where Android keeps misc or
// misc/adb. The node must neither write through it nor change what it
// points at.
func TestAuthorizeADBKeyRefusesLinksInTheGuestData(t *testing.T) {
	for _, linked := range []string{"misc", "misc/adb"} {
		t.Run(linked, func(t *testing.T) {
			dataDir := t.TempDir()
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dataDir, linked)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dataDir, linked)); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			var calls []chownCall
			if err := authorizeADBKey(dataDir, []byte("QAAAAKey root@virtroid"), recordChowns(&calls)); err == nil {
				t.Fatal("authorizeADBKey followed a link in the guest's data")
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("authorizeADBKey wrote %d entries through the link", len(entries))
			}
			after, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			if after.Mode() != before.Mode() {
				t.Fatalf("link target mode changed from %v to %v", before.Mode(), after.Mode())
			}
			for _, call := range calls {
				if call.path == filepath.Base(linked) {
					t.Fatalf("chown was called on the link %s", linked)
				}
			}
		})
	}
}

func TestADBUserKeyDirFollowsADB(t *testing.T) {
	t.Setenv("ANDROID_USER_HOME", "")
	t.Setenv("ANDROID_SDK_HOME", "")
	t.Setenv("HOME", "/run/root")
	if dir, _ := adbUserKeyDir(); dir != "/run/root/.android" {
		t.Fatalf("key dir = %q, want $HOME/.android", dir)
	}
	t.Setenv("ANDROID_SDK_HOME", "/sdk")
	if dir, _ := adbUserKeyDir(); dir != "/sdk/.android" {
		t.Fatalf("key dir = %q, want $ANDROID_SDK_HOME/.android", dir)
	}
	t.Setenv("ANDROID_USER_HOME", "/adb-home")
	if dir, _ := adbUserKeyDir(); dir != "/adb-home" {
		t.Fatalf("key dir = %q, want $ANDROID_USER_HOME", dir)
	}
}
