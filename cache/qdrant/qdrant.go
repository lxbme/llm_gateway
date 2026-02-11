package qdrant

import (
	"context"
	"fmt"
	"sync"
	"time"

	"llm_gateway/cache"
	"llm_gateway/embedding"

	"github.com/google/uuid"
	"github.com/qdrant/go-client/qdrant"
)

// Service implements cache.Service using Qdrant as the backend
type Service struct {
	taskChan            chan cache.Task
	wg                  sync.WaitGroup
	ctx                 context.Context
	cancel              context.CancelFunc
	qdrantClient        *qdrant.Client
	dimensions          int
	collectionName      string
	similarityThreshold float32
	embeddingService    embedding.Service
}

// New creates a new Qdrant cache service
func New(bufferSize int, workerCount int, dimensions int,
	similarityThreshold float32, collectionName string,
	qdrantHost string, qdrantPort int,
	embeddingService embedding.Service) (*Service, error) {

	ctx, cancel := context.WithCancel(context.Background())
	qclient, err := qdrant.NewClient(&qdrant.Config{
		Host: qdrantHost,
		Port: qdrantPort,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("fail to create qdrant client: %w", err)
	}

	s := &Service{
		taskChan:            make(chan cache.Task, bufferSize),
		ctx:                 ctx,
		cancel:              cancel,
		qdrantClient:        qclient,
		dimensions:          dimensions,
		collectionName:      collectionName,
		similarityThreshold: similarityThreshold,
		embeddingService:    embeddingService,
	}

	err = s.createCollection()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("fail to create qdrant collection: %w", err)
	}

	s.start(workerCount)

	return s, nil
}

// Get implements cache.Service
func (s *Service) Get(ctx context.Context, question string, model string) (string, bool, error) {
	found, answer, err := s.searchSimilar(ctx, question, model)
	if err != nil {
		return "", false, err
	}
	return answer, found, nil
}

// Set implements cache.Service
func (s *Service) Set(ctx context.Context, item cache.Task) error {
	if !s.submit(item) {
		return fmt.Errorf("failed to submit task: queue is full")
	}
	return nil
}

// Shutdown implements cache.Service
func (s *Service) Shutdown() {
	fmt.Println("[Info] Shutting down cache service...")
	s.cancel()
	close(s.taskChan)
	s.wg.Wait()
	fmt.Println("[Info] Cache service stopped")
}

func (s *Service) start(workerCount int) {
	for i := 0; i < workerCount; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}
	fmt.Printf("[Info] Started %d embedding cache workers\n", workerCount)
}

func (s *Service) worker(id int) {
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
			}
		}
	}
}

func (s *Service) processTask(task cache.Task) error {
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

func (s *Service) submit(task cache.Task) bool {
	select {
	case s.taskChan <- task:
		return true
	default:
		fmt.Printf("[Warning] Embedding task queue is full, dropping task.\n")
		return false
	}
}

func (s *Service) createCollection() error {
	isExist, err := s.qdrantClient.CollectionExists(context.Background(), s.collectionName)
	if err != nil {
		return fmt.Errorf("fail to check if collection %s exists: %w", s.collectionName, err)
	}
	if !isExist {
		err = s.qdrantClient.CreateCollection(context.Background(), &qdrant.CreateCollection{
			CollectionName: s.collectionName,
			VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{
				Size:     uint64(s.dimensions),
				Distance: qdrant.Distance_Cosine,
			}),
		})
		if err != nil {
			return fmt.Errorf("fail to create collection: %w", err)
		}
		fmt.Printf("[Info] Created Qdrant collection: %s\n", s.collectionName)
	}
	return nil
}

func (s *Service) storeCache(embedded []float32, questionText string, answerText string, modelName string, tokenUsage int) error {
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

func (s *Service) searchSimilar(ctx context.Context, userPrompt string, model string) (bool, string, error) {
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
