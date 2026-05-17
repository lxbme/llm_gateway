package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"llm_gateway/cache"
	"llm_gateway/cache/factory"
	cachegrpc "llm_gateway/cache/grpc"
	pb "llm_gateway/cache/proto"
	embeddingGrpc "llm_gateway/embedding/grpc"
	"llm_gateway/internal/discovery"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const serviceName = "cache"

func main() {
	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50052"
	}

	cfg, err := cache.LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("Failed to load cache config: %v", err)
	}

	deps := factory.Dependencies{}
	var embeddingClient *embeddingGrpc.Client

	if cfg.Mode == cache.ModeSemantic {
		embeddingGrpcAddress := os.Getenv("EMBED_ADDR")
		if embeddingGrpcAddress == "" {
			embeddingGrpcAddress = "localhost:50051"
		}

		embeddingClient, err = embeddingGrpc.NewClient(embeddingGrpcAddress)
		if err != nil {
			fmt.Printf("[Error] Failed to create embedding client: %s\n", err)
			return
		}
		defer embeddingClient.Close()

		log.Printf("[Info] Fetching embedding service info...")
		embeddingInfo, err := embeddingClient.Info(context.Background())
		if err != nil {
			log.Fatalf("Failed to get embedding service info: %v", err)
		}
		log.Printf("[Info] Connected embedding service: provider=%s, model=%s, dimensions=%d",
			embeddingInfo.Provider,
			embeddingInfo.Model,
			embeddingInfo.Dimensions,
		)

		deps.Embedding = embeddingClient
		deps.Dimensions = embeddingInfo.Dimensions
	}

	cacheSvc, err := factory.New(cfg, deps)
	if err != nil {
		log.Fatalf("Failed to create cache service: %v", err)
	}

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterCacheServiceServer(s, cachegrpc.NewServer(cacheSvc))

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
		fmt.Printf("[Info] Cache received signal %s, shutting down\n", sig)
		healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
		deregister()
		s.GracefulStop()
	}()

	fmt.Printf("[Info] Cache gRPC server listening on port %s, advertise=%s\n", servePort, advertiseAddr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
