package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

func CreateQdrantcollection(client *qdrant.Client, dimensions int, collection_name string) error {
	is_exist, err := client.CollectionExists(context.Background(), collection_name)
	if err != nil {
		return fmt.Errorf("Fail to check if collection %s exist or not: %s", collection_name, err)
	}
	if !is_exist {
		err = client.CreateCollection(context.Background(), &qdrant.CreateCollection{
			CollectionName: collection_name,
			VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
				Size:     uint64(dimensions),
				Distance: qdrant.Distance_Cosine,
			}),
		})
		if err != nil {
			return fmt.Errorf("Fail to create collection")
		}

		return nil
	} else {
		return nil
	}
}

// get embedding vector from embedding endpoint
func GetEmbedding(input string, dimensions int, client *http.Client) ([]float32, error) {
	requestBody := EmbeddingRequest{
		Model:          embeddingModel,
		Input:          input,
		EncodingFormat: "float",
		Dimensions:     int32(dimensions),
	}
	requestBodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("fail to marshal embedding request body: %w", err)
	}
	req, err := http.NewRequest("POST", openaiEmbeddingEndpoint, bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("fail to new embedding request: %w", err)
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("empty openai api key")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fail to do embedding request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding request fail: (%d) %s", resp.StatusCode, body)
	}
	if err != nil {
		return nil, fmt.Errorf("fail to read embedding response body: %w", err)
	}
	var respBody EmbeddingResponse
	if err := json.Unmarshal(body, &respBody); err != nil {
		return nil, fmt.Errorf("fail to unmarshal embedding response: %w", err)
	}
	if len(respBody.Data) == 0 {
		return nil, fmt.Errorf("empty embedding response data")
	}
	return respBody.Data[0].Embedding, nil
}

func QdrantStoreCache(collectionName string, embedded []float32, questionText string, answerText string, modelName string, tokenUsage int, client *qdrant.Client) error {
	timeStamp := time.Now().Unix()
	pointId := uuid.New().String()
	_, err := client.Upsert(context.Background(), &qdrant.UpsertPoints{
		CollectionName: collectionName,
		Points: []*qdrant.PointStruct{
			{
				Id:      qdrant.NewID(pointId),
				Vectors: qdrant.NewVectorsDense(embedded),
				Payload: qdrant.NewValueMap(map[string]any{
					"question":   questionText,
					"answer":     answerText,
					"model":      modelName,
					"tokenUsage": tokenUsage,
					"timestamp":  timeStamp,
				}),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("fail to store qdrant point: %w", err)
	}
	return nil
}
