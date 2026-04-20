package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	embeddingGrpc "llm_gateway/embedding/grpc"
	"llm_gateway/rag"
	ragGrpc "llm_gateway/rag/grpc"
	pb "llm_gateway/rag/proto"
	ragQdrant "llm_gateway/rag/qdrant"

	"google.golang.org/grpc"
)

func main() {
	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50055"
	}

	embeddingGrpcAddress := os.Getenv("EMBED_ADDR")
	if embeddingGrpcAddress == "" {
		embeddingGrpcAddress = "localhost:50051"
	}

	embeddingClient, err := embeddingGrpc.NewClient(embeddingGrpcAddress)
	if err != nil {
		log.Fatalf("[Error] Failed to create embedding client: %s", err)
	}
	defer embeddingClient.Close()

	log.Printf("[Info] Fetching embedding service info...")
	embeddingInfo, err := embeddingClient.Info(context.Background())
	if err != nil {
		log.Fatalf("[Error] Failed to get embedding service info: %s", err)
	}
	log.Printf("[Info] Connected embedding service: provider=%s, model=%s, dimensions=%d",
		embeddingInfo.Provider,
		embeddingInfo.Model,
		embeddingInfo.Dimensions,
	)

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

	s := grpc.NewServer()
	pb.RegisterRagServiceServer(s, ragGrpc.NewServer(ragSvc))

	fmt.Printf("[Info] RAG gRPC server listening on port %s\n", servePort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("[Error] Failed to serve: %s", err)
	}
}
