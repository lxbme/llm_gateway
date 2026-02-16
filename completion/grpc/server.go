package grpc

import (
	"llm_gateway/completion"
	pb "llm_gateway/completion/proto"
)

type Server struct {
	pb.UnimplementedCompletionServiceServer
	completionService completion.Service
}

func NewServer(completionService completion.Service) *Server {
	return &Server{
		completionService: completionService,
	}
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
