//go:build !linux

package main

import (
	"errors"
	"os/exec"
)

func hardenCameraCommand(_ *exec.Cmd, _ string) error {
	return errors.New("camera passthrough requires Linux process isolation")
}
