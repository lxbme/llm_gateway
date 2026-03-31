package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"llm_gateway/auth/redis"
	"llm_gateway/cache"
	"llm_gateway/completion"
)

func defaultGatewayPipeline() *Pipeline {
	return NewPipeline(
		newStageHandler("cors_handler", []StageName{StageRequestReceived}, handleCORSStage),
		newStageHandler("rate_limit_handler", []StageName{StageRequestReceived}, handleRateLimitStage),
		newStageHandler("token_extract_handler", []StageName{StageRequestReceived}, handleTokenExtractStage),
		newStageHandler("request_decode_handler", []StageName{StageRequestDecoded}, handleRequestDecodeStage),
		newStageHandler("prompt_build_handler", []StageName{StageRequestDecoded}, handlePromptBuildStage),
		newStageHandler("auth_validate_handler", []StageName{StageBeforeUpstream}, handleAuthValidateStage),
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
		logWarn("Hit rate limit (token bucket)")
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
		logWarn("Hit rate limit (semaphore)")
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
		logError("Failed to read user request: %s", err)
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
		logError("Failed to parse user request: %s", err)
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

	logDebug(
		"Parsed request: model=%s, stream=%v, messages=%d",
		gw.Request.Chat.Model,
		gw.Request.Chat.Stream,
		len(gw.Request.Chat.Messages),
	)
	return StageResult{Action: ActionContinue}
}

func handleAuthValidateStage(gw *GatewayContext) StageResult {
	isValid, alias, err := gw.Services.Auth.Get(gw.Auth.BearerToken)
	if err != nil {
		logError("AuthCheckMiddleware: auth service error: %s", err)
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

	logDebug("x-mock: true")
	gw.Response.DirectResponse = &DirectResponse{
		Kind:       DirectResponseMockStream,
		StatusCode: http.StatusOK,
		Model:      gw.Route.Model,
	}
	return StageResult{Action: ActionDirectResponse, StatusCode: http.StatusOK}
}

func handleCacheLookupStage(gw *GatewayContext) StageResult {
	cacheAnswer, isHit, err := gw.Services.Cache.Get(gw.Context, gw.Request.NormalizedKey, gw.Route.Model)
	if err != nil {
		logError("Failed to search similar vector in qdrant: %s", err)
		return StageResult{Action: ActionContinue}
	}
	if !isHit {
		return StageResult{Action: ActionContinue}
	}

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
		logError("Stream error: %s", chunk.Error)
		gw.Upstream.Error = chunk.Error
		return StageResult{Action: ActionContinue}
	}

	if chunk.Content != "" {
		gw.Stream.FullAnswer.WriteString(chunk.Content)
	}

	if chunk.Done {
		gw.Stream.TokenUsage = chunk.TokenUsage
		logDebug("Stream completed successfully")
	}

	return StageResult{Action: ActionContinue}
}

func handleCacheWritebackStage(gw *GatewayContext) StageResult {
	if gw.Response.FromCache || gw.Upstream.Error != nil || gw.Stream.FullAnswer.Len() == 0 {
		return StageResult{Action: ActionContinue}
	}

	err := gw.Services.Cache.Set(gw.Context, cache.Task{
		UserPrompt: gw.Request.NormalizedKey,
		AIResponse: gw.Stream.FullAnswer.String(),
		ModelName:  gw.Route.Model,
		TokenUsage: gw.Stream.TokenUsage,
	})
	if err != nil {
		logError("Failed to save cache: %s", err)
	}

	return StageResult{Action: ActionContinue}
}

func handleAuditLogStage(gw *GatewayContext) StageResult {
	answerText := gw.Stream.FullAnswer.String()
	if answerText == "" && gw.Response.DirectResponse != nil && gw.Response.DirectResponse.Kind == DirectResponseCachedStream {
		answerText = gw.Response.DirectResponse.CachedAnswer
	}
	printDialog(gw.Request.PromptText, answerText)
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
