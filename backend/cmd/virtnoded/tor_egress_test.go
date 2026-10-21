package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"virtroid/backend/internal/config"
)

func TestTorBridgeNameIsAShortLinuxInterfaceName(t *testing.T) {
	name := torBridgeName("11111111-1111-1111-1111-111111111111")
	if !strings.HasPrefix(name, torBridgePrefix) || len(name) > 15 {
		t.Fatalf("torBridgeName = %q", name)
	}
	if name == torBridgeName("22222222-2222-2222-2222-222222222222") {
		t.Fatal("two runtimes share a Tor bridge name")
	}
	if runtimeBridgeName(runtimeAssignment{ID: "x", EgressMode: "direct"}) != "" {
		t.Fatal("a direct runtime got a named bridge")
	}
}

func TestTorRuntimeRefusedWhereTorIsUnavailable(t *testing.T) {
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "per-runtime")
	runtime := runtimeAssignment{ID: "rt", EgressMode: "tor"}
	if err := (&nodeAgent{}).checkEgressSupported(runtime); !errors.Is(err, errTorEgressUnavailable) {
		t.Fatalf("no Tor client: %v", err)
	}
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "shared")
	if err := (&nodeAgent{cfg: config.NodeConfig{TorEgress: true}}).checkEgressSupported(runtime); !errors.Is(err, errTorEgressUnavailable) {
		t.Fatalf("shared bridge: %v", err)
	}
	if err := (&nodeAgent{}).checkEgressSupported(runtimeAssignment{ID: "rt"}); err != nil {
		t.Fatalf("direct runtime: %v", err)
	}
}

func TestEnsureRuntimeNetworkOnHostNamesTorBridgeWithoutJoiningIt(t *testing.T) {
	t.Setenv("NODE_RUNTIME_NETWORK_MODE", "per-runtime")
	t.Setenv("NODE_AGENT_CONTAINER_NAME", "")
	runtimeID := "11111111-1111-1111-1111-111111111111"
	node := &nodeAgent{cfg: config.NodeConfig{DockerNetworkName: "virtroid-guest", AgentOnHost: true}}
	var created map[string]any
	var calls []string
	node.docker = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		switch {
		case req.Method == http.MethodGet:
			// Built direct while the runtime was stopped.
			payload := `{"Name":"x","Labels":{"io.virtroid.managed":"true","io.virtroid.runtime":"` + runtimeID + `"},"Options":{},"Containers":{}}`
			if len(calls) > 2 {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(payload)), Header: make(http.Header)}, nil
		case req.Method == http.MethodDelete:
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader(``)), Header: make(http.Header)}, nil
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/networks/create"):
			if err := json.NewDecoder(req.Body).Decode(&created); err != nil {
				t.Fatalf("decode network create: %v", err)
			}
			return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		default:
			t.Fatalf("unexpected Docker request %s %s", req.Method, req.URL.Path)
			return nil, nil
		}
	})}

	bridge := torBridgeName(runtimeID)
	if _, err := node.ensureRuntimeNetwork(context.Background(), runtimeID, bridge); err != nil {
		t.Fatalf("ensureRuntimeNetwork: %v (calls %v)", err, calls)
	}
	options, _ := created["Options"].(map[string]any)
	if options[dockerBridgeNameOption] != bridge {
		t.Fatalf("network options = %#v, want bridge %q", created["Options"], bridge)
	}
	for _, call := range calls {
		if strings.HasSuffix(call, "/connect") {
			t.Fatalf("node on the host joined the runtime network: %v", calls)
		}
	}
	if !strings.HasPrefix(calls[len(calls)-1], "POST") || !strings.Contains(strings.Join(calls, ","), "DELETE") {
		t.Fatalf("calls = %v, want the direct network replaced", calls)
	}
}
