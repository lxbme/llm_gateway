package grpc

import (
	"context"
	"llm_gateway/embedding"
	pb "llm_gateway/embedding/proto"
)

type Server struct {
	pb.UnimplementedEmbeddingServiceServer
	embeddingService embedding.Service
}

func NewServer(embeddingService embedding.Service) *Server {
	return &Server{
		embeddingService: embeddingService,
	}
}

func (s *Server) GetEmbedding(ctx context.Context, req *pb.EmbeddingRequest) (*pb.EmbeddingResponse, error) {
	embedding, err := s.embeddingService.Get(ctx, req.Text)
	if err != nil {
		return &pb.EmbeddingResponse{
			Error: err.Error(),
		}, nil
	}

	return &pb.EmbeddingResponse{
		Embedding: embedding,
		Error:     "",
	}, nil
}
