//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

// viewerSocketBuffer bounds what each socket on the viewer's path may hold.
// Video that cannot leave waits in the encoder, which then skips frames,
// instead of piling up in buffers where every new frame queues behind it.
const viewerSocketBuffer = 64 << 10

// oPath is O_PATH, which Go's syscall package defines on arm64 but not on
// amd64; the value is the same on both.
const oPath = 0x200000

// tcpNotSentLowat (TCP_NOTSENT_LOWAT) caps unsent data in a TCP socket's
// buffer, which otherwise grows to megabytes on a long path.
const (
	tcpNotSentLowat      = 25
	viewerNotSentLowater = 128 << 10
)

// dialViewerSocket connects to the runtime's encrypted viewer socket in its
// userdata, which the node shares with the container, rather than through
// docker exec and nc: one hop, with small buffers. Everything below dataDir
// belongs to the guest, so each directory is opened without following links
// and the socket is connected through the descriptor that pinned it.
func dialViewerSocket(dataDir string) (*net.UnixConn, error) {
	dir, err := syscall.Open(dataDir, oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open runtime data: %w", err)
	}
	for _, name := range []string{"local", "tmp", "virtroid-viewer"} {
		next, err := syscall.Openat(dir, name, oPath|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(dir)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		dir = next
	}
	socket, err := syscall.Openat(dir, "viewer.sock", oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	_ = syscall.Close(dir)
	if err != nil {
		return nil, fmt.Errorf("open viewer socket: %w", err)
	}
	defer syscall.Close(socket)
	var stat syscall.Stat_t
	if err := syscall.Fstat(socket, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFSOCK {
		return nil, errors.New("viewer socket is not a socket")
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d", socket), Net: "unix"})
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(viewerSocketBuffer)
	_ = conn.SetWriteBuffer(viewerSocketBuffer)
	return conn, nil
}

// limitUnsentData keeps a phone's TCP connection from buffering more than
// viewerNotSentLowater of video it has not sent yet.
func limitUnsentData(conn net.Conn) {
	raw, ok := conn.(syscall.Conn)
	if !ok {
		return
	}
	control, err := raw.SyscallConn()
	if err != nil {
		return
	}
	_ = control.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpNotSentLowat, viewerNotSentLowater)
	})
}
