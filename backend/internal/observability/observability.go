package observability

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type traceContextKey struct{}

type TraceContext struct {
	TraceID string
	SpanID  string
	Sampled bool
}

type requestKey struct {
	Method string
	Route  string
	Status int
}

type requestValue struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}

type metricKey struct {
	Name   string
	Labels string
}

type Service struct {
	name     string
	mu       sync.RWMutex
	requests map[requestKey]*requestValue
	counters map[metricKey]uint64
	gauges   map[metricKey]float64
}

func New(serviceName string) *Service {
	return &Service{
		name:     normalizedMetricLabel(serviceName),
		requests: make(map[requestKey]*requestValue),
		counters: make(map[metricKey]uint64),
		gauges:   make(map[metricKey]float64),
	}
}

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace := incomingTrace(r.Header.Get("Traceparent"))
		if trace.TraceID == "" {
			trace.TraceID = randomHex(16)
		}
		trace.SpanID = randomHex(8)
		if trace.TraceID == "" || trace.SpanID == "" {
			trace = TraceContext{}
		}
		if trace.TraceID != "" {
			w.Header().Set("Traceparent", trace.Header())
			r = r.WithContext(context.WithValue(r.Context(), traceContextKey{}, trace))
		}

		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(recorder, r)
		duration := time.Since(started)
		route := r.Pattern
		if strings.TrimSpace(route) == "" {
			route = "unmatched"
		}
		s.observeRequest(r.Method, route, recorder.status, duration)
		if trace.TraceID != "" && (trace.Sampled || recorder.status >= 500) {
			log.Printf(
				"trace service=%s trace_id_hash=%s span_id=%s kind=server status=%d duration_ms=%d",
				s.name,
				logTraceID(trace.TraceID),
				trace.SpanID,
				recorder.status,
				duration.Milliseconds(),
			)
		}
	})
}

func (s *Service) Transport(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		trace, _ := req.Context().Value(traceContextKey{}).(TraceContext)
		if trace.TraceID == "" {
			trace.TraceID = randomHex(16)
			trace.Sampled = true
		}
		trace.SpanID = randomHex(8)
		if trace.TraceID != "" && trace.SpanID != "" {
			clone.Header.Set("Traceparent", trace.Header())
		}
		started := time.Now()
		resp, err := next.RoundTrip(clone)
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		s.Counter("virtroid_outbound_requests_total", map[string]string{
			"target": outboundTarget(clone),
			"status": strconv.Itoa(status),
		})
		if trace.TraceID != "" && (trace.Sampled || err != nil || status >= 500) {
			log.Printf(
				"trace service=%s trace_id_hash=%s span_id=%s kind=client target=%s status=%d duration_ms=%d",
				s.name,
				logTraceID(trace.TraceID),
				trace.SpanID,
				outboundTarget(clone),
				status,
				time.Since(started).Milliseconds(),
			)
		}
		return resp, err
	})
}

func (s *Service) Counter(name string, labels map[string]string) {
	key := metricKey{Name: normalizedMetricName(name), Labels: encodeLabels(labels)}
	s.mu.Lock()
	s.counters[key]++
	s.mu.Unlock()
}

func (s *Service) SetGauge(name string, value float64, labels map[string]string) {
	key := metricKey{Name: normalizedMetricName(name), Labels: encodeLabels(labels)}
	s.mu.Lock()
	s.gauges[key] = value
	s.mu.Unlock()
}

func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		s.writeMetrics(w)
	})
}

func (s *Service) observeRequest(method, route string, status int, duration time.Duration) {
	key := requestKey{Method: boundedHTTPMethod(method), Route: normalizedMetricLabel(route), Status: status}
	s.mu.Lock()
	value := s.requests[key]
	if value == nil {
		value = &requestValue{Buckets: make([]uint64, len(durationBuckets))}
		s.requests[key] = value
	}
	value.Count++
	seconds := duration.Seconds()
	value.Sum += seconds
	for index, boundary := range durationBuckets {
		if seconds <= boundary {
			value.Buckets[index]++
		}
	}
	s.mu.Unlock()
}

