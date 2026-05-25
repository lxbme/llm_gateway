package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"llm_gateway/auth/redis"
	"llm_gateway/cache"
	"llm_gateway/completion"
	"llm_gateway/internal/metrics"
	"llm_gateway/internal/tracing"

	"go.opentelemetry.io/otel/attribute"
)

func defaultGatewayPipeline() *Pipeline {
	return NewPipeline(
		newStageHandler("cors_handler", []StageName{StageRequestReceived}, handleCORSStage),
		newStageHandler("rate_limit_handler", []StageName{StageRequestReceived}, handleRateLimitStage),
		newStageHandler("token_extract_handler", []StageName{StageRequestReceived}, handleTokenExtractStage),
		newStageHandler("request_decode_handler", []StageName{StageRequestDecoded}, handleRequestDecodeStage),
		newStageHandler("prompt_build_handler", []StageName{StageRequestDecoded}, handlePromptBuildStage),
		newStageHandler("auth_validate_handler", []StageName{StageBeforeUpstream}, handleAuthValidateStage),
		newStageHandler("rag_retrieve_handler", []StageName{StageBeforeUpstream}, handleRAGRetrieveStage),
		newStageHandler("mock_response_handler", []StageName{StageBeforeUpstream}, handleMockResponseStage),
		newStageHandler("cache_lookup_handler", []StageName{StageBeforeUpstream}, handleCacheLookupStage),
		newStageHandler("upstream_request_build_handler", []StageName{StageBeforeUpstream}, handleUpstreamBuildStage),
		newStageHandler("stream_assemble_handler", []StageName{StageStreamChunk}, handleStreamChunkStage),
		newStageHandler("cache_writeback_handler", []StageName{StageResponseComplete}, handleCacheWritebackStage),
		newStageHandler("audit_log_handler", []StageName{StageResponseComplete}, handleAuditLogStage),
	)
}

type stageHandler struct {
	name   string
	stages []StageName
	handle func(*GatewayContext) StageResult
}

func newStageHandler(name string, stages []StageName, handle func(*GatewayContext) StageResult) StageHandler {
	return &stageHandler{name: name, stages: stages, handle: handle}
}

func (h *stageHandler) Name() string {
	return h.name
}

func (h *stageHandler) Stages() []StageName {
	return h.stages
}

func (h *stageHandler) Handle(gw *GatewayContext) StageResult {
	return h.handle(gw)
}

func handleCORSStage(gw *GatewayContext) StageResult {
	gw.Response.Header.Set("Access-Control-Allow-Origin", "*")
	gw.Response.Header.Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")

	requestedHeaders := gw.Request.Header.Get("Access-Control-Request-Headers")
	if requestedHeaders != "" {
		gw.Response.Header.Set("Access-Control-Allow-Headers", requestedHeaders)
	} else {
		gw.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	}

	if gw.Request.Method == http.MethodOptions {
		gw.Response.DirectResponse = &DirectResponse{
			Kind:       DirectResponseBody,
			StatusCode: http.StatusNoContent,
			Headers: http.Header{
				"Access-Control-Max-Age": []string{"86400"},
			},
		}
		return StageResult{Action: ActionDirectResponse, StatusCode: http.StatusNoContent}
	}

	return StageResult{Action: ActionContinue}
}

func handleRateLimitStage(gw *GatewayContext) StageResult {
	if !rateLimiter.Allow() {
		slog.WarnContext(gw.Context, "rate limit hit", "limiter", "token_bucket")
		gw.Response.DirectResponse = newJSONDirectResponse(
			http.StatusTooManyRequests,
			map[string]any{
				"error": map[string]string{
					"message": "rate limit exceeded",
					"type":    "server_busy",
				},
			},
		)
		return StageResult{Action: ActionReject, StatusCode: http.StatusTooManyRequests, Message: "rate limit exceeded"}
	}

	select {
	case parallelSemaphore <- struct{}{}:
		gw.Runtime.ParallelSlotAcquired = true
	default:
		slog.WarnContext(gw.Context, "rate limit hit", "limiter", "semaphore")
		gw.Response.DirectResponse = newJSONDirectResponse(
			http.StatusTooManyRequests,
			map[string]any{
				"error": map[string]string{
					"message": "rate limit exceeded",
					"type":    "server_busy",
				},
			},
		)
		return StageResult{Action: ActionReject, StatusCode: http.StatusTooManyRequests, Message: "rate limit exceeded"}
	}

	return StageResult{Action: ActionContinue}
}

