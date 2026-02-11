package main

import (
	"fmt"
	"net/http"
)

const serverPort = 8080
const openaiCompletionEndpoint = "https://api.openai-proxy.org/v1/chat/completions"
const openaiEmbeddingEndpoint = "https://api.openai-proxy.org/v1/embeddings"
const embeddingModel = "text-embedding-3-small"
const embeddingDimensions = 1536
const embeddingApiKeyEnvName = "OPENAI_API_KEY"

const qdrantSimilarityThreshold = 0.93
const qdrantCollectionName = "llm_semantic_cache"
const qdrantClientPort = 6334
const qdrantHost = "localhost"

var semanticCacheService SemanticCacheService

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	embeddingService := &OpenaiEmbeddingService{}
	embeddingService.Init(openaiEmbeddingEndpoint, embeddingApiKeyEnvName, embeddingDimensions)

	qdrantService := &QdrantSemanticCacheService{}
	if err := qdrantService.InitQdrant(1000, 5, embeddingDimensions, float32(qdrantSimilarityThreshold), qdrantCollectionName, qdrantHost, qdrantClientPort, embeddingService); err != nil {
		fmt.Printf("[Error] Fail to init semantic cache service: %s\n", err)
	}
	defer qdrantService.Shutdown()

	semanticCacheService = qdrantService

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: mux,
	}
	fmt.Printf("[Info] Starting server at %d\n", serverPort)
	err := server.ListenAndServe()
	if err != nil {
		fmt.Printf("[Error] Error running http server: %s\n", err)
	}
}
