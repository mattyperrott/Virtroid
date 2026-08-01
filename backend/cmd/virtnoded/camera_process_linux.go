//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

const isolatedCameraProcessID uint32 = 65534

// hardenCameraCommand keeps the untrusted JPEG decoder out of the node's root
// and Docker-group identity. The child receives no node credentials in its
// environment and only the group needed to write the configured V4L2 device.
func hardenCameraCommand(cmd *exec.Cmd, device string) error {
	if cmd == nil {
		return errors.New("camera command is required")
	}
	info, err := os.Stat(device)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("camera device ownership is unavailable")
	}

	deviceGroup := isolatedCameraProcessID
	permissions := info.Mode().Perm()
	if permissions&0o020 != 0 {
		deviceGroup = stat.Gid
	} else if permissions&0o002 == 0 {
		return errors.New("camera device is not writable by an isolated decoder group")
	}
	if dockerInfo, dockerErr := os.Stat("/var/run/docker.sock"); dockerErr == nil {
		if dockerStat, dockerOK := dockerInfo.Sys().(*syscall.Stat_t); dockerOK && dockerStat.Gid == deviceGroup {
			return errors.New("camera device group must not grant Docker socket access")
		}
	}

	cmd.Env = []string{
		"HOME=/nonexistent",
		"LANG=C",
		"LC_ALL=C",
		"PATH=/usr/bin:/bin",
	}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    isolatedCameraProcessID,
			Gid:    deviceGroup,
			Groups: []uint32{deviceGroup},
		},
		Noctty:    true,
		Pdeathsig: syscall.SIGKILL,
		Setpgid:   true,
	}
	return nil
}
