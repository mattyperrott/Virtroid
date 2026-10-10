package httpapi

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"virtroid/backend/internal/store"
)

// nodeHasPublicEndpoint reports whether phones and the control plane reach the
// node directly at its public TLS address instead of through the local edge.
func nodeHasPublicEndpoint(host store.Host) bool {
	return host.PublicAddr != "" && host.TLSSPKISHA256 != ""
}

// nodeCallbackTarget returns the base URL and client for calling a node. A node
// with a public endpoint is called over TLS and authenticated by its pinned
// key; a local node keeps its allowlisted private address.
func (a *API) nodeCallbackTarget(host store.Host) (string, *http.Client, error) {
	if nodeHasPublicEndpoint(host) {
		addr, pin, err := validateNodePublicEndpoint(host.PublicAddr, host.TLSSPKISHA256, a.cfg.NodeAllowedAdvertiseAddrs, a.cfg.AppEnv)
		if err != nil {
			return "", nil, err
		}
		return "https://" + addr, a.pinnedNodeClient(pin), nil
	}
	advertiseAddr, err := validateNodeAdvertiseAddr(host.AdvertiseAddr, a.cfg.NodeAllowedAdvertiseAddrs, a.cfg.AppEnv)
	if err != nil {
		return "", nil, err
	}
	port := host.RelayPort
	if port <= 0 {
		port = 8090
	}
	return "http://" + net.JoinHostPort(advertiseAddr, strconv.Itoa(port)), a.outboundClient(), nil
}

var pinnedNodeClients sync.Map // pin -> *http.Client

func (a *API) pinnedNodeClient(pin string) *http.Client {
	if client, ok := pinnedNodeClients.Load(pin); ok {
		return client.(*http.Client)
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 4,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				// The node certificate is self-signed; the pin below is the
				// whole trust decision, so chain and hostname checks are skipped.
				InsecureSkipVerify: true,
				VerifyConnection: func(state tls.ConnectionState) error {
					return verifySPKIPin(state, pin)
				},
			},
		},
	}
	actual, _ := pinnedNodeClients.LoadOrStore(pin, client)
	return actual.(*http.Client)
}

func verifySPKIPin(state tls.ConnectionState, pin string) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("node presented no certificate")
	}
	sum := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
	if base64.StdEncoding.EncodeToString(sum[:]) != pin {
		return errors.New("node certificate does not match its pinned key")
	}
	return nil
}

// relayEndpointForHost is where a phone connects for a session on host: the
// node itself when it has a public endpoint, otherwise the public edge.
func (a *API) relayEndpointForHost(host store.Host) (publicRelayEndpoint, string, error) {
	if !nodeHasPublicEndpoint(host) {
		endpoint, err := a.publicRelayEndpoint()
		return endpoint, "", err
	}
	addr, pin, err := validateNodePublicEndpoint(host.PublicAddr, host.TLSSPKISHA256, a.cfg.NodeAllowedAdvertiseAddrs, a.cfg.AppEnv)
	if err != nil {
		return publicRelayEndpoint{}, "", err
	}
	hostPart, portPart, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portPart)
	return publicRelayEndpoint{
		Host:    hostPart,
		Port:    port,
		Scheme:  "https",
		TLS:     true,
		Address: addr,
	}, pin, nil
}
