package gateway

import (
	"context"
	"sync"

	"llm_gateway/rag"
)

type ingestJobStatus string

const (
	ingestJobQueued     ingestJobStatus = "queued"
	ingestJobProcessing ingestJobStatus = "processing"
	ingestJobDone       ingestJobStatus = "done"
	ingestJobFailed     ingestJobStatus = "failed"
)

type ingestJob struct {
	JobID      string
	Collection string
	Source     string
	Status     ingestJobStatus
	ChunkCount int
	Err        string
}

type ingestTask struct {
	jobID  string
	chunks []rag.Chunk
}

type ingestWorkerPool struct {
	taskChan chan ingestTask
	jobs     map[string]*ingestJob
	mu       sync.RWMutex
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	ragSvc   rag.Service
}

func newIngestWorkerPool(ragSvc rag.Service, bufferSize int, workerCount int) *ingestWorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &ingestWorkerPool{
		taskChan: make(chan ingestTask, bufferSize),
		jobs:     make(map[string]*ingestJob),
		ctx:      ctx,
		cancel:   cancel,
		ragSvc:   ragSvc,
	}
	for i := 0; i < workerCount; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	return p
}

func (p *ingestWorkerPool) submit(task ingestTask) bool {
	select {
	case p.taskChan <- task:
		return true
	default:
		logWarn("ingest task queue full, dropping job %s", task.jobID)
		return false
	}
}

func (p *ingestWorkerPool) Shutdown() {
	p.cancel()
	close(p.taskChan)
	p.wg.Wait()
}

func (p *ingestWorkerPool) worker(_ int) {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case task, ok := <-p.taskChan:
			if !ok {
				return
			}
			p.processTask(task)
		}
	}
}

func (p *ingestWorkerPool) processTask(task ingestTask) {
	p.mu.Lock()
	if job, ok := p.jobs[task.jobID]; ok {
		job.Status = ingestJobProcessing
	}
	p.mu.Unlock()

	_, _, err := p.ragSvc.Ingest(context.Background(), task.chunks)

	p.mu.Lock()
	if job, ok := p.jobs[task.jobID]; ok {
		if err != nil {
			job.Status = ingestJobFailed
			job.Err = err.Error()
			logError("ingest worker: job %s failed: %s", task.jobID, err)
		} else {
			job.Status = ingestJobDone
			logInfo("ingest worker: job %s done, %d chunks ingested", task.jobID, job.ChunkCount)
		}
	}
	p.mu.Unlock()
}
