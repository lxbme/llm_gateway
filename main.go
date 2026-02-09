package main

import (
	"fmt"
	"net/http"
)

const serverPort = 8080
const qdrant_collection_name = "llm_semantic_cache"
const qdrant_client_port = 6334
const qdrant_host = "localhost"

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", CompletionHandle)

	err := CreateQdrantcollection(qdrant_host, qdrant_client_port, qdrant_collection_name)
	if err != nil {
		fmt.Printf("[Error] Fail to create qdrant collection: %s", err)
	}

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
