package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewarePropagatesTraceAndExportsBoundedRouteMetrics(t *testing.T) {
	service := New("test-service")
	var outboundTrace string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outboundTrace = r.Header.Get("Traceparent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	client := &http.Client{Transport: service.Transport(http.DefaultTransport)}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Do(req); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
	})

	request := httptest.NewRequest(http.MethodGet, "/items/untrusted-value", nil)
	request.Header.Set("Traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	response := httptest.NewRecorder()
	service.Middleware(mux).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if !strings.HasPrefix(response.Header().Get("Traceparent"), "00-11111111111111111111111111111111-") {
		t.Fatalf("response traceparent = %q", response.Header().Get("Traceparent"))
	}
	if !strings.HasPrefix(outboundTrace, "00-11111111111111111111111111111111-") {
		t.Fatalf("outbound traceparent = %q", outboundTrace)
	}

	metricsResponse := httptest.NewRecorder()
	service.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	metrics, err := io.ReadAll(metricsResponse.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metrics), `route="GET /items/{id}"`) {
		t.Fatalf("metrics did not use the bounded route pattern:\n%s", metrics)
	}
	if strings.Contains(string(metrics), "untrusted-value") {
		t.Fatalf("metrics leaked a raw request path:\n%s", metrics)
	}
}

func TestIncomingTraceRejectsControlCharacters(t *testing.T) {
	trace := incomingTrace("00-11111111111111111111111111111111-2222222222222222-01\nforged")
	if trace.TraceID != "" {
		t.Fatalf("accepted invalid trace id %q", trace.TraceID)
	}
}

func TestMetricsBoundUnknownHTTPMethodsAndClassifyNodeTargets(t *testing.T) {
	if got := boundedHTTPMethod("UNTRUSTED-123"); got != "OTHER" {
		t.Fatalf("bounded method = %q", got)
	}
	req, err := http.NewRequest(http.MethodPost, "http://node.example:8090/api/v1/internal/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := outboundTarget(req); got != "runtime-node" {
		t.Fatalf("outbound target = %q", got)
	}
}
