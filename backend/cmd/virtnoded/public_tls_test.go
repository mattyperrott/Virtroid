package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNodeTLSIdentityIsStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	first, firstPin, err := loadOrCreateNodeTLS(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	_, secondPin, err := loadOrCreateNodeTLS(dir, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if firstPin == "" || firstPin != secondPin {
		t.Fatalf("pin changed across restarts: %q then %q", firstPin, secondPin)
	}
	if got := spkiPin(first.Leaf); got != firstPin {
		t.Fatalf("returned pin %q does not match certificate pin %q", firstPin, got)
	}
}

func TestValidNodePublicAddr(t *testing.T) {
	for _, addr := range []string{"203.0.113.10:8443", "[2001:db8::1]:443"} {
		if err := validNodePublicAddr(addr); err != nil {
			t.Errorf("%s rejected: %v", addr, err)
		}
	}
	for _, addr := range []string{"node.example.com:8443", "203.0.113.10", "203.0.113.10:0", "203.0.113.10:70000", ""} {
		if err := validNodePublicAddr(addr); err == nil {
			t.Errorf("%s accepted, want rejection", addr)
		}
	}
}

func TestPublicNodeHandlerExposesOnlyPeerRoutes(t *testing.T) {
	private := http.NewServeMux()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	for _, pattern := range []string{
		"GET /healthz",
		"GET /metrics",
		"/capabilities",
		"POST /api/v1/internal/viewer/prepare",
		"GET /api/v1/relay/{id}",
	} {
		private.Handle(pattern, ok)
	}
	public := publicNodeHandler(private)

	for path, want := range map[string]int{
		"GET /healthz":                             http.StatusTeapot,
		"POST /api/v1/internal/viewer/prepare":     http.StatusTeapot,
		"GET /api/v1/relay/session-1":              http.StatusTeapot,
		"GET /metrics":                             http.StatusNotFound,
		"GET /capabilities":                        http.StatusNotFound,
		"POST /api/v1/internal/runtimes/x/unknown": http.StatusNotFound,
	} {
		method, target, _ := strings.Cut(path, " ")
		recorder := httptest.NewRecorder()
		public.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
		if recorder.Code != want {
			t.Errorf("%s = %d, want %d", path, recorder.Code, want)
		}
	}
}
