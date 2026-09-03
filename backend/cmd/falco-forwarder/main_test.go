package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestForwardLineAppliesPerMinuteLimit(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	f := testForwarder(t, server.URL)
	f.limiter = newEventLimiter(2)
	f.deduper = newEventDeduper(0)

	line := []byte(`{"time":"2026-05-09T10:00:00Z","source":"syscall","rule":"Virtroid Shell Spawned In Managed Container","priority":"ERROR","output":"shell spawned","output_fields":{"container.name":"virtroid-runtime","proc.name":"sh","proc.cmdline":"/system/bin/sh"}}`)
	for i := 0; i < 3; i++ {
		if err := f.forwardLine(context.Background(), line); err != nil {
			t.Fatalf("forwardLine %d: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(&posts); got != 2 {
		t.Fatalf("forwarded posts = %d, want 2 after per-minute limit", got)
	}
}

func TestForwardLineDeduplicatesRepeatedFalcoEvents(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	f := testForwarder(t, server.URL)
	f.limiter = newEventLimiter(0)
	f.deduper = newEventDeduper(time.Minute)

	line := []byte(`{"time":"2026-05-09T10:00:00Z","source":"syscall","rule":"Virtroid Shell Spawned In Managed Container","priority":"ERROR","output":"shell spawned pid=1","output_fields":{"container.name":"virtroid-runtime","proc.name":"sh","proc.cmdline":"/system/bin/sh"}}`)
	for i := 0; i < 2; i++ {
		if err := f.forwardLine(context.Background(), line); err != nil {
			t.Fatalf("forwardLine %d: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(&posts); got != 1 {
		t.Fatalf("forwarded posts = %d, want 1 after dedupe", got)
	}
}

func TestForwardSuricataLineReducesNetworkEventBeforeUpload(t *testing.T) {
	var received securityEventPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Errorf("decode body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	f := testForwarder(t, server.URL)
	f.limiter = newEventLimiter(0)
	f.deduper = newEventDeduper(0)
	line := []byte(`{"timestamp":"2026-09-03T10:00:00.000000+0000","event_type":"alert","src_ip":"203.0.113.5","dest_ip":"10.0.0.2","proto":"TCP","alert":{"signature":"Virtroid inbound TCP SYN scan","category":"Attempted Information Leak","severity":3}}`)
	if err := f.forwardSuricataLine(context.Background(), line); err != nil {
		t.Fatal(err)
	}
	if received.Source != "suricata" || received.Priority != "notice" {
		t.Fatalf("received source/priority = %q/%q", received.Source, received.Priority)
	}
	if strings.Contains(string(received.Event), "src_ip") || strings.Contains(string(received.Event), "dest_ip") {
		t.Fatalf("reduced event leaked network addresses: %s", received.Event)
	}
}

func TestForwardSuricataLineIgnoresParserAnomalies(t *testing.T) {
	var posts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	f := testForwarder(t, server.URL)
	f.limiter = newEventLimiter(0)
	f.deduper = newEventDeduper(0)
	line := []byte(`{"timestamp":"2026-09-03T10:00:00.000000+0000","event_type":"anomaly","proto":"TCP","anomaly":{"type":"applayer","event":"RESPONSE_BODY_UNEXPECTED"}}`)
	if err := f.forwardSuricataLine(context.Background(), line); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&posts); got != 0 {
		t.Fatalf("forwarded posts = %d, want 0 for a parser anomaly", got)
	}
}

func testForwarder(t *testing.T, endpoint string) *forwarder {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return &forwarder{
		endpoint:   endpoint,
		nodeID:     "node-1",
		privateKey: privateKey,
		client:     serverClient(),
	}
}

func serverClient() *http.Client {
	return &http.Client{Timeout: time.Second}
}
