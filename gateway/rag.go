package gateway

import (
	"fmt"
	"log/slog"
	"strings"

	"llm_gateway/internal/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// handleRAGRetrieveStage retrieves semantically relevant document chunks and
// prepends them to the prompt before the upstream LLM call.
//
// Placement: StageBeforeUpstream, after auth_validate_handler and before cache_lookup_handler.
// This ensures:
//   - Auth is validated before we call the RAG service.
//   - The cache lookup operates on the RAG-augmented prompt, preventing stale hits
//     from non-RAG sessions being served to RAG-enabled ones.
//
// Fallback: any error or empty result is silently skipped so the request always
// continues to the LLM, just without retrieved context.
func handleRAGRetrieveStage(gw *GatewayContext) StageResult {
	if gw.Services.RAG == nil {
		return StageResult{Action: ActionContinue}
	}

	// Span wraps the embedding + vector-DB query path. Not just an event,
	// because internally RAG does two upstream calls (embedding gRPC then
	// qdrant) and a child span makes the slower leg visible in Tempo.
	ctx, span := tracing.Tracer("gateway").Start(gw.Context, "gateway.rag.retrieve")
	defer span.End()

	collection := gw.Request.Header.Get("X-RAG-Collection")
	// collection_source is a bounded enum (2 values) — safe attribute. The
	// collection value itself can be a per-user identifier (token alias) so
	// it is deliberately NOT recorded as a span attribute or log field.
	collectionSourceAttr := "header"
	if collection == "" {
		collection = gw.Auth.Subject
		collectionSourceAttr = "token_alias"
	}
	if collection == "" {
		span.SetAttributes(attribute.String("result", "skipped_no_collection"))
		slog.WarnContext(gw.Context, "rag retrieve skipped: no collection resolvable")
		return StageResult{Action: ActionContinue}
	}
	span.SetAttributes(attribute.String("collection_source", collectionSourceAttr))

	slog.DebugContext(gw.Context, "rag collection resolved", "source", collectionSourceAttr)

	chunks, err := gw.Services.RAG.Retrieve(ctx, gw.Request.PromptText, collection, 0, 0)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "rag retrieve failed")
		span.SetAttributes(attribute.String("result", "error"))
		slog.WarnContext(gw.Context, "rag retrieve failed (degrading gracefully)",
			"source", collectionSourceAttr, "err", err)
		return StageResult{Action: ActionContinue}
	}
	if len(chunks) == 0 {
		span.SetAttributes(
			attribute.Int("chunks", 0),
			attribute.String("result", "no_match"),
		)
		slog.DebugContext(gw.Context, "rag retrieve returned 0 chunks", "source", collectionSourceAttr)
		return StageResult{Action: ActionContinue}
	}

	span.SetAttributes(
		attribute.Int("chunks", len(chunks)),
		attribute.String("result", "ok"),
	)

	// Inject retrieved context before the user prompt.
	var sb strings.Builder
	sb.WriteString("The following are relevant reference materials; please prioritize basing your response on these sources:\n\n")
	for i, chunk := range chunks {
		sb.WriteString(fmt.Sprintf("[%d] (Source: %s)\n%s\n\n", i+1, chunk.Source, chunk.Content))
	}
	sb.WriteString("---\n\nUser Prompt：\n")
	sb.WriteString(gw.Request.PromptText)

	gw.Request.PromptText = sb.String()

	gw.Data["rag_chunks_count"] = len(chunks)
	gw.Data["rag_collection"] = collection

	slog.DebugContext(gw.Context, "rag chunks injected", "chunks", len(chunks), "source", collectionSourceAttr)
	return StageResult{Action: ActionContinue}
}
