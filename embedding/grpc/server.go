package grpc

import (
	"context"
	"llm_gateway/embedding"
	pb "llm_gateway/embedding/proto"

	"google.golang.org/protobuf/types/known/emptypb"
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

func (s *Server) Info(ctx context.Context, _ *emptypb.Empty) (*pb.InfoResponse, error) {
	info, err := s.embeddingService.Info(ctx)
	if err != nil {
		return nil, err
	}

	return &pb.InfoResponse{
		Provider:   info.Provider,
		Model:      info.Model,
		Dimensions: int32(info.Dimensions),
	}, nil
}
