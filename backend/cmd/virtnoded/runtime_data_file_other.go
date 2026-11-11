//go:build !linux

package main

import (
	"errors"
	"os"
)

// writeRuntimeDataFile needs openat2: runtimes run on Linux only.
func writeRuntimeDataFile(string, string, []byte, os.FileMode, int, int) error {
	return errors.ErrUnsupported
}
