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

	"llm_gateway/cache"
	"llm_gateway/cache/factory"
	cachegrpc "llm_gateway/cache/grpc"
	pb "llm_gateway/cache/proto"
	"llm_gateway/embedding"
	embeddingGrpc "llm_gateway/embedding/grpc"
	"llm_gateway/internal/discovery"
	"llm_gateway/internal/metrics"
	"llm_gateway/internal/tracing"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
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

	tracingShutdown, err := tracing.Init(context.Background(), serviceName)
	if err != nil {
		log.Printf("[Warn] tracing init failed: %v", err)
	}
	defer func() { _ = tracingShutdown(context.Background()) }()

	// Start metrics server first so Prometheus can observe this process even
	// while the (potentially slow) embedding probe is still retrying. Without
	// this, a flapping embedding-service would cause cache-service to appear
	// "down" in Prometheus indistinguishably from a real crash.
	go startMetricsServer()

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

		embeddingInfo, err := probeEmbeddingWithBackoff(embeddingClient)
		if err != nil {
			log.Fatalf("[Error] embedding probe gave up: %v", err)
		}

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

	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(metrics.GRPCServer.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(metrics.GRPCServer.StreamServerInterceptor()),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)
	pb.RegisterCacheServiceServer(s, cachegrpc.NewServer(cacheSvc))

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(s, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)
	metrics.GRPCServer.InitializeMetrics(s)

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
// budget is exhausted. Embedding-service may legitimately take dozens of
// seconds to come up (model loading, ollama warmup), and during a brief
// outage we want cache-service to wait it out rather than crash and lose
// metrics. Each attempt has its own short ctx timeout so a hung dial does
// not stall the whole budget.
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
