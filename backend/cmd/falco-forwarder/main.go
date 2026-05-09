package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"virtroid/backend/internal/nodeauth"
)

const (
	defaultControlPlaneURL    = "http://virtroidd:8080"
	defaultForwarderBindAddr  = ":8766"
	securityEventsPath        = "/api/v1/internal/security/events"
	maxFalcoLineBytes         = 1 << 20
	defaultMaxEventsPerMinute = 120
	defaultDedupWindow        = 5 * time.Second
)

type falcoEvent struct {
	Time         string         `json:"time"`
	Source       string         `json:"source"`
	Rule         string         `json:"rule"`
	Priority     string         `json:"priority"`
	Output       string         `json:"output"`
	Tags         []string       `json:"tags"`
	OutputFields map[string]any `json:"output_fields"`
}

type securityEventPayload struct {
	Time     *time.Time      `json:"time,omitempty"`
	Source   string          `json:"source"`
	Rule     string          `json:"rule"`
	Priority string          `json:"priority"`
	Output   string          `json:"output"`
	Tags     []string        `json:"tags"`
	Event    json.RawMessage `json:"event"`
}

type forwarder struct {
	endpoint   string
	nodeID     string
	privateKey *ecdsa.PrivateKey
	client     *http.Client
	limiter    *eventLimiter
	deduper    *eventDeduper
}

type eventLimiter struct {
	mu          sync.Mutex
	max         int
	windowStart time.Time
	count       int
	dropped     int
}

type eventDeduper struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]time.Time
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	privateKey, _, err := nodeauth.LoadPrivateKey(os.Getenv("NODE_PRIVATE_KEY_B64"))
	if err != nil {
		log.Fatalf("load node private key: %v", err)
	}
	if privateKey == nil {
		log.Fatal("NODE_PRIVATE_KEY_B64 is required")
	}

	nodeID := strings.TrimSpace(envOrDefault("NODE_ID", ""))
	if nodeID == "" {
		log.Fatal("NODE_ID is required")
	}

	endpoint, err := securityEventsEndpoint(envOrDefault("CONTROL_PLANE_URL", defaultControlPlaneURL))
	if err != nil {
		log.Fatalf("control plane url: %v", err)
	}

	eventsFile := strings.TrimSpace(os.Getenv("FALCO_EVENTS_FILE"))
	bindAddr := envOrDefault("FALCO_FORWARDER_BIND_ADDR", defaultForwarderBindAddr)
	tailFromStart := parseBool(os.Getenv("FALCO_FORWARD_TAIL_FROM_START"))
	f := &forwarder{
		endpoint:   endpoint,
		nodeID:     nodeID,
		privateKey: privateKey,
		client:     &http.Client{Timeout: 10 * time.Second},
		limiter:    newEventLimiter(parseEnvInt("FALCO_FORWARD_MAX_EVENTS_PER_MINUTE", defaultMaxEventsPerMinute)),
		deduper:    newEventDeduper(parseEnvDuration("FALCO_FORWARD_DEDUP_WINDOW", defaultDedupWindow)),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if eventsFile == "" {
		log.Printf("listening for Falco HTTP events on %s and forwarding to %s as node %s", bindAddr, endpoint, nodeID)
		if err := f.serve(ctx, bindAddr); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("serve Falco HTTP forwarder: %v", err)
		}
		return
	}

	log.Printf("forwarding Falco events from %s to %s as node %s", eventsFile, endpoint, nodeID)
	for {
		if err := f.tail(ctx, eventsFile, tailFromStart); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("tail error: %v", err)
		}
		if ctx.Err() != nil {
			return
		}
		tailFromStart = false
		time.Sleep(2 * time.Second)
	}
}

