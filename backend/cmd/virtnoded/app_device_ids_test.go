package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppDeviceIDResetOnlyForTheFreshIdentityStart(t *testing.T) {
	five, six := 5, 6
	if wantsAppDeviceIDReset(runtimeAssignment{PersonaVersion: 6}) {
		t.Fatal("a start that never asked reset the IDs")
	}
	if !wantsAppDeviceIDReset(runtimeAssignment{PersonaVersion: 6, IdentityResetPersonaVersion: &six}) {
		t.Fatal("the fresh-identity start kept the IDs")
	}
	if wantsAppDeviceIDReset(runtimeAssignment{PersonaVersion: 6, IdentityResetPersonaVersion: &five}) {
		t.Fatal("a later ordinary start reset the IDs again")
	}
}

func TestResetAppDeviceIDsRemovesOnlyTheIDFiles(t *testing.T) {
	dataDir := t.TempDir()
	users := filepath.Join(dataDir, "system/users/0")
	if err := os.MkdirAll(users, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings_ssaid.xml", "settings_ssaid.xml.fallback", "settings_secure.xml"} {
		if err := os.WriteFile(filepath.Join(users, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if reset, err := resetAppDeviceIDs(dataDir, 6); err != nil || !reset {
		t.Fatalf("resetAppDeviceIDs = %v, %v", reset, err)
	}
	for name, want := range map[string]bool{"settings_ssaid.xml": false, "settings_ssaid.xml.fallback": false, "settings_secure.xml": true} {
		if _, err := os.Stat(filepath.Join(users, name)); (err == nil) != want {
			t.Fatalf("%s present = %v, want %v", name, err == nil, want)
		}
	}
	// Android wrote new IDs; rebuilding the same start must keep them.
	if err := os.WriteFile(filepath.Join(users, "settings_ssaid.xml"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reset, err := resetAppDeviceIDs(dataDir, 6); err != nil || reset {
		t.Fatalf("the same start reset again: %v, %v", reset, err)
	}
	if _, err := os.Stat(filepath.Join(users, "settings_ssaid.xml")); err != nil {
		t.Fatalf("new IDs removed: %v", err)
	}
	if reset, err := resetAppDeviceIDs(dataDir, 9); err != nil || !reset {
		t.Fatalf("a later fresh-identity start = %v, %v", reset, err)
	}
	if reset, err := resetAppDeviceIDs(t.TempDir(), 1); err != nil || !reset {
		t.Fatalf("a runtime with no IDs yet: %v, %v", reset, err)
	}
}
