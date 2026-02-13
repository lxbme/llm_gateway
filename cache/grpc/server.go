package grpc

import (
	"context"
	"llm_gateway/cache"
	pb "llm_gateway/cache/proto"
)

type Server struct {
	pb.UnimplementedCacheServiceServer
	cacheService cache.Service
}

func NewServer(cacheService cache.Service) *Server {
	return &Server{
		cacheService: cacheService,
	}
}

func (s *Server) SearchSimilar(ctx context.Context, req *pb.SearchRequest) (*pb.SearchResponse, error) {
	cacheAnswer, isHit, err := s.cacheService.Get(ctx, req.Prompt, req.Model)
	if err != nil {
		return &pb.SearchResponse{
			Answer: "",
			IsHit:  false,
			Error:  err.Error(),
		}, nil
	}

	return &pb.SearchResponse{
		Answer: cacheAnswer,
		IsHit:  isHit,
		Error:  "",
	}, nil
}

func (s *Server) SaveCache(ctx context.Context, req *pb.CacheTaskRequest) (*pb.SaveCacheResponse, error) {
	task := cache.Task{
		UserPrompt: req.UserPrompt,
		AIResponse: req.AiResponse,
		ModelName:  req.ModelName,
		TokenUsage: int(req.TokenUsage),
	}
	err := s.cacheService.Set(ctx, task)
	if err != nil {
		return &pb.SaveCacheResponse{
			Error: err.Error(),
		}, nil
	}
	return &pb.SaveCacheResponse{
		Error: "",
	}, nil
}
