package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeRemoteNode accepts one relay upgrade carrying the expected token and then
// echoes bytes back, like a runtime stream would answer.
func fakeRemoteNode(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/relay/session-1" || r.Header.Get("X-Virtroid-Relay-Token") != wantToken {
			http.Error(w, "relay target not found", http.StatusNotFound)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: virtroid-relay\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw.Reader)
	}))
}

func forwardingNode(route relayRoute) *httptest.Server {
	node := &nodeAgent{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node.forwardRelayToNode(w, r, "session-1", r.Header.Get("X-Virtroid-Relay-Token"), route)
	}))
}

func dialRelay(t *testing.T, server *httptest.Server, token string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /api/v1/relay/session-1 HTTP/1.1\r\nHost: edge\r\nConnection: Upgrade\r\nUpgrade: virtroid-relay\r\nX-Virtroid-Relay-Token: "+token+"\r\n\r\n")
	reader := bufio.NewReader(conn)
	status, _ := reader.ReadString('\n')
	for {
		line, err := reader.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	return conn, reader, status
}

func TestForwardRelayToNodeCarriesTheStreamBothWays(t *testing.T) {
	remote := fakeRemoteNode(t, "token-1")
	defer remote.Close()
	route := relayRoute{
		HostID:        "remote",
		PublicAddr:    strings.TrimPrefix(remote.URL, "https://"),
		TLSSPKISHA256: spkiPin(remote.Certificate()),
	}
	edge := forwardingNode(route)
	defer edge.Close()

	conn, reader, status := dialRelay(t, edge, "token-1")
	defer conn.Close()
	if !strings.Contains(status, "101") {
		t.Fatalf("relay status = %q, want 101", status)
	}
	if _, err := io.WriteString(conn, "frame"); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 5)
	if _, err := io.ReadFull(reader, echo); err != nil || string(echo) != "frame" {
		t.Fatalf("echo = %q err=%v, want frame", echo, err)
	}
}

func TestForwardRelayToNodeRejectsWrongPinAndPassesNodeErrors(t *testing.T) {
	remote := fakeRemoteNode(t, "token-1")
	defer remote.Close()
	addr := strings.TrimPrefix(remote.URL, "https://")

	wrongPin := forwardingNode(relayRoute{HostID: "remote", PublicAddr: addr, TLSSPKISHA256: "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="})
	defer wrongPin.Close()
	conn, _, status := dialRelay(t, wrongPin, "token-1")
	conn.Close()
	if !strings.Contains(status, "502") {
		t.Fatalf("wrong pin status = %q, want 502", status)
	}

	rightPin := forwardingNode(relayRoute{HostID: "remote", PublicAddr: addr, TLSSPKISHA256: spkiPin(remote.Certificate())})
	defer rightPin.Close()
	conn, _, status = dialRelay(t, rightPin, "wrong-token")
	conn.Close()
	if !strings.Contains(status, "404") {
		t.Fatalf("rejected token status = %q, want the node's 404", status)
	}
}
