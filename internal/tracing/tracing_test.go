package tracing

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestInit_NoopWhenEndpointUnset verifies that an unset OTLP endpoint env var
// results in tracing being silently disabled — Init returns a non-nil noop
// shutdown and no error. This is the production safety net: a service must
// run normally even without the observability stack.
func TestInit_NoopWhenEndpointUnset(t *testing.T) {
	t.Setenv(envOTLPEndpoint, "")

	shutdown, err := Init(context.Background(), "test-service")
	if err != nil {
		t.Fatalf("Init returned err: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown returned err: %v", err)
	}
}

// TestEnvOr covers the small helper that reads an env var with a default.
func TestEnvOr(t *testing.T) {
	t.Setenv("LLM_GW_TEST_KEY", "")
	if got := envOr("LLM_GW_TEST_KEY", "fallback"); got != "fallback" {
		t.Errorf("empty env: got %q want %q", got, "fallback")
	}

	t.Setenv("LLM_GW_TEST_KEY", "  set  ")
	if got := envOr("LLM_GW_TEST_KEY", "fallback"); got != "set" {
		t.Errorf("set env (whitespace-trim): got %q want %q", got, "set")
	}
}

// TestTracer ensures Tracer returns a non-nil Tracer regardless of init state.
// The OTel SDK's global default already provides a noop tracer, so this
// should hold even before Init is called.
func TestTracer(t *testing.T) {
	tr := Tracer("test")
	if tr == nil {
		t.Fatal("Tracer returned nil")
	}
}

// TestAddEvent_NoopWhenSpanInvalid verifies that AddEvent on a context with
// no active span does not panic and is a silent no-op. This is the safety
// net that lets us pepper AddEvent calls through business code without
// guarding each call site.
func TestAddEvent_NoopWhenSpanInvalid(t *testing.T) {
	// context.Background() carries the OTel noop span — IsValid() is false.
	AddEvent(context.Background(), "test.event", attribute.String("k", "v"))
}

// TestAddEvent_AttachesAttrs verifies events land on the active span with
// the supplied attributes, when one is active.
func TestAddEvent_AttachesAttrs(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, span := Tracer("test").Start(context.Background(), "root")
	AddEvent(ctx, "stage",
		attribute.String("name", "auth"),
		attribute.Int64("latency_ms", 12),
	)
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 ended span, got %d", len(spans))
	}
	events := spans[0].Events()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Name != "stage" {
		t.Errorf("event name: got %q want %q", events[0].Name, "stage")
	}
	var sawName, sawLatency bool
	for _, a := range events[0].Attributes {
		switch a.Key {
		case "name":
			sawName = a.Value.AsString() == "auth"
		case "latency_ms":
			sawLatency = a.Value.AsInt64() == 12
		}
	}
	if !sawName || !sawLatency {
		t.Errorf("missing expected attrs in %+v", events[0].Attributes)
	}
}

// TestTruncateErr exercises the corner cases of TruncateErr: nil, short
// (returned verbatim), long (truncated with marker), and zero/negative
// maxLen (pass-through).
func TestTruncateErr(t *testing.T) {
	if got := TruncateErr(nil, 10); got != "" {
		t.Errorf("nil: got %q want empty", got)
	}
	if got := TruncateErr(errors.New("ok"), 10); got != "ok" {
		t.Errorf("short: got %q want %q", got, "ok")
	}
	long := errors.New("0123456789ABCDEF")
	if got := TruncateErr(long, 8); got != "01234567...[truncated]" {
		t.Errorf("long: got %q", got)
	}
	if got := TruncateErr(long, 0); got != "0123456789ABCDEF" {
		t.Errorf("zero maxLen should pass-through: got %q", got)
	}
}
