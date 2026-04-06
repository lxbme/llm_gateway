package grpc

import (
	"context"
	"fmt"

	"llm_gateway/rag"
	pb "llm_gateway/rag/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	conn   *grpc.ClientConn
	client pb.RagServiceClient
}

func NewClient(address string) (*Client, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to rag service: %w", err)
	}
	return &Client{
		conn:   conn,
		client: pb.NewRagServiceClient(conn),
	}, nil
}

func (c *Client) Ingest(ctx context.Context, chunks []rag.Chunk) (string, int, error) {
	pbChunks := make([]*pb.Chunk, 0, len(chunks))
	for _, ch := range chunks {
		pbChunks = append(pbChunks, &pb.Chunk{
			ChunkId:     ch.ChunkID,
			DocId:       ch.DocID,
			Collection:  ch.Collection,
			Content:     ch.Content,
			Source:      ch.Source,
			ChunkIndex:  ch.ChunkIndex,
			TotalChunks: ch.TotalChunks,
		})
	}

	resp, err := c.client.Ingest(ctx, &pb.IngestRequest{Chunks: pbChunks})
	if err != nil {
		return "", 0, fmt.Errorf("rag service Ingest: %w", err)
	}
	if resp.Error != "" {
		return "", 0, fmt.Errorf("rag service Ingest: %s", resp.Error)
	}
	return resp.DocId, int(resp.IngestedCount), nil
}

func (c *Client) Retrieve(ctx context.Context, query string, collection string, topK int32, threshold float32) ([]rag.RetrievedChunk, error) {
	resp, err := c.client.Retrieve(ctx, &pb.RetrieveRequest{
		Query:      query,
		Collection: collection,
		TopK:       topK,
		Threshold:  threshold,
	})
	if err != nil {
		return nil, fmt.Errorf("rag service Retrieve: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("rag service Retrieve: %s", resp.Error)
	}

	chunks := make([]rag.RetrievedChunk, 0, len(resp.Chunks))
	for _, c := range resp.Chunks {
		chunks = append(chunks, rag.RetrievedChunk{
			ChunkID: c.ChunkId,
			Content: c.Content,
			Source:  c.Source,
			Score:   c.Score,
		})
	}
	return chunks, nil
}

func (c *Client) DeleteDoc(ctx context.Context, docID string, collection string) error {
	_, err := c.client.DeleteDoc(ctx, &pb.DeleteDocRequest{
		DocId:      docID,
		Collection: collection,
	})
	if err != nil {
		return fmt.Errorf("rag service DeleteDoc: %w", err)
	}
	return nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}
