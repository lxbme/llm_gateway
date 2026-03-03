package main

import (
	"fmt"
	authgrpc "llm_gateway/auth/grpc"
	pb "llm_gateway/auth/proto"
	"llm_gateway/auth/redis"
	"log"
	"net"
	"os"
	"strconv"

	"google.golang.org/grpc"
)

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
	fmt.Printf("[Info] Auth gRPC server listening on port %s\n", servePort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
