package grpc

import (
	"context"
	"fmt"
	"io"
	"llm_gateway/completion"
	pb "llm_gateway/completion/proto"
	"llm_gateway/internal/discovery"

	"google.golang.org/grpc"
)

type Client struct {
	conn   *grpc.ClientConn
	client pb.CompletionServiceClient
	admin  pb.CompletionAdminClient
}

func NewClient(address string) (*Client, error) {
	conn, err := discovery.Dial("completion", address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to completion service: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewCompletionServiceClient(conn),
		admin:  pb.NewCompletionAdminClient(conn),
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

func (c *Client) PoolStats(ctx context.Context) ([]completion.EndpointStatsSnapshot, error) {
	resp, err := c.client.PoolStats(ctx, &pb.PoolStatsRequest{})
	if err != nil {
		return nil, fmt.Errorf("PoolStats rpc: %w", err)
	}
	out := make([]completion.EndpointStatsSnapshot, 0, len(resp.Endpoints))
	for _, e := range resp.Endpoints {
		out = append(out, completion.EndpointStatsSnapshot{
			Endpoint:     e.Name,
			Weight:       int(e.Weight),
			Enabled:      e.Enabled,
			InFlight:     e.InFlight,
			Success:      e.Success,
			Failure:      e.Failure,
			SuccessRate:  e.SuccessRate,
			LatencyMs:    e.LatencyMsEwma,
			BreakerState: e.BreakerState,
		})
	}
	return out, nil
}

func (c *Client) ListEndpoints(ctx context.Context) ([]completion.EndpointView, error) {
	resp, err := c.admin.ListEndpoints(ctx, &pb.ListEndpointsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListEndpoints rpc: %w", err)
	}
	out := make([]completion.EndpointView, 0, len(resp.Endpoints))
	for _, e := range resp.Endpoints {
		out = append(out, completion.EndpointView{
			Name:         e.Name,
			URL:          e.Url,
			APIKeyEnv:    e.ApiKeyEnv,
			Weight:       int(e.Weight),
			Models:       e.Models,
			Enabled:      e.Enabled,
			BreakerState: e.BreakerState,
		})
	}
	return out, nil
}

func (c *Client) AddEndpoint(ctx context.Context, spec completion.EndpointSpec) error {
	_, err := c.admin.AddEndpoint(ctx, &pb.EndpointSpec{
		Name:      spec.Name,
		Url:       spec.URL,
		ApiKeyEnv: spec.APIKeyEnv,
		Weight:    int32(spec.Weight),
		Models:    spec.Models,
		Enabled:   spec.Enabled,
	})
	if err != nil {
		return fmt.Errorf("AddEndpoint rpc: %w", err)
	}
	return nil
}

func (c *Client) RemoveEndpoint(ctx context.Context, name string) error {
	if _, err := c.admin.RemoveEndpoint(ctx, &pb.EndpointName{Name: name}); err != nil {
		return fmt.Errorf("RemoveEndpoint rpc: %w", err)
	}
	return nil
}

func (c *Client) Reweight(ctx context.Context, name string, weight int) error {
	if _, err := c.admin.Reweight(ctx, &pb.ReweightRequest{Name: name, Weight: int32(weight)}); err != nil {
		return fmt.Errorf("Reweight rpc: %w", err)
	}
	return nil
}

func (c *Client) SetEnabled(ctx context.Context, name string, enabled bool) error {
	if _, err := c.admin.SetEnabled(ctx, &pb.SetEnabledRequest{Name: name, Enabled: enabled}); err != nil {
		return fmt.Errorf("SetEnabled rpc: %w", err)
	}
	return nil
}

func (c *Client) ResetBreaker(ctx context.Context, name string) error {
	if _, err := c.admin.ResetBreaker(ctx, &pb.EndpointName{Name: name}); err != nil {
		return fmt.Errorf("ResetBreaker rpc: %w", err)
	}
	return nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
