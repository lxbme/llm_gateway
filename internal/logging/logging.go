// Package logging installs a process-wide slog logger that emits JSON to
// stdout and auto-injects trace_id / span_id from the active OTel span. All
// llm_gateway services call Init(name) at startup — afterwards, business
// code logs through standard `slog.InfoContext(ctx, ...)` (and the package
// shortcuts) without further setup.
//
// CONFIGURATION
//
//   LOG_LEVEL = DEBUG | INFO | WARN | ERROR   (default INFO)
//
// SENSITIVE FIELD RULES
//
// Log messages AND log attributes must never carry:
//   - prompt content / chat message bodies / completion text
//   - API keys, bearer tokens, the auth `Subject` (it is a token alias and
//     therefore effectively a per-user identifier)
//   - user IDs / per-user identifiers
//   - full upstream URLs (may embed credentials)
//   - raw cache keys / qdrant collection names that are user-derived
//
// Use these closed-set fields freely:
//   - service, endpoint (pool config name), strategy, stage, handler,
//     error_class, breaker_state — all bounded enums
//   - latency_ms, attempt, chunks, status_code — numeric
//
// FALLBACK
//
// trace_id / span_id are injected only when an active OTel span is in ctx.
// On startup paths or background goroutines without a span, those fields are
// simply absent from the JSON record — the rest of the line is still valid.
package logging

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"
)

const envLogLevel = "LOG_LEVEL"

// initialized tracks whether Init has run; subsequent calls are no-ops to
// keep tests and double-init scenarios safe.
var initialized bool

// Init installs the global slog logger for service. Reads LOG_LEVEL from env
// (default INFO). Safe to call from main() — subsequent calls are no-ops.
//
// Also redirects the stdlib log package's default output through slog so
// any residual log.Printf in third-party deps still ends up as JSON.
func Init(service string) {
	if initialized {
		return
	}
	initialized = true

	level := levelFromEnv()

	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	// service is a static attr — every record from this process carries it,
	// which is how cross-service log queries discriminate.
	handler := newTraceContextHandler(base).WithAttrs([]slog.Attr{
		slog.String("service", service),
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// Bridge stdlib `log` to slog. Anything that still calls `log.Printf`
	// (including this very package's transitive deps) will now route to the
	// JSON handler at the configured level.
	log.SetFlags(0)
	log.SetOutput(slogWriter{})
}

// levelFromEnv parses LOG_LEVEL → slog.Level. Unknown / unset → INFO. This
// is a behavioural change from the legacy gateway/logger.go default of
// ERROR (which was too quiet for production debugging); the migration to
// JSON makes higher verbosity cheap to filter downstream.
func levelFromEnv() slog.Level {
	switch strings.ToUpper(strings.TrimSpace(os.Getenv(envLogLevel))) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// slogWriter implements io.Writer; bytes written to it become slog records
// at INFO level. Used to redirect stdlib `log` package output.
type slogWriter struct{}

func (slogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if msg == "" {
		return len(p), nil
	}
	slog.LogAttrs(context.Background(), slog.LevelInfo, msg, slog.String("source", "stdlib_log"))
	return len(p), nil
}
