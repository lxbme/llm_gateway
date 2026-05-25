package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// TestMetricsMiddleware_SetsXTraceIdWhenSpanValid asserts that when a request
// arrives with a valid OTel SpanContext on the context (which is what otelhttp
// installs upstream), the middleware writes X-Trace-Id BEFORE the inner
// handler runs — i.e. before any WriteHeader can lock the header set.
func TestMetricsMiddleware_SetsXTraceIdWhenSpanValid(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := metricsMiddleware(inner)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), sc))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	got := rec.Header().Get("X-Trace-Id")
	if want := "0af7651916cd43dd8448eb211c80319c"; got != want {
		t.Fatalf("X-Trace-Id: got %q, want %q", got, want)
	}
	if len(got) != 32 {
		t.Fatalf("X-Trace-Id length: got %d, want 32", len(got))
	}
}

// TestMetricsMiddleware_NoTraceHeaderWhenNoSpan asserts that without a valid
// span context (tracing disabled / no otelhttp), the middleware does not emit
// an X-Trace-Id header. This protects against a misleading empty header value.
func TestMetricsMiddleware_NoTraceHeaderWhenNoSpan(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := metricsMiddleware(inner)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Trace-Id"); got != "" {
		t.Fatalf("X-Trace-Id should be empty when no span on ctx, got %q", got)
	}
}
