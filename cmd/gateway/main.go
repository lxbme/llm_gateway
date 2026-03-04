package main

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"

	"llm_gateway/auth"
	authGrpc "llm_gateway/auth/grpc"
	"llm_gateway/cache"
	cacheGrpc "llm_gateway/cache/grpc"
	"llm_gateway/completion"
	completionGrpc "llm_gateway/completion/grpc"
)

const serverPort = 8080
const adminPort = 8081

var semanticCacheService cache.Service
var completionService completion.Service
var authService auth.Service

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

	authGrpcAddress := os.Getenv("AUTH_ADDR")
	if authGrpcAddress == "" {
		authGrpcAddress = "localhost:50054"
	}

	debugMode := os.Getenv("DEBUG_MODE")

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

	// Initialize auth service
	authSvc, err := authGrpc.NewClient(authGrpcAddress)
	if err != nil {
		logError("Fail to init auth service: %s", err)
		return
	}

	// register service as public
	semanticCacheService = cacheSvc
	completionService = completionSvc
	authService = authSvc

	// admin handler
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("POST /admin/create", handleRedisCreate)
	adminMux.HandleFunc("POST /admin/get", handleRedisGet)
	adminMux.HandleFunc("POST /admin/delete", handleRedisDelete)

	go http.ListenAndServe(fmt.Sprintf(":%d", adminPort), Chain(
		adminMux,
		AdminCheckMiddleware,
	))
	logInfo("Starting admin service at %d", adminPort)

	// completion handler
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", Chain(
		http.HandlerFunc(CompletionHandle),
		CORSMiddleware,
		AuthCheckMiddleware,
	))

	// Register pprof handlers
	if debugMode == "true" {
		logInfo("Debug mode on")
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: mux,
	}
	logInfo("Starting completion service at %d", serverPort)
	err = server.ListenAndServe()
	if err != nil {
		logError("Error running http server: %s", err)
	}

}
