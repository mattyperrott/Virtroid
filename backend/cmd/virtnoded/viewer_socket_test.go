//go:build linux

package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func viewerTree(t *testing.T) (dataDir, socketDir string) {
	t.Helper()
	dataDir = filepath.Join(t.TempDir(), "data")
	socketDir = filepath.Join(dataDir, "local", "tmp", "virtroid-viewer")
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dataDir, socketDir
}

func listenViewer(t *testing.T, path, greeting string) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte(greeting))
			conn.Close()
		}
	}()
}

func TestDialViewerSocketReachesTheRuntimesSocket(t *testing.T) {
	dataDir, socketDir := viewerTree(t)
	listenViewer(t, filepath.Join(socketDir, "viewer.sock"), "viewer")
	conn, err := dialViewerSocket(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, _ := io.ReadAll(conn)
	if string(got) != "viewer" {
		t.Fatalf("read %q", got)
	}
}

// The guest owns the tree under its data directory, so links it places
// there must not lead the node anywhere else.
func TestDialViewerSocketRefusesLinks(t *testing.T) {
	elsewhere := t.TempDir()
	listenViewer(t, filepath.Join(elsewhere, "viewer.sock"), "elsewhere")

	dataDir, socketDir := viewerTree(t)
	if err := os.Symlink(filepath.Join(elsewhere, "viewer.sock"), filepath.Join(socketDir, "viewer.sock")); err != nil {
		t.Fatal(err)
	}
	if conn, err := dialViewerSocket(dataDir); err == nil {
		conn.Close()
		t.Fatal("followed a linked socket")
	}

	dataDir, _ = viewerTree(t)
	tmp := filepath.Join(dataDir, "local", "tmp")
	if err := os.RemoveAll(tmp); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(elsewhere, "tree")
	if err := os.MkdirAll(filepath.Join(linked, "virtroid-viewer"), 0o700); err != nil {
		t.Fatal(err)
	}
	listenViewer(t, filepath.Join(linked, "virtroid-viewer", "viewer.sock"), "elsewhere")
	if err := os.Symlink(linked, tmp); err != nil {
		t.Fatal(err)
	}
	if conn, err := dialViewerSocket(dataDir); err == nil {
		conn.Close()
		t.Fatal("followed a linked directory")
	}

	dataDir, socketDir = viewerTree(t)
	if err := os.WriteFile(filepath.Join(socketDir, "viewer.sock"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if conn, err := dialViewerSocket(dataDir); err == nil {
		conn.Close()
		t.Fatal("connected to a file that is not a socket")
	}
}
