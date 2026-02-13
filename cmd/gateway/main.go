package main

import (
	"fmt"
	"net/http"

	"llm_gateway/cache"
	cacheGrpc "llm_gateway/cache/grpc"
	"llm_gateway/completion"
	openaiCompletionService "llm_gateway/completion/openai"
)

const serverPort = 8080
const openaiCompletionEndpoint = "https://api.openai-proxy.org/v1/chat/completions"
const completionApiKeyEnvName = "OPENAI_API_KEY"

const cacheGrpcAddress = "localhost:50052"
const qdrantHost = "localhost"

var semanticCacheService cache.Service
var completionService completion.Service

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	// Initialize cache service
	cacheSvc, err := cacheGrpc.NewClient(cacheGrpcAddress)
	if err != nil {
		fmt.Printf("[Error] Fail to init semantic cache service: %s\n", err)
		return
	}
	defer cacheSvc.Close()

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
