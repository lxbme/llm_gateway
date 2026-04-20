package gateway

import (
	"fmt"
	"strings"
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

	collection := gw.Request.Header.Get("X-RAG-Collection")
	collectionSource := "X-RAG-Collection header"
	if collection == "" {
		collection = gw.Auth.Subject
		collectionSource = "Auth.Subject (token alias)"
	}
	if collection == "" {
		logWarn("RAG: configured but no collection resolvable for this request (no X-RAG-Collection header, no token alias) — skipping retrieve")
		return StageResult{Action: ActionContinue}
	}

	logDebug("RAG: resolved collection=%q from %s", collection, collectionSource)

	chunks, err := gw.Services.RAG.Retrieve(gw.Context, gw.Request.PromptText, collection, 0, 0)
	if err != nil {
		logWarn("RAG retrieve failed (degrading gracefully) collection=%q: %s", collection, err)
		return StageResult{Action: ActionContinue}
	}
	if len(chunks) == 0 {
		logDebug("RAG: retrieve returned 0 chunks for collection=%q — verify ingest used the same collection name", collection)
		return StageResult{Action: ActionContinue}
	}

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

	logDebug("RAG: injected %d chunks from collection=%s", len(chunks), collection)
	return StageResult{Action: ActionContinue}
}
