package main

import (
	"fmt"
	"log"
	"net"

	embeddinggrpc "llm_gateway/embedding/grpc"
	"llm_gateway/embedding/openai"

	pb "llm_gateway/embedding/proto"

	"google.golang.org/grpc"
)

const (
	grpcPort                = 50051
	openaiEmbeddingEndpoint = "https://api.openai-proxy.org/v1/embeddings"
	embeddingModel          = "text-embedding-3-small"
	embeddingDimensions     = 1536
	embeddingApiKeyEnvName  = "OPENAI_API_KEY"
)

func main() {
	embeddingService := openai.New(
		openaiEmbeddingEndpoint,
		embeddingModel,
		embeddingApiKeyEnvName,
		embeddingDimensions,
	)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		log.Fatalf("Failed to listen:  %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterEmbeddingServiceServer(s, embeddinggrpc.NewServer(embeddingService))

	fmt.Printf("[Info] Embedding gRPC server listening on port %d", grpcPort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
