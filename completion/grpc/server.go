package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"llm_gateway/completion"
	pb "llm_gateway/completion/proto"
)

type Server struct {
	pb.UnimplementedCompletionServiceServer
	completionService completion.Service
	statsProvider     completion.StatsProvider // optional; nil = PoolStats returns Unimplemented
}

func NewServer(completionService completion.Service) *Server {
	s := &Server{completionService: completionService}
	if sp, ok := completionService.(completion.StatsProvider); ok {
		s.statsProvider = sp
	}
	return s
}

func (s *Server) GetStream(req *pb.CompletionRequest, stream pb.CompletionService_GetStreamServer) error {
	// Convert pb.CompletionRequest to completion.CompletionRequest
	completionReq := &completion.CompletionRequest{
		Model:       req.Model,
		Question:    req.Question,
		Temperature: req.Temperature,
		MaxTokens:   int(req.MaxTokens),
		Stream:      req.Stream,
	}

	// Call the completion service
	chunkChan, err := s.completionService.GetStream(stream.Context(), completionReq)
	if err != nil {
		return err
	}

	// Stream the chunks back to the client
	for chunk := range chunkChan {
		pbChunk := &pb.CompletionChunk{
			Content:    chunk.Content,
			Done:       chunk.Done,
			TokenUsage: int32(chunk.TokenUsage),
		}
		if chunk.Error != nil {
			pbChunk.Error = chunk.Error.Error()
		}

		if err := stream.Send(pbChunk); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) PoolStats(ctx context.Context, _ *pb.PoolStatsRequest) (*pb.PoolStatsResponse, error) {
	if s.statsProvider == nil {
		return nil, status.Error(codes.Unimplemented, "completion service does not expose pool stats")
	}
	snapshots, err := s.statsProvider.PoolStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "PoolStats: %v", err)
	}
	resp := &pb.PoolStatsResponse{Endpoints: make([]*pb.EndpointStat, 0, len(snapshots))}
	for _, s := range snapshots {
		resp.Endpoints = append(resp.Endpoints, &pb.EndpointStat{
			Name:          s.Endpoint,
			Weight:        int32(s.Weight),
			Enabled:       s.Enabled,
			InFlight:      s.InFlight,
			Success:       s.Success,
			Failure:       s.Failure,
			SuccessRate:   s.SuccessRate,
			LatencyMsEwma: s.LatencyMs,
			BreakerState:  s.BreakerState,
		})
	}
	return resp, nil
}
