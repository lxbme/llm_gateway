package main

import (
	"context"
	"fmt"
	authgrpc "llm_gateway/auth/grpc"
	pb "llm_gateway/auth/proto"
	"llm_gateway/auth/redis"
	"llm_gateway/internal/discovery"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const serviceName = "auth"

func main() {
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

	authService, err := redis.NewRedisAuthService(redisAddr, password, db)
	if err != nil {
		log.Fatalf("Failed to create cache service: %v", err)
		panic("[Panic] Failed to create cache service")
	}

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterAuthServiceServer(s, authgrpc.NewServer(authService))

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
		fmt.Printf("[Info] Auth received signal %s, shutting down\n", sig)
		healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		healthSrv.SetServingStatus(serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
		deregister()
		s.GracefulStop()
	}()

	fmt.Printf("[Info] Auth gRPC server listening on port %s, advertise=%s\n", servePort, advertiseAddr)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
