package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"

	authGrpc "llm_gateway/auth/grpc"
	cacheGrpc "llm_gateway/cache/grpc"
	completionGrpc "llm_gateway/completion/grpc"
	"llm_gateway/gateway"
	"llm_gateway/internal/logging"
	"llm_gateway/internal/metrics"
	"llm_gateway/internal/tracing"
	ragGrpc "llm_gateway/rag/grpc"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const serverPort = 8080
const adminPort = 8081

func main() {
	logging.Init("gateway")
	tracingShutdown, err := tracing.Init(context.Background(), "gateway")
	if err != nil {
		slog.Warn("tracing init failed", "err", err)
	}
	defer func() { _ = tracingShutdown(context.Background()) }()

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
		slog.Error("cache client init failed", "err", err)
		return
	}
	defer cacheSvc.Close()

	// Initialize completion service
	completionSvc, err := completionGrpc.NewClient(completionGrpcAddress)
	if err != nil {
		slog.Error("completion client init failed", "err", err)
		return
	}
	defer completionSvc.Close()

	// Initialize auth service
	authSvc, err := authGrpc.NewClient(authGrpcAddress)
	if err != nil {
		slog.Error("auth client init failed", "err", err)
		return
	}
	defer authSvc.Close()

	deps := gateway.Dependencies{
		Auth:            authSvc,
		Cache:           cacheSvc,
		Completion:      completionSvc,
		CompletionStats: completionSvc,
		CompletionAdmin: completionSvc,
	}

	// RAG service is optional: omit RAG_ADDR to run without it.
	ragGrpcAddress := os.Getenv("RAG_ADDR")
	if ragGrpcAddress != "" {
		ragSvc, err := ragGrpc.NewClient(ragGrpcAddress)
		if err != nil {
			slog.Error("rag client init failed", "err", err)
			return
		}
		defer ragSvc.Close()
		deps.RAG = ragSvc
		slog.Info("rag client connected", "addr", ragGrpcAddress)
	}

	gatewayServer := gateway.NewServer(deps)
	defer gatewayServer.Shutdown()

	mux := http.NewServeMux()
	gatewayServer.RegisterPublicRoutes(mux)

	go func() {
		adminAddr := fmt.Sprintf(":%d", adminPort)
		slog.Info("admin server listening", "port", adminPort)
		if err := http.ListenAndServe(adminAddr, gatewayServer.AdminHandler()); err != nil {
			slog.Error("admin server stopped", "err", err)
		}
	}()

	go func() {
		metricsAddr := os.Getenv("METRICS_ADDR")
		if metricsAddr == "" {
			metricsAddr = "127.0.0.1:9090"
		}
		slog.Info("metrics endpoint listening", "addr", metricsAddr)
		if err := metrics.Serve(metricsAddr); err != nil {
			slog.Error("metrics server stopped", "err", err)
		}
	}()

	if debugMode == "true" {
		slog.Info("debug mode on")
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	handler := otelhttp.NewHandler(gateway.WithMetricsMiddleware(mux), "gateway.http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
	httpServer := http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: handler,
	}
	slog.Info("gateway http server listening", "port", serverPort)
	err = httpServer.ListenAndServe()
	if err != nil {
		slog.Error("http server stopped", "err", err)
	}

}
