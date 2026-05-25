package cache

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"llm_gateway/embedding"
)

// SemanticService coordinates embedding generation and vector store operations.
type SemanticService struct {
	store            Store
	embeddingService embedding.Service
	taskChan         chan Task
	wg               sync.WaitGroup
	ctx              context.Context
	cancel           context.CancelFunc
}

func NewSemanticService(store Store, embeddingService embedding.Service, bufferSize int, workerCount int) (*SemanticService, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if embeddingService == nil {
		return nil, fmt.Errorf("embedding service is required")
	}

	capabilities := store.Capabilities()
	if !capabilities.SupportsSemantic {
		return nil, fmt.Errorf("store does not support semantic cache mode")
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &SemanticService{
		store:            store,
		embeddingService: embeddingService,
		taskChan:         make(chan Task, bufferSize),
		ctx:              ctx,
		cancel:           cancel,
	}
	s.start(workerCount)

	return s, nil
}

func (s *SemanticService) Get(ctx context.Context, question string, model string) (string, bool, error) {
	vector, err := s.embeddingService.Get(ctx, question)
	if err != nil {
		return "", false, fmt.Errorf("failed to get embedding: %w", err)
	}

	return s.store.Search(ctx, Query{
		Vector: vector,
		Text:   question,
		Model:  model,
	})
}

func (s *SemanticService) Set(ctx context.Context, item Task) error {
	_ = ctx
	if !s.submit(item) {
		return fmt.Errorf("failed to submit task: queue is full")
	}
	return nil
}

func (s *SemanticService) Shutdown() {
	s.cancel()
	close(s.taskChan)
	s.wg.Wait()
	_ = s.store.Close()
}

func (s *SemanticService) start(workerCount int) {
	for i := 0; i < workerCount; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}
	slog.Info("semantic cache workers started", "count", workerCount)
}

func (s *SemanticService) worker(id int) {
	defer s.wg.Done()

	for {
		select {
		case <-s.ctx.Done():
			slog.Info("cache worker shutting down", "worker_id", id)
			return
		case task, ok := <-s.taskChan:
			if !ok {
				slog.Info("cache worker channel closed", "worker_id", id)
				return
			}

			if err := s.processTask(task); err != nil {
				slog.Error("cache worker task failed", "worker_id", id, "err", err)
			}
		}
	}
}

func (s *SemanticService) processTask(task Task) error {
	vector, err := s.embeddingService.Get(context.Background(), task.UserPrompt)
	if err != nil {
		return fmt.Errorf("failed to get embedding in worker: %w", err)
	}

	if err := s.store.Insert(context.Background(), Record{
		Vector:     vector,
		UserPrompt: task.UserPrompt,
		AIResponse: task.AIResponse,
		ModelName:  task.ModelName,
		TokenUsage: task.TokenUsage,
	}); err != nil {
		return fmt.Errorf("failed to store semantic cache item: %w", err)
	}

	// Do NOT include any portion of task.UserPrompt or AIResponse — both can
	// contain sensitive user data. Length is enough for sizing telemetry.
	slog.Info("cache write ok",
		"model", task.ModelName,
		"prompt_chars", len(task.UserPrompt),
		"answer_chars", len(task.AIResponse),
	)
	return nil
}

func (s *SemanticService) submit(task Task) bool {
	select {
	case s.taskChan <- task:
		return true
	default:
		slog.Warn("cache embedding queue full, dropping task")
		return false
	}
}
