package main

import (
	"fmt"
	"llm_gateway/completion/openai"
	"log"
	"net"

	"google.golang.org/grpc"

	completiongrpc "llm_gateway/completion/grpc"
	pb "llm_gateway/completion/proto"
)

const openaiCompletionEndpoint = "https://api.openai-proxy.org/v1/chat/completions"
const completionApiKeyEnvName = "OPENAI_API_KEY"
const grpcPort = 50053

func main() {
	completionService := openai.New(openaiCompletionEndpoint, completionApiKeyEnvName)

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		log.Fatalf("Failed to listen:  %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterCompletionServiceServer(s, completiongrpc.NewServer(completionService))

	fmt.Printf("[Info] Completion gRPC server listening on port %d", grpcPort)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
