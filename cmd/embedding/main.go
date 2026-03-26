package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"llm_gateway/embedding"
	"llm_gateway/embedding/factory"
	embeddinggrpc "llm_gateway/embedding/grpc"

	pb "llm_gateway/embedding/proto"

	"google.golang.org/grpc"
)

// const (
// 	// grpcPort                = 50051
// 	// openaiEmbeddingEndpoint = "https://api.openai-proxy.org/v1/embeddings"
// 	embeddingModel         = "text-embedding-3-small"
// 	embeddingDimensions    = 1536
// 	embeddingApiKeyEnvName = "EMBED_API_KEY"
// )

func main() {
	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50051"
	}

	cfg, err := embedding.LoadConfigFromEnv()
	if err != nil {
		panic(fmt.Sprintf("failed to load embedding config: %v", err))
	}

	svc, err := factory.New(cfg)
	if err != nil {
		panic(fmt.Sprintf("failed to create embedding service: %v", err))
	}

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("Failed to listen:  %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterEmbeddingServiceServer(s, embeddinggrpc.NewServer(svc))

	fmt.Printf("[Info] Embedding gRPC server listening on port %s", servePort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