func handleTokenExtractStage(gw *GatewayContext) StageResult {
	authHeader := gw.Request.Header.Get("Authorization")
	if authHeader == "" {
		gw.Auth.RejectReason = "Missing Authorization header"
		gw.Response.DirectResponse = invalidAPIKeyResponse(gw.Auth.RejectReason)
		return StageResult{Action: ActionReject, StatusCode: http.StatusUnauthorized, Message: gw.Auth.RejectReason}
	}

	token, found := strings.CutPrefix(authHeader, "Bearer ")
	if !found || token == "" {
		gw.Auth.RejectReason = "Authorization header must be: Bearer token"
		gw.Response.DirectResponse = invalidAPIKeyResponse(gw.Auth.RejectReason)
		return StageResult{Action: ActionReject, StatusCode: http.StatusUnauthorized, Message: gw.Auth.RejectReason}
	}

	if !redis.CheckTokenFormat(tokenPrefix, tokenEntropyLen, token) {
		gw.Auth.RejectReason = "Invalid token format"
		gw.Response.DirectResponse = invalidAPIKeyResponse(gw.Auth.RejectReason)
		return StageResult{Action: ActionReject, StatusCode: http.StatusUnauthorized, Message: gw.Auth.RejectReason}
	}

	gw.Auth.BearerToken = token
	return StageResult{Action: ActionContinue}
}

func handleRequestDecodeStage(gw *GatewayContext) StageResult {
	bodyBytes, err := io.ReadAll(gw.Request.Raw.Body)
	if err != nil {
		slog.ErrorContext(gw.Context, "read request body failed", "err", err)
		gw.Response.DirectResponse = newJSONDirectResponse(
			http.StatusBadRequest,
			map[string]string{"error": "Failed to parse user request"},
		)
		return StageResult{Action: ActionReject, StatusCode: http.StatusBadRequest, Message: "Failed to parse user request", Err: err}
	}
	defer gw.Request.Raw.Body.Close()

	gw.Request.BodyBytes = bodyBytes

	var userReq ChatCompleteionRequest
	if err := json.Unmarshal(bodyBytes, &userReq); err != nil {
		slog.ErrorContext(gw.Context, "parse request body failed", "err", err)
		gw.Response.DirectResponse = newJSONDirectResponse(
			http.StatusBadRequest,
			map[string]string{"error": "Failed to parse user request"},
		)
		return StageResult{Action: ActionReject, StatusCode: http.StatusBadRequest, Message: "Failed to parse user request", Err: err}
	}

	gw.Request.Chat = &userReq
	return StageResult{Action: ActionContinue}
}

func handlePromptBuildStage(gw *GatewayContext) StageResult {
	if gw.Request.Chat == nil {
		return StageResult{Action: ActionReject, StatusCode: http.StatusBadRequest, Message: "request body not decoded"}
	}

	gw.Request.PromptText = buildPromptText(gw.Request.Chat.Messages)
	gw.Request.NormalizedKey = gw.Request.PromptText
	gw.Route.Model = gw.Request.Chat.Model

	slog.DebugContext(gw.Context, "request parsed",
		"model", gw.Request.Chat.Model,
		"stream", gw.Request.Chat.Stream,
		"messages", len(gw.Request.Chat.Messages),
	)
	return StageResult{Action: ActionContinue}
}

func handleAuthValidateStage(gw *GatewayContext) StageResult {
	isValid, alias, err := gw.Services.Auth.Get(gw.Context, gw.Auth.BearerToken)
	if err != nil {
		slog.ErrorContext(gw.Context, "auth service error", "err", err)
		gw.Response.DirectResponse = invalidAPIKeyResponse("Authentication service unavailable")
		return StageResult{Action: ActionReject, StatusCode: http.StatusUnauthorized, Message: "Authentication service unavailable", Err: err}
	}
	if !isValid {
		gw.Response.DirectResponse = invalidAPIKeyResponse("Invalid or revoked token")
		return StageResult{Action: ActionReject, StatusCode: http.StatusUnauthorized, Message: "Invalid or revoked token"}
	}

	gw.Auth.Valid = true
	gw.Auth.Subject = alias
	return StageResult{Action: ActionContinue}
}

func handleMockResponseStage(gw *GatewayContext) StageResult {
	if gw.Request.Header.Get("x-mock") != "true" {
		return StageResult{Action: ActionContinue}
	}

	slog.DebugContext(gw.Context, "mock response requested", "header", "x-mock")
	gw.Response.DirectResponse = &DirectResponse{
		Kind:       DirectResponseMockStream,
		StatusCode: http.StatusOK,
		Model:      gw.Route.Model,
	}
	return StageResult{Action: ActionDirectResponse, StatusCode: http.StatusOK}
}

