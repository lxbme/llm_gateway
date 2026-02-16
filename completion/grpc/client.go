package grpc

import (
	"context"
	"fmt"
	"io"
	"llm_gateway/completion"
	pb "llm_gateway/completion/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	conn   *grpc.ClientConn
	client pb.CompletionServiceClient
}

func NewClient(address string) (*Client, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to completion service: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewCompletionServiceClient(conn),
	}, nil
}

func (c *Client) GetStream(ctx context.Context, req *completion.CompletionRequest) (<-chan *completion.CompletionChunk, error) {
	// Convert completion.CompletionRequest to pb.CompletionRequest
	pbReq := &pb.CompletionRequest{
		Model:       req.Model,
		Question:    req.Question,
		Temperature: req.Temperature,
		MaxTokens:   int32(req.MaxTokens),
		Stream:      req.Stream,
	}

	// Call gRPC streaming method
	stream, err := c.client.GetStream(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream: %w", err)
	}

	// Create a channel to return completion chunks
	chunkChan := make(chan *completion.CompletionChunk)

	// Start a goroutine to receive from stream and send to channel
	go func() {
		defer close(chunkChan)

		for {
			pbChunk, err := stream.Recv()
			if err == io.EOF {
				// Stream ended normally
				return
			}
			if err != nil {
				// Send error chunk
				chunkChan <- &completion.CompletionChunk{
					Error: fmt.Errorf("stream error: %w", err),
				}
				return
			}

			// Convert pb.CompletionChunk to completion.CompletionChunk
			chunk := &completion.CompletionChunk{
				Content:    pbChunk.Content,
				Done:       pbChunk.Done,
				TokenUsage: int(pbChunk.TokenUsage),
			}
			if pbChunk.Error != "" {
				chunk.Error = fmt.Errorf("%s", pbChunk.Error)
			}

			chunkChan <- chunk
		}
	}()

	return chunkChan, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
