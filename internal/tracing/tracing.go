// Package tracing initializes the process-wide OpenTelemetry tracer provider
// for all llm_gateway services. Each service main() calls Init() once at
// startup; business code obtains tracers via Tracer(name).
//
// SPAN NAMING CONVENTION
//
//	<service>.<area>.<operation>
//
// Examples:
//   - gateway.cache.lookup
//   - completion.pool.select
//   - completion.upstream.http
//   - completion.upstream.sse.first_byte
//   - embedding.upstream.http
//
// Automatic spans from otelhttp / otelgrpc keep their library-default names.
//
// SENSITIVE ATTRIBUTE RULES
//
// Span attributes must never carry:
//   - prompt / message body / completion content
//   - API keys, bearer tokens, cookies, request headers carrying auth
//   - user_id / subject / any per-user identifier
//   - upstream URLs (may embed credentials in the path)
//   - raw cache keys (often a hash of the prompt — treat as sensitive)
//
// Allowed (small, enumerated, non-PII):
//   - endpoint    pool endpoint name (config-defined, ≤10)
//   - strategy    pool selection strategy enum
//   - attempt     retry index (int)
//   - cache.result hit | miss | error
//   - http.status_code, rpc.grpc.status_code (numeric)
//   - model       model name (config-bounded)
//
// FALLBACK BEHAVIOUR
//
// When OTEL_EXPORTER_OTLP_ENDPOINT is unset Init returns a no-op shutdown and
// the global TracerProvider remains the SDK default (which is itself a noop
// when no exporter is registered). This keeps services runnable without the
// observability stack (i.e. when `docker-run.sh` is invoked without --observe).
package tracing

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	envOTLPEndpoint   = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envServiceVersion = "OTEL_SERVICE_VERSION"
	envDeployment     = "DEPLOYMENT_ENV"
)

// noopShutdown is returned whenever tracing is disabled or fails to initialize
// — callers `defer shutdown(...)` unconditionally, so this must never be nil.
func noopShutdown(context.Context) error { return nil }

// Init installs a global TracerProvider for service. When the OTLP endpoint
// env is empty, tracing is left as a no-op (the SDK default) and a no-op
// shutdown is returned so callers can `defer shutdown(...)` unconditionally.
//
// On exporter creation failure Init returns the noop shutdown plus the error;
// callers should log-and-continue rather than abort the process — tracing
// outage must never take down business traffic.
func Init(ctx context.Context, service string) (func(context.Context) error, error) {
	// Always install the W3C tracecontext propagator. Even when we have no
	// exporter, this lets incoming traceparent headers flow through and
	// downstream services that DO have an exporter keep the trace stitched.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	endpoint := strings.TrimSpace(os.Getenv(envOTLPEndpoint))
	if endpoint == "" {
		log.Printf("[Info] tracing disabled (%s unset) service=%s", envOTLPEndpoint, service)
		return noopShutdown, nil
	}

	// Accept either "host:port" or a full URL — strip scheme so the gRPC
	// exporter sees a plain target.
	target := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(target),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("otlptracegrpc.New: %w", err)
	}

	// Use a schemaless resource for our own attributes — the SDK's Default()
	// resource pins a fixed semconv schema URL (1.40.0 in current SDKs); if we
	// also assert one here Merge errors out with "conflicting Schema URL".
	// Schemaless is safe because our attribute keys are stable semantic-conv
	// names whose meaning doesn't change across schema versions.
	res, err := sdkresource.Merge(
		sdkresource.Default(),
		sdkresource.NewSchemaless(
			semconv.ServiceName(service),
			semconv.ServiceVersion(envOr(envServiceVersion, "dev")),
			attribute.String("deployment.environment", envOr(envDeployment, "local")),
		),
	)
	if err != nil {
		return noopShutdown, fmt.Errorf("resource.Merge: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	log.Printf("[Info] tracing enabled service=%s endpoint=%s", service, target)
	return tp.Shutdown, nil
}

// Tracer returns a Tracer from the global TracerProvider. Wrapped here so all
// business code has one obvious entry point and stale tracer references are
// impossible (the lookup is cheap; do not cache the returned value).
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
