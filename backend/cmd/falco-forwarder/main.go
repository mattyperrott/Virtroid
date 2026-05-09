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
	"syscall"
	"time"

	"virtroid/backend/internal/nodeauth"
)

const (
	defaultControlPlaneURL = "http://virtroidd:8080"
	defaultEventsFile      = "/var/log/falco/events.jsonl"
	securityEventsPath     = "/api/v1/internal/security/events"
	maxFalcoLineBytes      = 1 << 20
)

type falcoEvent struct {
	Time     string   `json:"time"`
	Source   string   `json:"source"`
	Rule     string   `json:"rule"`
	Priority string   `json:"priority"`
	Output   string   `json:"output"`
	Tags     []string `json:"tags"`
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

	eventsFile := envOrDefault("FALCO_EVENTS_FILE", defaultEventsFile)
	tailFromStart := parseBool(os.Getenv("FALCO_FORWARD_TAIL_FROM_START"))
	f := &forwarder{
		endpoint:   endpoint,
		nodeID:     nodeID,
		privateKey: privateKey,
		client:     &http.Client{Timeout: 10 * time.Second},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
