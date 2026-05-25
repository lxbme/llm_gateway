package grpc

import (
	"context"
	"fmt"

	pb "llm_gateway/auth/proto"
	"llm_gateway/internal/discovery"

	"google.golang.org/grpc"
)

type Client struct {
	conn   *grpc.ClientConn
	client pb.AuthServiceClient
}

func NewClient(address string) (*Client, error) {
	conn, err := discovery.Dial("auth", address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to auth service: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewAuthServiceClient(conn),
	}, nil
}

func (c *Client) Create(ctx context.Context, alias string) (string, error) {
	resp, err := c.client.Create(ctx, &pb.CreateRequest{Alias: alias})
	if err != nil {
		return "", fmt.Errorf("auth service Create: %w", err)
	}
	if resp.Error != "" {
		return "", fmt.Errorf("auth service Create: %s", resp.Error)
	}
	return resp.Token, nil
}

func (c *Client) Get(ctx context.Context, token string) (bool, string, error) {
	resp, err := c.client.Get(ctx, &pb.GetRequest{Token: token})
	if err != nil {
		return false, "", fmt.Errorf("auth service Get: %w", err)
	}
	if resp.Error != "" {
		return false, "", fmt.Errorf("auth service Get: %s", resp.Error)
	}
	return resp.Valid, resp.Alias, nil
}

func (c *Client) Delete(ctx context.Context, token string) error {
	resp, err := c.client.Delete(ctx, &pb.DeleteRequest{Token: token})
	if err != nil {
		return fmt.Errorf("auth service Delete: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("auth service Delete: %s", resp.Error)
	}
	return nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
