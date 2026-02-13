package grpc

import (
    "context"
    "fmt"
    pb "llm_gateway/embedding/proto"

    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

type Client struct {
    conn   *grpc.ClientConn
    client pb.EmbeddingServiceClient
}

func NewClient(address string) (*Client, error) {
    conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
    if err != nil {
        return nil, fmt.Errorf("failed to connect to embedding service: %w", err)
    }

    return &Client{
        conn:   conn,
        client: pb.NewEmbeddingServiceClient(conn),
    }, nil
}

func (c *Client) Get(ctx context.Context, text string) ([]float32, error) {
    resp, err := c.client.GetEmbedding(ctx, &pb.EmbeddingRequest{
        Text: text,
    })
    if err != nil {
        return nil, fmt.Errorf("failed to get embedding: %w", err)
    }

    if resp.Error != "" {
        return nil, fmt.Errorf("embedding service error: %s", resp.Error)
    }

    return resp.Embedding, nil
}

func (c *Client) Close() error {
    return c.conn.Close()
}