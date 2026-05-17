package main

import (
	"context"
	"fmt"
	"llm_gateway/completion/pool"
	"llm_gateway/internal/discovery"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	completiongrpc "llm_gateway/completion/grpc"
	pb "llm_gateway/completion/proto"
)

const serviceName = "completion"

func main() {
	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50053"
	}

	poolCfg, err := pool.LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("[Error] Failed to load pool config: %v", err)
	}
	completionService, err := pool.NewFromConfig(poolCfg)
	if err != nil {
		log.Fatalf("[Error] Failed to init completion pool: %v", err)
	}

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("Failed to listen:  %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterCompletionServiceServer(s, completiongrpc.NewServer(completionService))
	pb.RegisterCompletionAdminServer(s, completiongrpc.NewAdminServer(completionService))

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(s, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)

	advertiseAddr, err := discovery.AdvertiseAddr(servePort)
	if err != nil {
		log.Fatalf("Failed to resolve advertise addr: %v", err)
	}
	registerCtx, registerCancel := context.WithCancel(context.Background())
	defer registerCancel()
	deregister, err := discovery.Register(registerCtx, serviceName, advertiseAddr)
	if err != nil {
		log.Fatalf("Failed to register with discovery: %v", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		fmt.Printf("[Info] Completion received signal %s, shutting down\n", sig)
		healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
		deregister()
		s.GracefulStop()
	}()

	fmt.Printf("[Info] Completion gRPC server listening on port %s, advertise=%s\n", servePort, advertiseAddr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
