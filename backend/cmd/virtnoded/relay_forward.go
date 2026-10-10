package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const remoteRelayHandshakeTimeout = 15 * time.Second

type relayRoute struct {
	HostID        string `json:"host_id"`
	PublicAddr    string `json:"public_addr"`
	TLSSPKISHA256 string `json:"tls_spki_sha256"`
}

// fetchRelayRoute asks the control plane which node hosts a session without
// consuming the relay token.
func (n *nodeAgent) fetchRelayRoute(ctx context.Context, sessionID, relayToken string) (relayRoute, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		n.cfg.ControlPlaneURL+"/api/v1/internal/sessions/"+url.PathEscape(sessionID)+"/relay-route",
		nil,
	)
	if err != nil {
		return relayRoute{}, err
	}
	if err := n.signControlPlaneRequest(req, nil, false); err != nil {
		return relayRoute{}, err
	}
	req.Header.Set("X-Virtroid-Relay-Token", relayToken)

	resp, err := n.controlPlane.Do(req)
	if err != nil {
		return relayRoute{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return relayRoute{}, fmt.Errorf("resolve relay route: status=%d", resp.StatusCode)
	}
	var route relayRoute
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&route); err != nil {
		return relayRoute{}, err
	}
	return route, nil
}

// forwardRelayToNode carries a phone's relay connection to the remote node that
// hosts the session, for phones that cannot reach that node directly. The
// stream is end-to-end encrypted between phone and runtime, so this node only
// moves opaque bytes; the remote node validates and consumes the relay token.
func (n *nodeAgent) forwardRelayToNode(w http.ResponseWriter, r *http.Request, sessionID, relayToken string, route relayRoute) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	upstream, err := tls.DialWithDialer(dialer, "tcp", route.PublicAddr, &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Remote nodes use self-signed certificates; the pin is the whole trust decision.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			return verifyNodePin(state, route.TLSSPKISHA256)
		},
	})
	if err != nil {
		http.Error(w, "remote runtime node is unreachable", http.StatusBadGateway)
		return
	}
	_ = upstream.SetDeadline(time.Now().Add(remoteRelayHandshakeTimeout))
	request := "GET /api/v1/relay/" + url.PathEscape(sessionID) + " HTTP/1.1\r\n" +
		"Host: " + route.PublicAddr + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: virtroid-relay\r\n" +
		"X-Virtroid-Relay-Token: " + relayToken + "\r\n\r\n"
	if _, err := io.WriteString(upstream, request); err != nil {
		upstream.Close()
		http.Error(w, "remote runtime node closed the connection", http.StatusBadGateway)
		return
	}
	upstreamReader := bufio.NewReader(upstream)
	resp, err := http.ReadResponse(upstreamReader, nil)
	if err != nil {
		upstream.Close()
		http.Error(w, "remote runtime node sent an invalid response", http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		upstream.Close()
		http.Error(w, strings.TrimSpace(string(message)), resp.StatusCode)
		return
	}
	_ = upstream.SetDeadline(time.Time{})

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "relay hijacking unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, clientRW, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	statusLine := "HTTP/1.1 200 Connection Established\r\n\r\n"
	if r.Method != http.MethodConnect {
		statusLine = "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: virtroid-relay\r\n\r\n"
	}
	if _, err := clientRW.WriteString(statusLine); err != nil || clientRW.Flush() != nil {
		clientConn.Close()
		upstream.Close()
		return
	}

	deadline := time.Now().Add(maxRelaySessionDuration)
	_ = clientConn.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	defer clientConn.Close()
	defer upstream.Close()

	clientToNodeDone := make(chan struct{})
	go func() {
		defer close(clientToNodeDone)
		// clientRW.Reader holds anything the phone sent after its request.
		_, _ = io.Copy(upstream, io.LimitReader(clientRW.Reader, maxRelayBytesPerDirection))
		_ = upstream.CloseWrite()
	}()
	// upstreamReader holds anything the node sent right after its response.
	_, _ = io.Copy(clientConn, io.LimitReader(upstreamReader, maxRelayBytesPerDirection))
	_ = clientConn.Close()
	<-clientToNodeDone
}

func verifyNodePin(state tls.ConnectionState, pin string) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("remote node presented no certificate")
	}
	if spkiPin(state.PeerCertificates[0]) != pin {
		return errors.New("remote node certificate does not match its pinned key")
	}
	return nil
}
