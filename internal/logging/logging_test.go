package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// resetInit lets each test re-run Init in isolation. Without this the
// `initialized` package-level flag would make every test after the first a
// no-op.
func resetInit(t *testing.T) {
	t.Helper()
	initialized = false
	t.Cleanup(func() { initialized = false })
}

// withCapturedHandler installs a slog default that writes JSON to the
// returned buffer. Bypasses Init() — used by tests that need to inspect
// records directly (Init writes to os.Stdout which is harder to assert on).
func withCapturedHandler(t *testing.T, level slog.Level) (*bytes.Buffer, *sync.Mutex) {
	t.Helper()
	var (
		buf bytes.Buffer
		mu  sync.Mutex
	)
	base := slog.NewJSONHandler(&lockedWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: level})
	handler := newTraceContextHandler(base).WithAttrs([]slog.Attr{slog.String("service", "test-svc")})
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf, &mu
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestLevelFromEnv exercises the LOG_LEVEL → slog.Level mapping. Anything
// unrecognised (including unset) maps to INFO, which is the documented
// default and a behavioural change from the legacy ERROR default.
func TestLevelFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
		{"DEBUG", slog.LevelDebug},
		{"debug", slog.LevelDebug},
		{"INFO", slog.LevelInfo},
		{"WARN", slog.LevelWarn},
		{"WARNING", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"  error  ", slog.LevelError},
	}
	for _, c := range cases {
		t.Setenv(envLogLevel, c.env)
		if got := levelFromEnv(); got != c.want {
			t.Errorf("LOG_LEVEL=%q -> %v, want %v", c.env, got, c.want)
		}
	}
}

// TestTraceContextHandler_InjectsIDs verifies that when a record is emitted
// inside an active span, the handler adds trace_id/span_id attrs that match
// the span's IDs. This is the load-bearing P4 contract — break it and
// log↔trace correlation goes away.
func TestTraceContextHandler_InjectsIDs(t *testing.T) {
	buf, _ := withCapturedHandler(t, slog.LevelDebug)

	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	sc := span.SpanContext()
	wantTID := sc.TraceID().String()
	wantSID := sc.SpanID().String()

	slog.InfoContext(ctx, "hello", "k", "v")
	span.End()

	record := parseLastJSON(t, buf)
	if record["trace_id"] != wantTID {
		t.Errorf("trace_id: got %q want %q", record["trace_id"], wantTID)
	}
	if record["span_id"] != wantSID {
		t.Errorf("span_id: got %q want %q", record["span_id"], wantSID)
	}
	if record["msg"] != "hello" {
		t.Errorf("msg: got %q want %q", record["msg"], "hello")
	}
	if record["k"] != "v" {
		t.Errorf("user attr k: got %v want \"v\"", record["k"])
	}
}

// TestTraceContextHandler_NoopWhenNoSpan asserts that without an active
// span (just context.Background), trace_id/span_id are omitted entirely
// rather than emitted as empty strings. This keeps startup / background-
// goroutine logs clean.
func TestTraceContextHandler_NoopWhenNoSpan(t *testing.T) {
	buf, _ := withCapturedHandler(t, slog.LevelDebug)

	slog.InfoContext(context.Background(), "startup")

	record := parseLastJSON(t, buf)
	if _, ok := record["trace_id"]; ok {
		t.Errorf("trace_id should be absent for spanless ctx, got %v", record["trace_id"])
	}
	if _, ok := record["span_id"]; ok {
		t.Errorf("span_id should be absent for spanless ctx, got %v", record["span_id"])
	}
}

// TestServiceAttrInjected asserts that the static `service` attribute set
// by Init is present on every record — that's how cross-service log
// aggregation queries discriminate which service produced a line.
func TestServiceAttrInjected(t *testing.T) {
	buf, _ := withCapturedHandler(t, slog.LevelDebug)
	slog.InfoContext(context.Background(), "hi")
	record := parseLastJSON(t, buf)
	if record["service"] != "test-svc" {
		t.Errorf("service: got %v want \"test-svc\"", record["service"])
	}
}

// TestInit_DefaultLevel checks Init() runs cleanly with LOG_LEVEL unset.
// Cannot assert on output (Init writes to os.Stdout) — just exercise the
// init path so it doesn't panic and is replayable in tests.
func TestInit_DefaultLevel(t *testing.T) {
	resetInit(t)
	t.Setenv(envLogLevel, "")
	Init("test-default")
	// Second call is a no-op.
	Init("test-default")
}

// TestInit_StdlibLogBridged asserts that calling stdlib log.Printf after
// Init() results in a JSON record on the slog handler — a safety net so
// any third-party / legacy `log.Printf` doesn't break the JSON stream.
func TestInit_StdlibLogBridged(t *testing.T) {
	resetInit(t)
	buf, mu := withCapturedHandler(t, slog.LevelDebug)
	// Replicate the stdlib bridge that Init wires up (Init writes to
	// os.Stdout which is hard to capture in tests).
	mu.Lock()
	buf.Reset()
	mu.Unlock()

	// Simulate what Init does: stdlib log -> slogWriter -> slog.Default.
	w := slogWriter{}
	if _, err := w.Write([]byte("legacy log line\n")); err != nil {
		t.Fatalf("slogWriter.Write: %v", err)
	}

	record := parseLastJSON(t, buf)
	if record["msg"] != "legacy log line" {
		t.Errorf("msg: got %q want %q", record["msg"], "legacy log line")
	}
	if record["source"] != "stdlib_log" {
		t.Errorf("source: got %v want \"stdlib_log\"", record["source"])
	}
}

// parseLastJSON pulls the last newline-delimited JSON record out of buf.
// JSONHandler writes one record per line; tests that emit multiple records
// can call this repeatedly after each emit.
func parseLastJSON(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	raw := strings.TrimRight(buf.String(), "\n")
	lines := strings.Split(raw, "\n")
	last := lines[len(lines)-1]
	var m map[string]any
	if err := json.Unmarshal([]byte(last), &m); err != nil {
		t.Fatalf("parse JSON %q: %v", last, err)
	}
	return m
}
