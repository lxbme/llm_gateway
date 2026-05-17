package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"llm_gateway/completion"
	pb "llm_gateway/completion/proto"
)

// AdminServer implements the pb.CompletionAdminServer service by delegating to
// a completion.Admin (typically the in-process *pool.Service).
type AdminServer struct {
	pb.UnimplementedCompletionAdminServer
	admin completion.Admin
}

func NewAdminServer(admin completion.Admin) *AdminServer {
	return &AdminServer{admin: admin}
}

func (s *AdminServer) ListEndpoints(ctx context.Context, _ *pb.ListEndpointsRequest) (*pb.ListEndpointsResponse, error) {
	views, err := s.admin.ListEndpoints(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ListEndpoints: %v", err)
	}
	resp := &pb.ListEndpointsResponse{Endpoints: make([]*pb.EndpointView, 0, len(views))}
	for _, v := range views {
		resp.Endpoints = append(resp.Endpoints, &pb.EndpointView{
			Name:         v.Name,
			Url:          v.URL,
			ApiKeyEnv:    v.APIKeyEnv,
			Weight:       int32(v.Weight),
			Models:       v.Models,
			Enabled:      v.Enabled,
			BreakerState: v.BreakerState,
		})
	}
	return resp, nil
}

func (s *AdminServer) AddEndpoint(ctx context.Context, req *pb.EndpointSpec) (*pb.AdminAck, error) {
	spec := completion.EndpointSpec{
		Name:      req.Name,
		URL:       req.Url,
		APIKeyEnv: req.ApiKeyEnv,
		Weight:    int(req.Weight),
		Models:    req.Models,
		Enabled:   req.Enabled,
	}
	if err := s.admin.AddEndpoint(ctx, spec); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "AddEndpoint: %v", err)
	}
	return &pb.AdminAck{Ok: true}, nil
}

func (s *AdminServer) RemoveEndpoint(ctx context.Context, req *pb.EndpointName) (*pb.AdminAck, error) {
	if err := s.admin.RemoveEndpoint(ctx, req.Name); err != nil {
		return nil, status.Errorf(codes.NotFound, "RemoveEndpoint: %v", err)
	}
	return &pb.AdminAck{Ok: true}, nil
}

func (s *AdminServer) Reweight(ctx context.Context, req *pb.ReweightRequest) (*pb.AdminAck, error) {
	if err := s.admin.Reweight(ctx, req.Name, int(req.Weight)); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "Reweight: %v", err)
	}
	return &pb.AdminAck{Ok: true}, nil
}

func (s *AdminServer) SetEnabled(ctx context.Context, req *pb.SetEnabledRequest) (*pb.AdminAck, error) {
	if err := s.admin.SetEnabled(ctx, req.Name, req.Enabled); err != nil {
		return nil, status.Errorf(codes.NotFound, "SetEnabled: %v", err)
	}
	return &pb.AdminAck{Ok: true}, nil
}

func (s *AdminServer) ResetBreaker(ctx context.Context, req *pb.EndpointName) (*pb.AdminAck, error) {
	if err := s.admin.ResetBreaker(ctx, req.Name); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "ResetBreaker: %v", err)
	}
	return &pb.AdminAck{Ok: true}, nil
}
