package main

import (
	"fmt"
	"llm_gateway/completion/openai"
	"log"
	"net"
	"os"

	"google.golang.org/grpc"

	completiongrpc "llm_gateway/completion/grpc"
	pb "llm_gateway/completion/proto"
)

// const openaiCompletionEndpoint = "https://api.openai-proxy.org/v1/chat/completions"
const completionApiKeyEnvName = "COMPL_API_KEY"

// const grpcPort = "50053"
func main() {
	servePort := os.Getenv("SERVE_PORT")
	if servePort == "" {
		servePort = "50053"
	}

	openaiCompletionEndpoint := os.Getenv("COMPL_ENDPOINT")
	if openaiCompletionEndpoint == "" {
		panic("COMPL_ENDPOINT environment variable is not set")
	}

	completionService := openai.New(openaiCompletionEndpoint, completionApiKeyEnvName)

	lis, err := net.Listen("tcp", ":"+servePort)
	if err != nil {
		log.Fatalf("Failed to listen:  %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterCompletionServiceServer(s, completiongrpc.NewServer(completionService))

	fmt.Printf("[Info] Completion gRPC server listening on port %s\n", servePort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
