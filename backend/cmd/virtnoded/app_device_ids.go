package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
// whether it did.
func resetAppDeviceIDs(dataDir string, personaVersion int) (bool, error) {
	marker := filepath.Join(dataDir, appDeviceIDResetMarker)
	if previous, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(previous)) == strconv.Itoa(personaVersion) {
		return false, nil
	}
	var errs []error
	for _, name := range appDeviceIDFiles {
		if err := os.Remove(filepath.Join(dataDir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(marker, []byte(strconv.Itoa(personaVersion)+"\n"), 0o600)
}
