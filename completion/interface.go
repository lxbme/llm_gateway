package completion

import "context"

type Service interface {
	GetStream(ctx context.Context, req *CompletionRequest) (completionStream <-chan *CompletionChunk, err error)
}
