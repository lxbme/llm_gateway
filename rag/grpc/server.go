package grpc

import (
	"context"

	"llm_gateway/rag"
	pb "llm_gateway/rag/proto"

	"google.golang.org/protobuf/types/known/emptypb"
)

type Server struct {
	pb.UnimplementedRagServiceServer
	ragService rag.Service
}

func NewServer(ragService rag.Service) *Server {
	return &Server{ragService: ragService}
}

func (s *Server) Ingest(ctx context.Context, req *pb.IngestRequest) (*pb.IngestResponse, error) {
	chunks := make([]rag.Chunk, 0, len(req.Chunks))
	for _, c := range req.Chunks {
		chunks = append(chunks, rag.Chunk{
			ChunkID:     c.ChunkId,
			DocID:       c.DocId,
			Collection:  c.Collection,
			Content:     c.Content,
			Source:      c.Source,
			ChunkIndex:  c.ChunkIndex,
			TotalChunks: c.TotalChunks,
		})
	}

	docID, count, err := s.ragService.Ingest(ctx, chunks)
	if err != nil {
		return &pb.IngestResponse{Error: err.Error()}, nil
	}
	return &pb.IngestResponse{
		IngestedCount: int32(count),
		DocId:         docID,
	}, nil
}

func (s *Server) Retrieve(ctx context.Context, req *pb.RetrieveRequest) (*pb.RetrieveResponse, error) {
	chunks, err := s.ragService.Retrieve(ctx, req.Query, req.Collection, req.TopK, req.Threshold)
	if err != nil {
		return &pb.RetrieveResponse{Error: err.Error()}, nil
	}

	pbChunks := make([]*pb.RetrievedChunk, 0, len(chunks))
	for _, c := range chunks {
		pbChunks = append(pbChunks, &pb.RetrievedChunk{
			ChunkId: c.ChunkID,
			Content: c.Content,
			Source:  c.Source,
			Score:   c.Score,
		})
	}
	return &pb.RetrieveResponse{Chunks: pbChunks}, nil
}

func (s *Server) DeleteDoc(ctx context.Context, req *pb.DeleteDocRequest) (*emptypb.Empty, error) {
	if err := s.ragService.DeleteDoc(ctx, req.DocId, req.Collection); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
