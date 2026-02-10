package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

type SemanticCacheTask struct {
	CollectionName string
	UserPrompt     string
	AIResponse     string
	Dimension      int
	ModelName      string
	TokenUsage     int
}

type SemanticCacheService struct {
	taskChan            chan SemanticCacheTask
	wg                  sync.WaitGroup
	ctx                 context.Context
	cancel              context.CancelFunc
	qdrantClient        *qdrant.Client
	EmbeddingHttpClient *http.Client
}

func NewSemanticCacheService(bufferSize int) *SemanticCacheService {
	ctx, cancel := context.WithCancel(context.Background())
	qclient, err := qdrant.NewClient(&qdrant.Config{
		Host: qdrantHost,
		Port: qdrantClientPort,
	})
	if err != nil {
		fmt.Printf("Fail to create qdrant client in service: %s", err)
	}
	return &SemanticCacheService{
		taskChan:            make(chan SemanticCacheTask, bufferSize),
		ctx:                 ctx,
		cancel:              cancel,
		qdrantClient:        qclient,
		EmbeddingHttpClient: &http.Client{},
	}
}

func (s *SemanticCacheService) Start(workerCount int) {
	for i := 0; i < workerCount; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}
	fmt.Printf("[Info] Started %d embedding cache workers\n", workerCount)
}

func (s *SemanticCacheService) worker(id int) {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			fmt.Printf("[Info] Worker %d shutting down...\n", id)
			return
		case task, ok := <-s.taskChan:
			if !ok {
				fmt.Printf("[Info] Worker %d: channel closed\n", id)
				return
			}

			if err := s.processTask(task); err != nil {
				fmt.Printf("[Error] Worker %d fail to process task: %s\n", id, err)
				//TODO: add to fail queue?
			}
		}
	}
}

func (s *SemanticCacheService) processTask(task SemanticCacheTask) error {
	embedding, err := GetEmbedding(s.EmbeddingHttpClient, task.UserPrompt, task.Dimension)
	if err != nil {
		return fmt.Errorf("fail to get embedding in worker: %w", err)
	}

	err = QdrantStoreCache(s.qdrantClient, task.CollectionName, embedding, task.UserPrompt, task.AIResponse, task.ModelName, task.TokenUsage)
	if err != nil {
		return fmt.Errorf("fail to store embedding to qdrant in worker: %w", err)
	}

	fmt.Printf("[Info] Successfully stored embedding for prompt: %.10s...\n", task.UserPrompt)
	return nil
}

func (s *SemanticCacheService) Submit(task SemanticCacheTask) bool {
	select {
	case s.taskChan <- task:
		return true
	default:
		fmt.Printf("[Warning] Embedding task queue is full, dropping task.\n")
		return false
	}
}

func (s *SemanticCacheService) Shutdown() {
	fmt.Println("[Info] Shutting down cache service...")
	close(s.taskChan)
	s.wg.Wait()
	fmt.Println("[Info] Cache service stopped")
}

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
func GetEmbedding(client *http.Client, input string, dimensions int) ([]float32, error) {
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

func QdrantStoreCache(client *qdrant.Client, collectionName string, embedded []float32, questionText string, answerText string, modelName string, tokenUsage int) error {
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
