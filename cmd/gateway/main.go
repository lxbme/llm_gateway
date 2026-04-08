package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"os"

	authGrpc "llm_gateway/auth/grpc"
	cacheGrpc "llm_gateway/cache/grpc"
	completionGrpc "llm_gateway/completion/grpc"
	"llm_gateway/gateway"
	ragGrpc "llm_gateway/rag/grpc"
)

const serverPort = 8080
const adminPort = 8081

func main() {
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
		log.Printf("[Error] Fail to init semantic cache service: %s", err)
		return
	}
	defer cacheSvc.Close()

	// Initialize completion service
	completionSvc, err := completionGrpc.NewClient(completionGrpcAddress)
	if err != nil {
		log.Printf("[Error] Fail to init completion service: %s", err)
		return
	}
	defer completionSvc.Close()

	// Initialize auth service
	authSvc, err := authGrpc.NewClient(authGrpcAddress)
	if err != nil {
		log.Printf("[Error] Fail to init auth service: %s", err)
		return
	}
	defer authSvc.Close()

	deps := gateway.Dependencies{
		Auth:       authSvc,
		Cache:      cacheSvc,
		Completion: completionSvc,
	}

	// RAG service is optional: omit RAG_ADDR to run without it.
	ragGrpcAddress := os.Getenv("RAG_ADDR")
	if ragGrpcAddress != "" {
		ragSvc, err := ragGrpc.NewClient(ragGrpcAddress)
		if err != nil {
			log.Printf("[Error] Fail to init RAG service: %s", err)
			return
		}
		defer ragSvc.Close()
		deps.RAG = ragSvc
		log.Printf("[Info] RAG service connected: %s", ragGrpcAddress)
	}

	gatewayServer := gateway.NewServer(deps)
	defer gatewayServer.Shutdown()

	mux := http.NewServeMux()
	gatewayServer.RegisterPublicRoutes(mux)

	go func() {
		adminAddr := fmt.Sprintf(":%d", adminPort)
		log.Printf("[Info] Starting admin service at %d", adminPort)
		if err := http.ListenAndServe(adminAddr, gatewayServer.AdminHandler()); err != nil {
			log.Printf("[Error] Error running admin server: %s", err)
		}
	}()

	if debugMode == "true" {
		log.Printf("[Info] Debug mode on")
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	httpServer := http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: mux,
	}
	log.Printf("[Info] Starting completion service at %d", serverPort)
	err = httpServer.ListenAndServe()
	if err != nil {
		log.Printf("[Error] Error running http server: %s", err)
	}

}
