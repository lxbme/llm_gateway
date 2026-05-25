package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"llm_gateway/embedding"
	embeddingGrpc "llm_gateway/embedding/grpc"
	"llm_gateway/internal/discovery"
	"llm_gateway/internal/metrics"
	"llm_gateway/internal/tracing"
	"llm_gateway/rag"
	ragGrpc "llm_gateway/rag/grpc"
	pb "llm_gateway/rag/proto"
	ragQdrant "llm_gateway/rag/qdrant"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const serviceName = "rag"

func main() {
	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50055"
	}

	tracingShutdown, err := tracing.Init(context.Background(), serviceName)
	if err != nil {
		log.Printf("[Warn] tracing init failed: %v", err)
	}
	defer func() { _ = tracingShutdown(context.Background()) }()

	// Start metrics server first so Prometheus can observe this process even
	// while the embedding probe is still retrying — mirrors cache-service.
	go startMetricsServer()

	embeddingGrpcAddress := os.Getenv("EMBED_ADDR")
	if embeddingGrpcAddress == "" {
		embeddingGrpcAddress = "localhost:50051"
	}

	embeddingClient, err := embeddingGrpc.NewClient(embeddingGrpcAddress)
	if err != nil {
		log.Fatalf("[Error] Failed to create embedding client: %s", err)
	}
	defer embeddingClient.Close()

	embeddingInfo, err := probeEmbeddingWithBackoff(embeddingClient)
	if err != nil {
		log.Fatalf("[Error] embedding probe gave up: %v", err)
	}

	cfg, err := ragQdrant.LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("[Error] Failed to load RAG config: %s", err)
	}

	store, err := ragQdrant.New(cfg, embeddingInfo.Dimensions)
	if err != nil {
		log.Fatalf("[Error] Failed to create RAG qdrant store: %s", err)
	}

	log.Printf("[Info] RAG config: topK=%d, threshold=%.4f", cfg.DefaultTopK, cfg.SimilarityThreshold)

	ragSvc, err := rag.NewService(store, embeddingClient, int32(cfg.DefaultTopK), cfg.SimilarityThreshold)
	if err != nil {
		log.Fatalf("[Error] Failed to create RAG service: %s", err)
	}
	defer ragSvc.Close()

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("[Error] Failed to listen on port %s: %s", servePort, err)
	}

	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(metrics.GRPCServer.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(metrics.GRPCServer.StreamServerInterceptor()),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)
	pb.RegisterRagServiceServer(s, ragGrpc.NewServer(ragSvc))

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(s, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)
	metrics.GRPCServer.InitializeMetrics(s)

	advertiseAddr, err := discovery.AdvertiseAddr(servePort)
	if err != nil {
		log.Fatalf("[Error] Failed to resolve advertise addr: %s", err)
	}
	registerCtx, registerCancel := context.WithCancel(context.Background())
	defer registerCancel()
	deregister, err := discovery.Register(registerCtx, serviceName, advertiseAddr)
	if err != nil {
		log.Fatalf("[Error] Failed to register with discovery: %s", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		fmt.Printf("[Info] RAG received signal %s, shutting down\n", sig)
		healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
		deregister()
		s.GracefulStop()
	}()

	fmt.Printf("[Info] RAG gRPC server listening on port %s, advertise=%s\n", servePort, advertiseAddr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("[Error] Failed to serve: %s", err)
	}
}

func startMetricsServer() {
	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "9090"
	}
	addr := ":" + port
	log.Printf("[Info] Metrics endpoint at %s/metrics", addr)
	if err := metrics.Serve(addr); err != nil {
		log.Printf("[Error] Metrics server stopped: %v", err)
	}
}

// probeEmbeddingWithBackoff calls Info() until it succeeds or the elapsed
// budget is exhausted. Same shape and rationale as the cache-service version
// — kept inline (instead of in internal/) because the helper is tiny and
// only two services need it.
func probeEmbeddingWithBackoff(c *embeddingGrpc.Client) (embedding.Info, error) {
	const maxElapsed = 5 * time.Minute
	const perAttempt = 5 * time.Second
	backoff := time.Second
	deadline := time.Now().Add(maxElapsed)
	var lastErr error
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		attemptCtx, cancel := context.WithTimeout(context.Background(), perAttempt)
		info, err := c.Info(attemptCtx)
		cancel()
		if err == nil {
			log.Printf("[Info] embedding probe ok (attempt %d): provider=%s model=%s dim=%d",
				attempt, info.Provider, info.Model, info.Dimensions)
			return info, nil
		}
		lastErr = err
		log.Printf("[Warn] embedding probe attempt %d failed: %v; retry in %s", attempt, err, backoff)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return embedding.Info{}, fmt.Errorf("embedding probe gave up after %s: %w", maxElapsed, lastErr)
}
