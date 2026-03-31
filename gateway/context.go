package gateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"llm_gateway/auth"
	"llm_gateway/cache"
	"llm_gateway/completion"
)

type Dependencies struct {
	Auth       auth.Service
	Cache      cache.Service
	Completion completion.Service
}

type GatewayContext struct {
	Context   context.Context
	RequestID string
	StartedAt time.Time

	Request  RequestState
	Auth     AuthState
	Route    RouteState
	Upstream UpstreamState
	Stream   StreamState
	Response ResponseState
	Runtime  RuntimeState

	Services Dependencies
	Data     map[string]any
}

type RequestState struct {
	Raw           *http.Request
	Path          string
	Method        string
	Header        http.Header
	RemoteAddr    string
	BodyBytes     []byte
	Chat          *ChatCompleteionRequest
	PromptText    string
	NormalizedKey string
}

type AuthState struct {
	BearerToken  string
	Subject      string
	Valid        bool
	RejectReason string
}

type RouteState struct {
	TargetService string
	Model         string
	Labels        map[string]string
}

type UpstreamState struct {
	Request  *completion.CompletionRequest
	Started  bool
	Finished bool
	Error    error
}

type StreamState struct {
	ChunkIndex   int
	CurrentChunk *completion.CompletionChunk
	FullAnswer   strings.Builder
	TokenUsage   int
}

type ResponseState struct {
	Writer         http.ResponseWriter
	StatusCode     int
	Header         http.Header
	DirectResponse *DirectResponse
	StreamStarted  bool
	FromCache      bool
}

type RuntimeState struct {
	ParallelSlotAcquired bool
}

type DirectResponseKind string

const (
	DirectResponseBody         DirectResponseKind = "body"
	DirectResponseCachedStream DirectResponseKind = "cached_stream"
	DirectResponseMockStream   DirectResponseKind = "mock_stream"
)

type DirectResponse struct {
	Kind         DirectResponseKind
	StatusCode   int
	Headers      http.Header
	Body         []byte
	CachedAnswer string
	Model        string
}

func newGatewayContext(w http.ResponseWriter, r *http.Request, services Dependencies) *GatewayContext {
	return &GatewayContext{
		Context:   r.Context(),
		RequestID: newRequestID(),
		StartedAt: time.Now(),
		Request: RequestState{
			Raw:        r,
			Path:       r.URL.Path,
			Method:     r.Method,
			Header:     r.Header.Clone(),
			RemoteAddr: r.RemoteAddr,
		},
		Route: RouteState{
			Labels: map[string]string{},
		},
		Response: ResponseState{
			Writer: w,
			Header: make(http.Header),
		},
		Services: services,
		Data:     map[string]any{},
	}
}

func buildPromptText(messages []Message) string {
	var builder strings.Builder
	for _, message := range messages {
		if message.Content == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(message.Content)
	}
	return builder.String()
}

func newRequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}
