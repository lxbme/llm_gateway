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

	// Determine the logical knowledge-base collection.
	// X-RAG-Collection header takes priority; falls back to the token alias.
	collection := gw.Request.Header.Get("X-RAG-Collection")
	if collection == "" {
		collection = gw.Auth.Subject
	}
	if collection == "" {
		return StageResult{Action: ActionContinue}
	}

	chunks, err := gw.Services.RAG.Retrieve(gw.Context, gw.Request.PromptText, collection, 3, 0.6)
	if err != nil {
		logWarn("RAG retrieve failed (degrading gracefully): %s", err)
		return StageResult{Action: ActionContinue}
	}
	if len(chunks) == 0 {
		return StageResult{Action: ActionContinue}
	}

	// Inject retrieved context before the user prompt.
	var sb strings.Builder
	sb.WriteString("以下是相关参考资料，请优先基于这些资料回答：\n\n")
	for i, chunk := range chunks {
		sb.WriteString(fmt.Sprintf("[%d] (来源: %s)\n%s\n\n", i+1, chunk.Source, chunk.Content))
	}
	sb.WriteString("---\n\n用户问题：\n")
	sb.WriteString(gw.Request.PromptText)

	gw.Request.PromptText = sb.String()

	gw.Data["rag_chunks_count"] = len(chunks)
	gw.Data["rag_collection"] = collection

	logDebug("RAG: injected %d chunks from collection=%s", len(chunks), collection)
	return StageResult{Action: ActionContinue}
}