func handleCacheLookupStage(gw *GatewayContext) StageResult {
	ctx, span := tracing.Tracer("gateway").Start(gw.Context, "gateway.cache.lookup")
	defer span.End()

	// emitResult records the result on the child cache.lookup span (for span
	// queries / metrics-via-trace) AND as an event on the parent gateway.http
	// span so the result is visible inline on the request timeline view —
	// child-span attributes do not show up on the parent's event list in
	// Tempo, so the event is what makes "did this hit?" scannable.
	emitResult := func(result string, latency time.Duration) {
		span.SetAttributes(attribute.String("cache.result", result))
		tracing.AddEvent(gw.Context, "gateway.cache.result",
			attribute.String("result", result),
			attribute.Int64("latency_ms", latency.Milliseconds()),
		)
	}

	start := time.Now()
	cacheAnswer, isHit, err := gw.Services.Cache.Get(ctx, gw.Request.NormalizedKey, gw.Route.Model)
	metrics.CacheLookupLatencySec.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.CacheLookupTotal.WithLabelValues("error").Inc()
		emitResult("error", time.Since(start))
		slog.ErrorContext(gw.Context, "cache lookup failed", "err", err)
		return StageResult{Action: ActionContinue}
	}
	if !isHit {
		metrics.CacheLookupTotal.WithLabelValues("miss").Inc()
		emitResult("miss", time.Since(start))
		return StageResult{Action: ActionContinue}
	}
	metrics.CacheLookupTotal.WithLabelValues("hit").Inc()
	emitResult("hit", time.Since(start))

	gw.Response.FromCache = true
	gw.Response.DirectResponse = &DirectResponse{
		Kind:         DirectResponseCachedStream,
		StatusCode:   http.StatusOK,
		CachedAnswer: cacheAnswer,
		Model:        gw.Route.Model,
	}
	return StageResult{Action: ActionDirectResponse, StatusCode: http.StatusOK}
}

func handleUpstreamBuildStage(gw *GatewayContext) StageResult {
	gw.Route.TargetService = "completion"
	gw.Upstream.Request = &completion.CompletionRequest{
		Model:       gw.Request.Chat.Model,
		Question:    gw.Request.PromptText,
		Temperature: gw.Request.Chat.Temperature,
		MaxTokens:   gw.Request.Chat.MaxTokens,
		Stream:      true,
	}
	return StageResult{Action: ActionContinue}
}

func handleStreamChunkStage(gw *GatewayContext) StageResult {
	chunk := gw.Stream.CurrentChunk
	if chunk == nil {
		return StageResult{Action: ActionContinue}
	}

	if chunk.Error != nil {
		slog.ErrorContext(gw.Context, "upstream stream error", "err", chunk.Error)
		gw.Upstream.Error = chunk.Error
		return StageResult{Action: ActionContinue}
	}

	if chunk.Content != "" {
		gw.Stream.FullAnswer.WriteString(chunk.Content)
	}

	if chunk.Done {
		gw.Stream.TokenUsage = chunk.TokenUsage
		slog.DebugContext(gw.Context, "upstream stream completed")
	}

	return StageResult{Action: ActionContinue}
}

func handleCacheWritebackStage(gw *GatewayContext) StageResult {
	if gw.Response.FromCache || gw.Upstream.Error != nil || gw.Stream.FullAnswer.Len() == 0 {
		return StageResult{Action: ActionContinue}
	}

	// Skip caching when the response was RAG-augmented: the retrieved context
	// depends on external documents that can be added or deleted at any time,
	// so caching such responses would return stale answers after document changes.
	if count, ok := gw.Data["rag_chunks_count"].(int); ok && count > 0 {
		slog.DebugContext(gw.Context, "cache write skipped (rag-augmented)", "chunks", count)
		return StageResult{Action: ActionContinue}
	}

	err := gw.Services.Cache.Set(gw.Context, cache.Task{
		UserPrompt: gw.Request.NormalizedKey,
		AIResponse: gw.Stream.FullAnswer.String(),
		ModelName:  gw.Route.Model,
		TokenUsage: gw.Stream.TokenUsage,
	})
	if err != nil {
		slog.ErrorContext(gw.Context, "cache write failed", "err", err)
	}

	return StageResult{Action: ActionContinue}
}

func handleAuditLogStage(gw *GatewayContext) StageResult {
	answerText := gw.Stream.FullAnswer.String()
	if answerText == "" && gw.Response.DirectResponse != nil && gw.Response.DirectResponse.Kind == DirectResponseCachedStream {
		answerText = gw.Response.DirectResponse.CachedAnswer
	}
	auditDialog(gw.Context, gw.Request.PromptText, answerText)
	return StageResult{Action: ActionContinue}
}

func invalidAPIKeyResponse(message string) *DirectResponse {
	return newJSONDirectResponse(
		http.StatusUnauthorized,
		map[string]any{
			"error": map[string]string{
				"message": message,
				"type":    "invalid_api_key",
			},
		},
	)
}

func newJSONDirectResponse(statusCode int, payload any) *DirectResponse {
	body, _ := json.Marshal(payload)
	return &DirectResponse{
		Kind:       DirectResponseBody,
		StatusCode: statusCode,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: body,
	}
}
