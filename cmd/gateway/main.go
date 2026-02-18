package main

import (
	"fmt"
	"net/http"
	"net/http/pprof"
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

	debugMode := os.Getenv("DEBUG_MODE")

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	// Register pprof handlers
	if debugMode == "true" {
		logInfo("Debug mode on")
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	// Initialize cache service
	cacheSvc, err := cacheGrpc.NewClient(cacheGrpcAddress)
	if err != nil {
		logError("Fail to init semantic cache service: %s", err)
		return
	}
	defer cacheSvc.Close()

	// Initialize completion service
	completionSvc, err := completionGrpc.NewClient(completionGrpcAddress)
	if err != nil {
		logError("Fail to init completion service: %s", err)
		return
	}
	defer completionSvc.Close()

	semanticCacheService = cacheSvc
	completionService = completionSvc

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: mux,
	}
	logInfo("Starting server at %d", serverPort)
	err = server.ListenAndServe()
	if err != nil {
		logError("Error running http server: %s", err)
	}
}
