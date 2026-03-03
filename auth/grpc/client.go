package grpc

import (
	"context"
	"fmt"

	pb "llm_gateway/auth/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	conn   *grpc.ClientConn
	client pb.AuthServiceClient
}

func NewClient(address string) (*Client, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to auth service: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewAuthServiceClient(conn),
	}, nil
}

func (c *Client) Create(alias string) (string, error) {
	resp, err := c.client.Create(context.Background(), &pb.CreateRequest{Alias: alias})
	if err != nil {
		return "", fmt.Errorf("auth service Create: %w", err)
	}
	if resp.Error != "" {
		return "", fmt.Errorf("auth service Create: %s", resp.Error)
	}
	return resp.Token, nil
}

func (c *Client) Get(token string) (bool, string, error) {
	resp, err := c.client.Get(context.Background(), &pb.GetRequest{Token: token})
	if err != nil {
		return false, "", fmt.Errorf("auth service Get: %w", err)
	}
	if resp.Error != "" {
		return false, "", fmt.Errorf("auth service Get: %s", resp.Error)
	}
	return resp.Valid, resp.Alias, nil
}

func (c *Client) Delete(token string) error {
	resp, err := c.client.Delete(context.Background(), &pb.DeleteRequest{Token: token})
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
