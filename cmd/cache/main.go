package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strconv"

	cachegrpc "llm_gateway/cache/grpc"
	pb "llm_gateway/cache/proto"
	"llm_gateway/cache/qdrant"
	embeddingGrpc "llm_gateway/embedding/grpc"

	"google.golang.org/grpc"
)

const qdrantSimilarityThreshold = 0.95
const qdrantCollectionName = "llm_semantic_cache"

// const qdrantClientPort = 6334
// const qdrantHost = "localhost"
const qdrantCacheBufferSize = 1000
const qdrantCacheWorkerSize = 5

const embeddingDimensions = 1536

func main() {
	embeddingGrpcAddress := os.Getenv("EMBED_ADDR")
	if embeddingGrpcAddress == "" {
		embeddingGrpcAddress = "localhost:50051"
	}

	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50052"
	}

	qdrantHost := os.Getenv("QDRANT_HOST")
	if qdrantHost == "" {
		qdrantHost = "localhost"
	}

	qdrantClientPortStr := os.Getenv("QDRANT_PORT")
	if qdrantClientPortStr == "" {
		qdrantClientPortStr = "6334"
	}
	qdrantClientPort, err := strconv.Atoi(qdrantClientPortStr)
	if err != nil {
		log.Fatalf("Invalid QDRANT_PORT: %v", err)
	}

	embeddingService, err := embeddingGrpc.NewClient(embeddingGrpcAddress)
	if err != nil {
		fmt.Printf("[Error] Failed to create embedding client: %s\n", err)
		return
	}
	defer embeddingService.Close()

	cacheSvc, err := qdrant.New(
		qdrantCacheBufferSize,
		qdrantCacheWorkerSize,
		embeddingDimensions,
		float32(qdrantSimilarityThreshold),
		qdrantCollectionName,
		qdrantHost,
		qdrantClientPort,
		embeddingService,
	)
	if err != nil {
		log.Fatalf("Failed to create cache service: %v", err)
	}

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterCacheServiceServer(s, cachegrpc.NewServer(cacheSvc))

	fmt.Printf("[Info] Cache gRPC server listening on port %s\n", servePort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
