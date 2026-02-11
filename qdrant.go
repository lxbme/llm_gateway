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

type QdrantSemanticCacheService struct {
	taskChan            chan SemanticCacheTask
	wg                  sync.WaitGroup
	ctx                 context.Context
	cancel              context.CancelFunc
	qdrantClient        *qdrant.Client
	dimensions          int
	collectionName      string
	similarityThreshold float32
	embeddingService    EmbeddingService
}

// Get implements [SemanticCacheService].
func (s *QdrantSemanticCacheService) Get(ctx context.Context, question string, model string) (string, bool, error) {
	found, answer, err := s.searchSimilar(ctx, question, model)
	if err != nil {
		return "", false, err
	}
	return answer, found, nil
}

// InitQdrant initializes the Qdrant semantic cache service
func (s *QdrantSemanticCacheService) InitQdrant(bufferSize int, workerCount int, dimensions int,
	similarityThreshold float32, collectionName string, qdrantHost string, qdrantPort int, embeddingService EmbeddingService) error {

	ctx, cancel := context.WithCancel(context.Background())
	qclient, err := qdrant.NewClient(&qdrant.Config{
		Host: qdrantHost,
		Port: qdrantPort,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("fail to create init qdrant client: %w", err)
	}

	s.taskChan = make(chan SemanticCacheTask, bufferSize)
	s.ctx = ctx
	s.cancel = cancel
	s.qdrantClient = qclient
	s.dimensions = dimensions
	s.collectionName = collectionName
	s.similarityThreshold = similarityThreshold
	s.embeddingService = embeddingService

	err = createQdrantCollection(s.qdrantClient, dimensions, collectionName)
	if err != nil {
		return fmt.Errorf("fail to create qdrant collection: %w", err)
	}

	s.start(workerCount)

	return nil
}

// Set implements [SemanticCacheService].
func (s *QdrantSemanticCacheService) Set(ctx context.Context, item SemanticCacheTask) error {
	if !s.submit(item) {
		return fmt.Errorf("failed to submit task: queue is full")
	}
	return nil
}

// Shutdown implements [SemanticCacheService].
func (s *QdrantSemanticCacheService) Shutdown() {
	fmt.Println("[Info] Shutting down cache service...")
	close(s.taskChan)
	s.wg.Wait()
	fmt.Println("[Info] Cache service stopped")
}

func NewSemanticCacheService(bufferSize int, threshold float32) *QdrantSemanticCacheService {
	ctx, cancel := context.WithCancel(context.Background())
	qclient, err := qdrant.NewClient(&qdrant.Config{
		Host: qdrantHost,
		Port: qdrantClientPort,
	})
	if err != nil {
		fmt.Printf("Fail to create qdrant client in service: %s", err)
	}
	return &QdrantSemanticCacheService{
		taskChan:     make(chan SemanticCacheTask, bufferSize),
		ctx:          ctx,
		cancel:       cancel,
		qdrantClient: qclient,
		// EmbeddingHttpClient: &http.Client{},
		similarityThreshold: threshold,
	}
}

func (s *QdrantSemanticCacheService) start(workerCount int) {
	for i := 0; i < workerCount; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}
	fmt.Printf("[Info] Started %d embedding cache workers\n", workerCount)
}

func (s *QdrantSemanticCacheService) worker(id int) {
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

func (s *QdrantSemanticCacheService) processTask(task SemanticCacheTask) error {
	// embedding, err := GetEmbedding(s.EmbeddingHttpClient, task.UserPrompt, task.Dimension)
	embedding, err := s.embeddingService.Get(context.Background(), task.UserPrompt)
	if err != nil {
		return fmt.Errorf("fail to get embedding in worker: %w", err)
	}

	err = s.storeCache(embedding, task.UserPrompt, task.AIResponse, task.ModelName, task.TokenUsage)
	if err != nil {
		return fmt.Errorf("fail to store embedding to qdrant in worker: %w", err)
	}

	fmt.Printf("[Info] Successfully stored embedding for prompt: %.10s...\n", task.UserPrompt)
	return nil
}

func (s *QdrantSemanticCacheService) submit(task SemanticCacheTask) bool {
	select {
	case s.taskChan <- task:
		return true
	default:
		fmt.Printf("[Warning] Embedding task queue is full, dropping task.\n")
		return false
	}
}

func createQdrantCollection(client *qdrant.Client, dimensions int, collectionName string) error {
	is_exist, err := client.CollectionExists(context.Background(), collectionName)
	if err != nil {
		return fmt.Errorf("Fail to check if collection %s exist or not: %s", collectionName, err)
	}
	if !is_exist {
		err = client.CreateCollection(context.Background(), &qdrant.CreateCollection{
			CollectionName: collectionName,
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

// storeCache stores embedding cache to Qdrant using service resources
func (s *QdrantSemanticCacheService) storeCache(embedded []float32, questionText string, answerText string, modelName string, tokenUsage int) error {
	timeStamp := time.Now().Unix()
	pointId := uuid.New().String()
	_, err := s.qdrantClient.Upsert(context.Background(), &qdrant.UpsertPoints{
		CollectionName: s.collectionName,
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

// searchSimilar searches for similar cache entries using service resources
func (s *QdrantSemanticCacheService) searchSimilar(ctx context.Context, userPrompt string, model string) (bool, string, error) {
	embedding, err := s.embeddingService.Get(ctx, userPrompt)
	if err != nil {
		return false, "", err
	}
	searchResult, err := s.qdrantClient.Query(ctx, &qdrant.QueryPoints{
		CollectionName: s.collectionName,
		Query:          qdrant.NewQueryDense(embedding),
		Filter: &qdrant.Filter{
			Must: []*qdrant.Condition{
				qdrant.NewMatch("model", model),
			},
		},
		WithPayload:    qdrant.NewWithPayload(true),
		ScoreThreshold: qdrant.PtrOf(s.similarityThreshold),
	})
	if err != nil {
		return false, "", fmt.Errorf("fail to search qdrant: %w", err)
	}
	if len(searchResult) == 0 {
		return false, "", nil
	}
	answer, ok := searchResult[0].Payload["answer"]
	if !ok {
		return false, "", nil
	}
	fmt.Printf("[Info] Hit cache: %s\n", searchResult[0].Id.GetUuid())
	return true, answer.GetStringValue(), nil
}

type OpenaiEmbeddingService struct {
	endpoint   string
	apiKeyEnv  string
	client     *http.Client
	dimensions int
}

func (s *OpenaiEmbeddingService) Get(ctx context.Context, question string) ([]float32, error) {
	return s.getEmbedding(question)
}

func (s *OpenaiEmbeddingService) Init(endpoint string, apiKeyEnvName string, dimension int) {
	s.endpoint = endpoint
	s.apiKeyEnv = apiKeyEnvName
	s.client = &http.Client{}
	s.dimensions = dimension
}

// getEmbedding gets embedding vector from embedding endpoint
func (s *OpenaiEmbeddingService) getEmbedding(input string) ([]float32, error) {
	requestBody := EmbeddingRequest{
		Model:          embeddingModel,
		Input:          input,
		EncodingFormat: "float",
		Dimensions:     int32(s.dimensions),
	}
	requestBodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("fail to marshal embedding request body: %w", err)
	}
	req, err := http.NewRequest("POST", s.endpoint, bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("fail to new embedding request: %w", err)
	}
	apiKey := os.Getenv(s.apiKeyEnv)
	if apiKey == "" {
		return nil, fmt.Errorf("empty openai api key")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
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
