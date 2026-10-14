package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The node installs a runtime's default and selected apps once: it records
// what it installed, and an app the user later uninstalls stays uninstalled
// until the user selects it again. The record lives in the runtime's own
// /data, so it travels in the runtime's snapshots to its next VM, and a wiped
// runtime starts without one.

const installedAppRecordVersion = 1

type installedAppRecord struct {
	Version int                     `json:"version"`
	Apps    map[string]installedApp `json:"apps"`
}

type installedApp struct {
	// SelectedAt is when the selection it was installed for was made; zero
	// for apps every runtime gets.
	SelectedAt  time.Time `json:"selected_at,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

func (n *nodeAgent) installedAppRecordPath(runtimeID string) string {
	return filepath.Join(n.cfg.RuntimeRoot, runtimeID, "data", "virtroid", "installed-apps.json")
}

// loadInstalledAppRecord reads the runtime's record. A missing or unreadable
// record is empty: at worst an app the user removed is installed once more.
func (n *nodeAgent) loadInstalledAppRecord(runtimeID string) installedAppRecord {
	record := installedAppRecord{Version: installedAppRecordVersion, Apps: map[string]installedApp{}}
	raw, err := os.ReadFile(n.installedAppRecordPath(runtimeID))
	if err != nil {
		return record
	}
	var stored installedAppRecord
	if json.Unmarshal(raw, &stored) != nil || stored.Version != installedAppRecordVersion {
		return record
	}
	for packageName, app := range stored.Apps {
		if appPackageNamePattern.MatchString(packageName) {
			record.Apps[packageName] = app
		}
	}
	return record
}

func (n *nodeAgent) saveInstalledAppRecord(runtimeID string, record installedAppRecord) error {
	path := n.installedAppRecordPath(runtimeID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save installed app record: %w", err)
	}
	return nil
}

// note records that the app is on the runtime for the given selection and
// reports whether the record changed.
func (r installedAppRecord) note(packageName string, selectedAt time.Time, now time.Time) bool {
	if existing, ok := r.Apps[packageName]; ok && existing.SelectedAt.Equal(selectedAt) {
		return false
	}
	r.Apps[packageName] = installedApp{SelectedAt: selectedAt, InstalledAt: now.UTC()}
	return true
}

// removedByUser is true for an app the node installed (or found installed)
// for this selection or a later one that is no longer there: the user
// uninstalled it, and only selecting it again brings it back.
func (r installedAppRecord) removedByUser(packageName string, selectedAt time.Time) bool {
	existing, ok := r.Apps[packageName]
	return ok && !selectedAt.After(existing.SelectedAt)
}
