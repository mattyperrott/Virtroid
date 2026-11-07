package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Android keeps each app's device ID (Settings.Secure.ANDROID_ID, one per
// app signing key) in this file in /data, so it survives stop and start and
// app reinstalls. Deleting it before Android boots makes Android generate new
// ones. Only a start asking for a fresh identity does this: ordinary starts
// keep the IDs, so apps that tie a login to them stay signed in.
var appDeviceIDFiles = []string{
	"system/users/0/settings_ssaid.xml",
	"system/users/0/settings_ssaid.xml.bak",
	"system/users/0/settings_ssaid.xml.fallback",
}

// appDeviceIDResetMarker records the persona version whose start reset the
// IDs, so rebuilding that same start (after a VM reboot) does not reset them
// again.
const appDeviceIDResetMarker = ".virtroid-app-device-ids-reset"

// wantsAppDeviceIDReset reports whether this start is the one its owner
// asked to give the apps new device IDs.
func wantsAppDeviceIDReset(runtime runtimeAssignment) bool {
	return runtime.IdentityResetPersonaVersion != nil && *runtime.IdentityResetPersonaVersion == runtime.PersonaVersion
}

// resetAppDeviceIDs deletes the IDs once for personaVersion and reports
// whether it did. dataDir belongs to the guest, so the marker and the ID
// files are read, deleted and written through a handle on it, never through
// a link the guest left.
func resetAppDeviceIDs(dataDir string, personaVersion int) (reset bool, resultErr error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return false, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	if previous, err := readResetMarker(root); err == nil && strings.TrimSpace(string(previous)) == strconv.Itoa(personaVersion) {
		return false, nil
	}
	var errs []error
	for _, name := range appDeviceIDFiles {
		if err := ensureRootPathHasNoSymlinks(root, name, false); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	// Whatever holds the marker's name, a link included, is replaced rather
	// than written through.
	if err := root.Remove(appDeviceIDResetMarker); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	marker, err := root.OpenFile(appDeviceIDResetMarker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	_, writeErr := marker.WriteString(strconv.Itoa(personaVersion) + "\n")
	return true, errors.Join(writeErr, marker.Close())
}

// readResetMarker reads the marker if it is a small regular file, without
// following a link at its name.
func readResetMarker(root *os.Root) ([]byte, error) {
	file, err := root.OpenFile(appDeviceIDResetMarker, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the reset marker is not a regular file")
	}
	return io.ReadAll(io.LimitReader(file, 64))
}
