package main

import (
	"context"
	authgrpc "llm_gateway/auth/grpc"
	pb "llm_gateway/auth/proto"
	"llm_gateway/auth/redis"
	"llm_gateway/internal/discovery"
	"llm_gateway/internal/logging"
	"llm_gateway/internal/metrics"
	"llm_gateway/internal/tracing"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const serviceName = "auth"

func main() {
	logging.Init(serviceName)

	redisAddr := os.Getenv("REDIS_ADDR")
	password := os.Getenv("REDIS_PASSWORD")
	dbNumber := os.Getenv("REDIS_DB")

	if redisAddr == "" {
		panic("REDIS_ADDR not set")
	}
	if dbNumber == "" {
		panic("REDIS_DB not set")
	}

	db, err := strconv.Atoi(dbNumber)
	if err != nil {
		panic("REDIS_DB must be a valid integer: " + err.Error())
	}

	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50054"
	}

	tracingShutdown, err := tracing.Init(context.Background(), serviceName)
	if err != nil {
		slog.Warn("tracing init failed", "err", err)
	}
	defer func() { _ = tracingShutdown(context.Background()) }()

	authService, err := redis.NewRedisAuthService(redisAddr, password, db)
	if err != nil {
		slog.Error("auth service init failed", "err", err)
		os.Exit(1)
	}

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		slog.Error("listen failed", "port", servePort, "err", err)
		os.Exit(1)
	}

	s := grpc.NewServer(
		grpc.ChainUnaryInterceptor(metrics.GRPCServer.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(metrics.GRPCServer.StreamServerInterceptor()),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)
	pb.RegisterAuthServiceServer(s, authgrpc.NewServer(authService))

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(s, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_SERVING)
	metrics.GRPCServer.InitializeMetrics(s)

	go startMetricsServer()

	advertiseAddr, err := discovery.AdvertiseAddr(servePort)
	if err != nil {
		slog.Error("resolve advertise addr failed", "err", err)
		os.Exit(1)
	}
	registerCtx, registerCancel := context.WithCancel(context.Background())
	defer registerCancel()
	deregister, err := discovery.Register(registerCtx, serviceName, advertiseAddr)
	if err != nil {
		slog.Error("discovery register failed", "err", err)
		os.Exit(1)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		slog.Info("shutdown signal received", "signal", sig.String())
		healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
		deregister()
		s.GracefulStop()
	}()

	slog.Info("grpc server listening", "port", servePort, "advertise", advertiseAddr)
	if err := s.Serve(lis); err != nil {
		slog.Error("serve failed", "err", err)
		os.Exit(1)
	}
}

func startMetricsServer() {
	port := os.Getenv("METRICS_PORT")
	if port == "" {
		port = "9090"
	}
	addr := ":" + port
	slog.Info("metrics endpoint listening", "addr", addr)
	if err := metrics.Serve(addr); err != nil {
		slog.Error("metrics server stopped", "err", err)
	}
}
