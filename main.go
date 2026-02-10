package main

import (
	"fmt"
	"net/http"

	"github.com/qdrant/go-client/qdrant"
)

const serverPort = 8080
const qdrantCollectionName = "llm_semantic_cache"
const qdrantClientPort = 6334
const qdrantHost = "localhost"
const openaiCompletionEndpoint = "https://api.openai-proxy.org/v1/chat/completions"
const openaiEmbeddingEndpoint = "https://api.openai-proxy.org/v1/embeddings"
const embeddingModel = "text-embedding-3-small"
const embeddingDimensions = 1536
const similarityThreshold = 0.93

var semanticCacheService *SemanticCacheService

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	qclient, err := qdrant.NewClient(&qdrant.Config{
		Host: qdrantHost,
		Port: qdrantClientPort,
	})
	if err != nil {
		fmt.Printf("Fail to create qdrant client: %s", err)
	}
	err = CreateQdrantcollection(qclient, embeddingDimensions, qdrantCollectionName)
	if err != nil {
		fmt.Printf("[Error] Fail to create qdrant collection: %s", err)
	}

	semanticCacheService = NewSemanticCacheService(1000)
	semanticCacheService.Start(5)
	defer semanticCacheService.Shutdown()

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
