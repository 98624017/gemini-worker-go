package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/queue"
	"banana-async-gateway/internal/store"
)

const defaultHeartbeatInterval = 15 * time.Second

type workerRepository interface {
	GetTaskByID(ctx context.Context, taskID string) (*domain.Task, error)
	GetTaskPayload(ctx context.Context, taskID string) (*domain.TaskPayload, error)
	MarkRunning(ctx context.Context, taskID, workerID string, heartbeatAt time.Time) error
	UpdateHeartbeat(ctx context.Context, taskID string, heartbeatAt time.Time) error
	MarkRequestDispatched(ctx context.Context, taskID string, dispatchedAt time.Time) error
	FinishSucceeded(ctx context.Context, taskID string, summary *domain.ResultSummary) error
	FinishFailed(ctx context.Context, taskID, errorCode, errorMessage string) error
	MarkUncertain(ctx context.Context, taskID, errorCode, errorMessage string) error
}

type workerQueue interface {
	Dequeue(ctx context.Context) (queue.TaskItem, error)
}

type queueCloser interface {
	Close()
}

type taskForwarder interface {
	Forward(ctx context.Context, task *domain.Task, payload *domain.TaskPayload, onDispatched func(context.Context) error) (ForwardOutcome, error)
}

type Pool struct {
	logger            *log.Logger
	repo              workerRepository
	queue             workerQueue
	queueCloser       queueCloser
	forwarder         taskForwarder
	workerCount       int
	workerID          string
	workerIDPrefix    string
	heartbeatInterval time.Duration
	now               func() time.Time

	startOnce sync.Once
	runCtx    context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewPool(cfg config.Config, logger *log.Logger, repo *store.Repository, taskQueue *queue.MemoryQueue) (*Pool, error) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	forwarder, err := NewForwarder(cfg)
	if err != nil {
		return nil, err
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "async-gateway"
	}

	return &Pool{
		logger:            logger,
		repo:              repo,
		queue:             taskQueue,
		queueCloser:       taskQueue,
		forwarder:         forwarder,
		workerCount:       maxWorkerCount(cfg.MaxInflightTasks),
		workerIDPrefix:    hostname,
		heartbeatInterval: defaultHeartbeatInterval,
		now:               time.Now,
	}, nil
}

func (p *Pool) Start() {
	if p == nil {
		return
	}

	p.startOnce.Do(func() {
		p.runCtx, p.cancel = context.WithCancel(context.Background())
		for i := 0; i < maxWorkerCount(p.workerCount); i++ {
			workerID := p.makeWorkerID(i)
			p.wg.Add(1)
			go p.workerLoop(workerID)
		}
	})
}

func (p *Pool) CloseQueue() {
	if p != nil && p.queueCloser != nil {
		p.queueCloser.Close()
	}
}

func (p *Pool) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pool) workerLoop(workerID string) {
	defer p.wg.Done()

	for {
		item, err := p.queue.Dequeue(p.runCtx)
		if err != nil {
			if errors.Is(err, queue.ErrQueueClosed) || errors.Is(err, context.Canceled) {
				return
			}
			p.logger.Printf("worker dequeue failed: worker_id=%s err=%v", workerID, err)
			continue
		}

		if err := p.processTaskWithWorker(p.runCtx, item, workerID); err != nil {
			p.logger.Printf("worker task failed: worker_id=%s task_id=%s err=%v", workerID, item.TaskID, err)
		}
	}
}

func (p *Pool) processTask(ctx context.Context, item queue.TaskItem) error {
	return p.processTaskWithWorker(ctx, item, p.workerID)
}

func (p *Pool) processTaskWithWorker(ctx context.Context, item queue.TaskItem, workerID string) error {
	task, err := p.repo.GetTaskByID(ctx, item.TaskID)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	payload, err := p.repo.GetTaskPayload(ctx, item.TaskID)
	if err != nil {
		return fmt.Errorf("get task payload: %w", err)
	}

	now := p.currentTime()
	if err := p.repo.MarkRunning(ctx, item.TaskID, p.resolveWorkerID(workerID), now); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	heartbeatStop := make(chan struct{})
	var heartbeatWG sync.WaitGroup
	if p.heartbeatInterval > 0 {
		heartbeatWG.Add(1)
		go p.heartbeatLoop(ctx, item.TaskID, heartbeatStop, &heartbeatWG)
	}

	outcome, err := p.forwarder.Forward(ctx, task, payload, func(callbackCtx context.Context) error {
		return p.repo.MarkRequestDispatched(callbackCtx, item.TaskID, p.currentTime())
	})

	close(heartbeatStop)
	heartbeatWG.Wait()

	if err != nil {
		return fmt.Errorf("forward task: %w", err)
	}
	if outcome.TransportUncertain {
		return p.repo.MarkUncertain(ctx, item.TaskID, outcome.ErrorCode, outcome.ErrorMessage)
	}
	if outcome.ErrorCode != "" {
		return p.repo.FinishFailed(ctx, item.TaskID, outcome.ErrorCode, outcome.ErrorMessage)
	}
	return p.repo.FinishSucceeded(ctx, item.TaskID, outcome.Summary)
}

func (p *Pool) heartbeatLoop(ctx context.Context, taskID string, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	ticker := time.NewTicker(p.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.repo.UpdateHeartbeat(ctx, taskID, p.currentTime()); err != nil {
				p.logger.Printf("update heartbeat failed: task_id=%s err=%v", taskID, err)
			}
		}
	}
}

func (p *Pool) currentTime() time.Time {
	if p.now == nil {
		return time.Now().UTC()
	}
	return p.now().UTC()
}

func (p *Pool) resolveWorkerID(workerID string) string {
	if workerID != "" {
		return workerID
	}
	if p.workerID != "" {
		return p.workerID
	}
	return "worker-1"
}

func (p *Pool) makeWorkerID(index int) string {
	if p.workerIDPrefix == "" {
		return fmt.Sprintf("worker-%d", index+1)
	}
	return fmt.Sprintf("%s-%d", p.workerIDPrefix, index+1)
}

func maxWorkerCount(value int) int {
	if value <= 0 {
		return 1
	}
	return value
}
