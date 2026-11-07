package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A user build's adbd accepts only clients whose key is authorized (unlike
// ReDroid's userdebug image, whose adbd lets anyone in). The node is the only
// adb client a runtime has: it installs apps, runs commands and starts the
// viewer over adb. So before each start it puts its own public key, and only
// that, in the runtime's /data/misc/adb/adb_keys. Inside the VM the key lives
// in the node's tmpfs home, so every VM boot makes a new one; a runtime
// restored on another VM drops the previous VM's key at its first start.
const (
	adbKeysPath = "misc/adb/adb_keys"
	// Android's owners: the system user, and the shell group adbd runs as.
	androidSystemUID = 1000
	androidShellGID  = 2000
	androidMiscGID   = 9998
)

// adbUserKeyDir is where the adb client keeps its key, as adb itself looks
// for it.
func adbUserKeyDir() (string, error) {
	if dir := os.Getenv("ANDROID_USER_HOME"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("ANDROID_SDK_HOME"); dir != "" {
		return filepath.Join(dir, ".android"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".android"), nil
}

// nodeADBPublicKey returns the public key of the node's adb client. Starting
// the adb server makes the key if there is none yet, and loads the one the
// node will use.
func nodeADBPublicKey(ctx context.Context, adbPath string) ([]byte, error) {
	keyDir, err := adbUserKeyDir()
	if err != nil {
		return nil, fmt.Errorf("find the adb key directory: %w", err)
	}
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(startCtx, adbPath, "start-server").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("start the adb server: %w: %s", err, strings.TrimSpace(string(output)))
	}
	key, err := os.ReadFile(filepath.Join(keyDir, "adbkey.pub"))
	if err != nil {
		return nil, fmt.Errorf("read the adb public key: %w", err)
	}
	return key, nil
}

// authorizeADBKey makes publicKey the only key the runtime's adbd accepts.
// Everything in dataDir belongs to the guest, which can leave a link where
// Android keeps a directory, so the work goes through a handle on dataDir
// and refuses links: nothing lands outside the runtime's data. chown sets
// Android's owners without following links; tests pass their own.
func authorizeADBKey(dataDir string, publicKey []byte, chown func(root *os.Root, name string, uid, gid int) error) (resultErr error) {
	key := bytes.TrimSpace(publicKey)
	if len(key) == 0 || bytes.ContainsAny(key, "\n\r") {
		return errors.New("the adb public key is not a single line")
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	// The same modes and owners Android's init.rc gives these directories.
	for _, dir := range []struct {
		name string
		mode os.FileMode
		gid  int
	}{
		{"misc", 0o771 | os.ModeSticky, androidMiscGID},
		{"misc/adb", 0o750 | os.ModeSetgid, androidShellGID},
	} {
		if err := root.Mkdir(dir.name, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err := root.Lstat(dir.name)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("runtime data has a %v where Android keeps the %s directory", info.Mode().Type(), dir.name)
		}
		if err := chown(root, dir.name, androidSystemUID, dir.gid); err != nil {
			return err
		}
		if err := root.Chmod(dir.name, dir.mode); err != nil {
			return err
		}
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmpName := "misc/adb/.adb_keys-" + hex.EncodeToString(suffix)
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmpName)
	_, writeErr := tmp.Write(append(key, '\n'))
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := chown(root, tmpName, androidSystemUID, androidShellGID); err != nil {
		return err
	}
	if err := root.Chmod(tmpName, 0o640); err != nil {
		return err
	}
	return root.Rename(tmpName, adbKeysPath)
}