func (s *Service) writeMetrics(w http.ResponseWriter) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, _ = fmt.Fprintln(w, "# HELP virtroid_http_requests_total HTTP requests completed by the service.")
	_, _ = fmt.Fprintln(w, "# TYPE virtroid_http_requests_total counter")
	_, _ = fmt.Fprintln(w, "# HELP virtroid_http_request_duration_seconds HTTP request duration in seconds.")
	_, _ = fmt.Fprintln(w, "# TYPE virtroid_http_request_duration_seconds histogram")
	requestKeys := make([]requestKey, 0, len(s.requests))
	for key := range s.requests {
		requestKeys = append(requestKeys, key)
	}
	sort.Slice(requestKeys, func(i, j int) bool {
		left := requestKeys[i]
		right := requestKeys[j]
		return left.Method+left.Route+strconv.Itoa(left.Status) < right.Method+right.Route+strconv.Itoa(right.Status)
	})
	for _, key := range requestKeys {
		value := s.requests[key]
		labels := fmt.Sprintf("service=%q,method=%q,route=%q,status=%q", s.name, key.Method, key.Route, strconv.Itoa(key.Status))
		_, _ = fmt.Fprintf(w, "virtroid_http_requests_total{%s} %d\n", labels, value.Count)
		for index, boundary := range durationBuckets {
			_, _ = fmt.Fprintf(w, "virtroid_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(boundary, 'f', -1, 64), value.Buckets[index])
		}
		_, _ = fmt.Fprintf(w, "virtroid_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, "+Inf", value.Count)
		_, _ = fmt.Fprintf(w, "virtroid_http_request_duration_seconds_sum{%s} %g\n", labels, value.Sum)
		_, _ = fmt.Fprintf(w, "virtroid_http_request_duration_seconds_count{%s} %d\n", labels, value.Count)
	}
	for key, value := range s.counters {
		_, _ = fmt.Fprintf(w, "%s%s %d\n", key.Name, renderLabels(s.name, key.Labels), value)
	}
	for key, value := range s.gauges {
		_, _ = fmt.Fprintf(w, "%s%s %g\n", key.Name, renderLabels(s.name, key.Labels), value)
	}
}

func FromContext(ctx context.Context) TraceContext {
	trace, _ := ctx.Value(traceContextKey{}).(TraceContext)
	return trace
}

func (t TraceContext) Header() string {
	flags := "00"
	if t.Sampled {
		flags = "01"
	}
	return "00-" + t.TraceID + "-" + t.SpanID + "-" + flags
}

func incomingTrace(value string) TraceContext {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return TraceContext{Sampled: randomSample()}
	}
	if _, err := hex.DecodeString(parts[1] + parts[2] + parts[3]); err != nil || allZero(parts[1]) || allZero(parts[2]) {
		return TraceContext{Sampled: randomSample()}
	}
	flags, err := strconv.ParseUint(parts[3], 16, 8)
	if err != nil {
		return TraceContext{Sampled: randomSample()}
	}
	return TraceContext{TraceID: strings.ToLower(parts[1]), Sampled: flags&1 == 1}
}

func randomSample() bool {
	var value [1]byte
	_, err := rand.Read(value[:])
	return err == nil && value[0] < 13 // approximately five percent
}

func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return ""
	}
	return hex.EncodeToString(value)
}

func allZero(value string) bool {
	return strings.Trim(value, "0") == ""
}

func outboundTarget(req *http.Request) string {
	host := strings.ToLower(strings.TrimSpace(req.URL.Hostname()))
	if host == "" {
		return "unknown"
	}
	if host == "docker" {
		return "docker"
	}
	if req.URL.Port() == "8090" || strings.Contains(host, "virtnoded") {
		return "runtime-node"
	}
	if req.URL.Port() == "8080" || strings.Contains(host, "virtroidd") {
		return "control-plane"
	}
	return "upstream"
}

func boundedHTTPMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return strings.ToUpper(strings.TrimSpace(method))
	default:
		return "OTHER"
	}
}

func normalizedMetricName(value string) string {
	var result strings.Builder
	for _, candidate := range value {
		if (candidate >= 'a' && candidate <= 'z') || (candidate >= 'A' && candidate <= 'Z') ||
			(candidate >= '0' && candidate <= '9') || candidate == '_' || candidate == ':' {
			result.WriteRune(candidate)
		} else {
			result.WriteByte('_')
		}
	}
	if result.Len() == 0 {
		return "virtroid_metric"
	}
	return result.String()
}

func normalizedMetricLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "_")
	value = strings.ReplaceAll(value, "\n", "_")
	value = strings.ReplaceAll(value, "\r", "_")
	value = strings.ReplaceAll(value, "\"", "_")
	if len(value) > 200 {
		value = value[:200]
	}
	return value
}

func normalizedLogToken(value string) string {
	value = normalizedMetricLabel(value)
	return strings.ReplaceAll(value, " ", "_")
}

func logTraceID(traceID string) string {
	digest := sha256.Sum256([]byte(traceID))
	return hex.EncodeToString(digest[:])
}

func encodeLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, normalizedMetricName(key)+"="+normalizedMetricLabel(labels[key]))
	}
	return strings.Join(parts, ",")
}

func renderLabels(service, encoded string) string {
	parts := []string{"service=" + strconv.Quote(normalizedMetricLabel(service))}
	if encoded != "" {
		for _, pair := range strings.Split(encoded, ",") {
			key, value, found := strings.Cut(pair, "=")
			if found {
				parts = append(parts, key+"="+strconv.Quote(value))
			}
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(payload []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(payload)
}

func (w *responseRecorder) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
