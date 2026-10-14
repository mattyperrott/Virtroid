//go:build !linux

package main

import (
	"errors"
	"net"
)

func dialViewerSocket(string) (*net.UnixConn, error) {
	return nil, errors.ErrUnsupported
}

func limitUnsentData(net.Conn) {}
