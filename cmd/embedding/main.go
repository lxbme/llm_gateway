package main

import (
	"fmt"
	"log"
	"net"
	"os"

	embeddinggrpc "llm_gateway/embedding/grpc"
	"llm_gateway/embedding/openai"

	pb "llm_gateway/embedding/proto"

	"google.golang.org/grpc"
)

const (
	// grpcPort                = 50051
	// openaiEmbeddingEndpoint = "https://api.openai-proxy.org/v1/embeddings"
	embeddingModel         = "text-embedding-3-small"
	embeddingDimensions    = 1536
	embeddingApiKeyEnvName = "EMBED_API_KEY"
)

func main() {
	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50051"
	}

	openaiEmbeddingEndpoint := os.Getenv("EMBED_ENDPOINT")
	if openaiEmbeddingEndpoint == "" {
		panic("EMBED_ENDPOINT environment variable is required")
	}

	embeddingService := openai.New(
		openaiEmbeddingEndpoint,
		embeddingModel,
		embeddingApiKeyEnvName,
		embeddingDimensions,
	)

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("Failed to listen:  %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterEmbeddingServiceServer(s, embeddinggrpc.NewServer(embeddingService))

	fmt.Printf("[Info] Embedding gRPC server listening on port %s", servePort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
