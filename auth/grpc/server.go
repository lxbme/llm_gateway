package grpc

import (
	"context"

	"llm_gateway/auth"
	pb "llm_gateway/auth/proto"
)

type Server struct {
	pb.UnimplementedAuthServiceServer
	authService auth.Service
}

func NewServer(authSerice auth.Service) *Server {
	return &Server{
		authService: authSerice,
	}
}

func (s *Server) Create(ctx context.Context, req *pb.CreateRequest) (*pb.CreateResponse, error) {
	token, err := s.authService.Create(req.Alias)
	if err != nil {
		return &pb.CreateResponse{Error: err.Error()}, nil
	}
	return &pb.CreateResponse{Token: token}, nil
}

func (s *Server) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	valid, alias, err := s.authService.Get(req.Token)
	if err != nil {
		return &pb.GetResponse{Error: err.Error()}, nil
	}
	return &pb.GetResponse{Valid: valid, Alias: alias}, nil
}

func (s *Server) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	if err := s.authService.Delete(req.Token); err != nil {
		return &pb.DeleteResponse{Error: err.Error()}, nil
	}
	return &pb.DeleteResponse{}, nil
}
