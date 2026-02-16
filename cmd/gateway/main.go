package main

import (
	"fmt"
	"net/http"
	"os"

	"llm_gateway/cache"
	cacheGrpc "llm_gateway/cache/grpc"
	"llm_gateway/completion"
	completionGrpc "llm_gateway/completion/grpc"
)

const serverPort = 8080

var semanticCacheService cache.Service
var completionService completion.Service

func main() {
	// const cacheGrpcAddress = "localhost:50052"
	// const completionGrpcAddress = "localhost:50053"

	cacheGrpcAddress := os.Getenv("CACHE_ADDR")
	if cacheGrpcAddress == "" {
		cacheGrpcAddress = "localhost:50052"
	}

	completionGrpcAddress := os.Getenv("COMPL_ADDR")
	if completionGrpcAddress == "" {
		completionGrpcAddress = "localhost:50053"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	// Initialize cache service
	cacheSvc, err := cacheGrpc.NewClient(cacheGrpcAddress)
	if err != nil {
		fmt.Printf("[Error] Fail to init semantic cache service: %s\n", err)
		return
	}
	defer cacheSvc.Close()

	// Initialize completion service
	completionSvc, err := completionGrpc.NewClient(completionGrpcAddress)
	if err != nil {
		fmt.Printf("[Error] Fail to init completion service: %s\n", err)
		return
	}
	defer completionSvc.Close()

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
