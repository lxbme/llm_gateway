package grpc

import (
	"context"
	"fmt"
	"llm_gateway/cache"
	pb "llm_gateway/cache/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	conn   *grpc.ClientConn
	client pb.CacheServiceClient
}

func NewClient(address string) (*Client, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to embedding service: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewCacheServiceClient(conn),
	}, nil
}

func (c *Client) Get(ctx context.Context, question string, model string) (string, bool, error) {
	resp, err := c.client.SearchSimilar(ctx, &pb.SearchRequest{
		Prompt: question,
		Model:  model,
	})
	if err != nil {
		return "", false, fmt.Errorf("failed to get embedding: %w", err)
	}

	if resp.Error != "" {
		return "", false, fmt.Errorf("embedding service error: %s", resp.Error)
	}

	return resp.Answer, resp.IsHit, nil
}

func (c *Client) Set(ctx context.Context, item cache.Task) error {
	resp, err := c.client.SaveCache(ctx, &pb.CacheTaskRequest{
		UserPrompt: item.UserPrompt,
		AiResponse: item.AIResponse,
		ModelName:  item.ModelName,
		TokenUsage: int32(item.TokenUsage),
	})
	if err != nil {
		return fmt.Errorf("failed to save cache: %w", err)
	}

	if resp.Error != "" {
		return fmt.Errorf("cache service error: %s", resp.Error)
	}

	return nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