func (f *forwarder) serve(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/falco", f.handleFalcoEvent)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (f *forwarder) handleFalcoEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFalcoLineBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			http.Error(w, "event body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read event body", http.StatusBadRequest)
		return
	}
	if err := f.forwardLine(r.Context(), bytes.TrimSpace(body)); err != nil {
		log.Printf("forward event: %v", err)
		http.Error(w, "forward event", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (f *forwarder) tail(ctx context.Context, path string, fromStart bool) error {
	_, statErr := os.Stat(path)
	missingAtStart := errors.Is(statErr, os.ErrNotExist)

	file, err := waitOpen(ctx, path)
	if err != nil {
		return err
	}
	defer file.Close()

	if !fromStart && !missingAtStart {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return err
		}
	}

	reader := bufio.NewReaderSize(file, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if len(line) > maxFalcoLineBytes {
				log.Printf("dropping oversized Falco event line: %d bytes", len(line))
			} else if err := f.forwardLine(ctx, bytes.TrimSpace(line)); err != nil {
				log.Printf("forward event: %v", err)
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}
		return err
	}
}

func (f *forwarder) forwardLine(ctx context.Context, line []byte) error {
	if len(line) == 0 {
		return nil
	}

	var event falcoEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return fmt.Errorf("parse Falco JSON: %w", err)
	}
	if strings.TrimSpace(event.Rule) == "" && strings.TrimSpace(event.Output) == "" {
		return nil
	}
	now := time.Now().UTC()
	if f.deduper != nil && !f.deduper.allow(eventFingerprint(event), now) {
		return nil
	}
	if f.limiter != nil {
		if ok, dropped := f.limiter.allow(now); !ok {
			if dropped == 1 || dropped%defaultMaxEventsPerMinute == 0 {
				log.Printf("dropping Falco events after per-minute forward limit; dropped in current window=%d", dropped)
			}
			return nil
		}
	}

	payload := securityEventPayload{
		Time:     parseFalcoTime(event.Time),
		Source:   event.Source,
		Rule:     event.Rule,
		Priority: event.Priority,
		Output:   event.Output,
		Tags:     event.Tags,
		Event:    append(json.RawMessage(nil), line...),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if err := f.post(ctx, body); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return lastErr
}

func (f *forwarder) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	nonceBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		return err
	}
	if err := nodeauth.ApplySignedHeaders(
		req,
		f.privateKey,
		f.nodeID,
		body,
		"",
		"",
		strconv.FormatInt(time.Now().Unix(), 10),
		hex.EncodeToString(nonceBytes),
	); err != nil {
		return err
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("control plane status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
}

func newEventLimiter(max int) *eventLimiter {
	return &eventLimiter{max: max}
}

func (l *eventLimiter) allow(now time.Time) (bool, int) {
	if l == nil || l.max <= 0 {
		return true, 0
	}
	bucket := now.UTC().Truncate(time.Minute)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.windowStart.IsZero() || !l.windowStart.Equal(bucket) {
		l.windowStart = bucket
		l.count = 0
		l.dropped = 0
	}
	if l.count >= l.max {
		l.dropped++
		return false, l.dropped
	}
	l.count++
	return true, 0
}

func newEventDeduper(window time.Duration) *eventDeduper {
	return &eventDeduper{
		window: window,
		seen:   map[string]time.Time{},
	}
}

func (d *eventDeduper) allow(fingerprint string, now time.Time) bool {
	if d == nil || d.window <= 0 || strings.TrimSpace(fingerprint) == "" {
		return true
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if last, ok := d.seen[fingerprint]; ok && now.Sub(last) < d.window {
		return false
	}
	if len(d.seen) > 2048 {
		cutoff := now.Add(-d.window)
		for key, seenAt := range d.seen {
			if seenAt.Before(cutoff) {
				delete(d.seen, key)
			}
		}
	}
	d.seen[fingerprint] = now
	return true
}

func eventFingerprint(event falcoEvent) string {
	parts := []string{
		strings.TrimSpace(event.Source),
		strings.TrimSpace(event.Rule),
		strings.TrimSpace(event.Priority),
	}
	for _, key := range []string{
		"container.name",
		"container.id",
		"proc.name",
		"proc.cmdline",
		"evt.type",
	} {
		if value, ok := event.OutputFields[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				parts = append(parts, key+"="+text)
			}
		}
	}
	if len(parts) <= 3 {
		output := strings.TrimSpace(event.Output)
		if len(output) > 256 {
			output = output[:256]
		}
		parts = append(parts, output)
	}
	return strings.Join(parts, "\x00")
}

func waitOpen(ctx context.Context, path string) (*os.File, error) {
	for {
		file, err := os.Open(path)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func securityEventsEndpoint(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = defaultControlPlaneURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("CONTROL_PLANE_URL must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + securityEventsPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func parseFalcoTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func parseEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseEnvDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
