package tracing

import (
	"context"
	"testing"
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
