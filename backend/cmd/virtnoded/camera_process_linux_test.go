//go:build linux

package main

import (
	"os/exec"
	"slices"
	"testing"
)

func TestHardenCameraCommandDropsNodeCredentials(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := hardenCameraCommand(cmd, "/dev/null"); err != nil {
		t.Fatalf("harden camera command: %v", err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatal("camera command has no isolated credential")
	}
	credential := cmd.SysProcAttr.Credential
	if credential.Uid != isolatedCameraProcessID || len(credential.Groups) != 1 {
		t.Fatalf("camera credential = %+v", credential)
	}
	if credential.Groups[0] != credential.Gid {
		t.Fatalf("camera supplementary groups = %v, primary gid = %d", credential.Groups, credential.Gid)
	}
	if !cmd.SysProcAttr.Noctty || cmd.SysProcAttr.Pdeathsig == 0 || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("camera process attributes = %+v", cmd.SysProcAttr)
	}
	if slices.ContainsFunc(cmd.Env, func(value string) bool {
		return value != "HOME=/nonexistent" && value != "LANG=C" && value != "LC_ALL=C" && value != "PATH=/usr/bin:/bin"
	}) {
		t.Fatalf("camera command inherited an unexpected environment: %v", cmd.Env)
	}
}
