package completion

import "context"

type Service interface {
	GetStream(ctx context.Context, req *CompletionRequest) (<-chan *CompletionChunk, error)
}
