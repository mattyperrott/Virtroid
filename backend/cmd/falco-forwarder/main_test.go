package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
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
