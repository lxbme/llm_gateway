package main

import (
	"fmt"
	"net/http"

	"llm_gateway/cache"
	qdrantCache "llm_gateway/cache/qdrant"
	"llm_gateway/completion"
	openaiCompletionService "llm_gateway/completion/openai"
	embeddingGrpc "llm_gateway/embedding/grpc"
)

const serverPort = 8080
const openaiCompletionEndpoint = "https://api.openai-proxy.org/v1/chat/completions"
const completionApiKeyEnvName = "OPENAI_API_KEY"

const embeddingDimensions = 1536
const embeddingGrpcAddress = "localhost:50051"

const qdrantSimilarityThreshold = 0.95
const qdrantCollectionName = "llm_semantic_cache"
const qdrantClientPort = 6334
const qdrantHost = "localhost"

var semanticCacheService cache.Service
var completionService completion.Service

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	// Initialize embedding service (grpc client)
	embeddingService, err := embeddingGrpc.NewClient(embeddingGrpcAddress)
	if err != nil {
		fmt.Printf("[Error] Failed to create embedding client: %s\n", err)
		return
	}
	defer embeddingService.Close()

	// Initialize cache service
	cacheSvc, err := qdrantCache.New(
		1000, // bufferSize
		5,    // workerCount
		embeddingDimensions,
		float32(qdrantSimilarityThreshold),
		qdrantCollectionName,
		qdrantHost,
		qdrantClientPort,
		embeddingService,
	)
	if err != nil {
		fmt.Printf("[Error] Fail to init semantic cache service: %s\n", err)
		return
	}
	defer cacheSvc.Shutdown()

	completionSvc := openaiCompletionService.New(
		openaiCompletionEndpoint,
		completionApiKeyEnvName,
	)

	semanticCacheService = cacheSvc
	completionService = completionSvc

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: mux,
	}
	fmt.Printf("[Info] Starting server at %d\n", serverPort)
	err = server.ListenAndServe()
	if err != nil {
		fmt.Printf("[Error] Error running http server: %s\n", err)
	}
}
