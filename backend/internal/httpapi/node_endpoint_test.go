package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"virtroid/backend/internal/config"
	"virtroid/backend/internal/store"
)

const testPin = "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="

func TestValidateNodePublicEndpoint(t *testing.T) {
	allowed := []string{"203.0.113.10"}
	if addr, pin, err := validateNodePublicEndpoint("203.0.113.10:8443", testPin, allowed); err != nil || addr != "203.0.113.10:8443" || pin != testPin {
		t.Fatalf("valid endpoint = %q %q %v", addr, pin, err)
	}
	if addr, pin, err := validateNodePublicEndpoint("", "", allowed); err != nil || addr != "" || pin != "" {
		t.Fatalf("absent endpoint = %q %q %v, want empty and no error", addr, pin, err)
	}
	for name, input := range map[string][2]string{
		"address without pin":    {"203.0.113.10:8443", ""},
		"pin without address":    {"", testPin},
		"pin is not base64":      {"203.0.113.10:8443", "not-a-pin"},
		"pin is not a SHA-256":   {"203.0.113.10:8443", base64.StdEncoding.EncodeToString([]byte("short"))},
		"hostname, not an IP":    {"node.example.com:8443", testPin},
		"missing port":           {"203.0.113.10", testPin},
		"IP not on allowlist":    {"10.0.0.5:8443", testPin},
		"port out of range":      {"203.0.113.10:70000", testPin},
		"internal docker target": {"127.0.0.1:5432", testPin},
	} {
		if _, _, err := validateNodePublicEndpoint(input[0], input[1], allowed); err == nil {
			t.Errorf("%s: accepted %q %q", name, input[0], input[1])
		}
	}
	if _, _, err := validateNodePublicEndpoint("203.0.113.10:8443", testPin, nil); err == nil {
		t.Error("accepted a remote node with no remote allowlist configured")
	}
}

func TestRemoteNodeAdvertiseAddrMustMatchItsPublicAddress(t *testing.T) {
	if addr, err := remoteNodeAdvertiseAddr("203.0.113.10", "203.0.113.10:8443"); err != nil || addr != "203.0.113.10" {
		t.Fatalf("matching advertise address = %q %v", addr, err)
	}
	for _, advertise := range []string{"virtnoded", "10.0.0.5", "127.0.0.1"} {
		if _, err := remoteNodeAdvertiseAddr(advertise, "203.0.113.10:8443"); err == nil {
			t.Errorf("remote node advertise %q accepted for public 203.0.113.10", advertise)
		}
	}
}

func TestPinnedNodeClientChecksTheNodeKey(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	sum := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	rightPin := base64.StdEncoding.EncodeToString(sum[:])

	api := &API{}
	response, err := api.pinnedNodeClient(rightPin).Get(server.URL)
	if err != nil {
		t.Fatalf("matching pin rejected: %v", err)
	}
	response.Body.Close()

	if _, err := api.pinnedNodeClient(testPin).Get(server.URL); err == nil || !strings.Contains(err.Error(), "pinned key") {
		t.Fatalf("wrong pin error = %v, want a pin mismatch", err)
	}
}

func TestNodeTargetsUseDirectEndpointOnlyForPublicNodes(t *testing.T) {
	api := &API{cfg: config.ServerConfig{
		AppEnv:                    "production",
		NodeAllowedAdvertiseAddrs: []string{"virtnoded"},
		NodeAllowedRemoteAddrs:    []string{"203.0.113.10"},
		PublicBaseURL:             "https://virtroid.network",
	}}

	local := store.Host{AdvertiseAddr: "virtnoded", RelayPort: 8090}
	baseURL, _, err := api.nodeCallbackTarget(local)
	if err != nil || baseURL != "http://virtnoded:8090" {
		t.Fatalf("local callback target = %q %v", baseURL, err)
	}
	endpoint, pin, err := api.relayEndpointForHost(local)
	if err != nil || endpoint.Host != "virtroid.network" || endpoint.Port != 443 || pin != "" {
		t.Fatalf("local relay endpoint = %+v pin=%q err=%v, want the edge", endpoint, pin, err)
	}

	remote := store.Host{AdvertiseAddr: "203.0.113.10", PublicAddr: "203.0.113.10:41234", TLSSPKISHA256: testPin}
	baseURL, _, err = api.nodeCallbackTarget(remote)
	if err != nil || baseURL != "https://203.0.113.10:41234" {
		t.Fatalf("remote callback target = %q %v", baseURL, err)
	}
	endpoint, pin, err = api.relayEndpointForHost(remote)
	if err != nil || endpoint.Address != "203.0.113.10:41234" || !endpoint.TLS || pin != testPin {
		t.Fatalf("remote relay endpoint = %+v pin=%q err=%v, want the node", endpoint, pin, err)
	}
	host, port, _ := net.SplitHostPort(endpoint.Address)
	if host != endpoint.Host || port != strconv.Itoa(endpoint.Port) {
		t.Fatalf("remote relay endpoint parts disagree: %+v", endpoint)
	}

	rejected := store.Host{AdvertiseAddr: "203.0.113.10", PublicAddr: "10.0.0.5:41234", TLSSPKISHA256: testPin}
	if _, _, err := api.nodeCallbackTarget(rejected); err == nil {
		t.Fatal("callback target accepted a public address outside the allowlist")
	}
}

func TestValidRegion(t *testing.T) {
	for _, region := range []string{"", "eu", "ap-southeast", "us-1"} {
		if !store.ValidRegion(region) {
			t.Errorf("%q rejected", region)
		}
	}
	for _, region := range []string{"EU", "-eu", "eu west", "eu_west", strings.Repeat("a", 33)} {
		if store.ValidRegion(region) {
			t.Errorf("%q accepted", region)
		}
	}
}
