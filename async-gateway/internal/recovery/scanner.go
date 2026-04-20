package recovery

import (
	"context"
	"io"
	"log"
	"time"

	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/queue"
	"banana-async-gateway/internal/store"
)

const (
	defaultStaleThreshold = 5 * time.Minute
	defaultScanLimit      = 1000

	recoveryPayloadMissingMessage = "recovery scan could not replay task because task payload is missing"
	recoveryUncertainMessage      = "connection to newapi broke after request dispatch; task result may be uncertain"
)

type Config struct {
	StaleThreshold time.Duration
	ScanLimit      int
	Now            func() time.Time
	Logger         *log.Logger
}

type repository interface {
	FindRecoverableTasks(ctx context.Context, staleBefore time.Time, limit int) ([]store.RecoverableTask, error)
	MarkQueued(ctx context.Context, taskID string) error
	FinishFailed(ctx context.Context, taskID, errorCode, errorMessage string) error
	MarkUncertain(ctx context.Context, taskID, errorCode, errorMessage string) error
}

type taskQueue interface {
	TryEnqueue(item queue.TaskItem) bool
}

type Scanner struct {
	repo           repository
	queue          taskQueue
	staleThreshold time.Duration
	scanLimit      int
	now            func() time.Time
	logger         *log.Logger
}

func NewScanner(repo repository, taskQueue taskQueue, cfg Config) *Scanner {
	logger := cfg.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	return &Scanner{
		repo:           repo,
		queue:          taskQueue,
		staleThreshold: durationOrDefault(cfg.StaleThreshold, defaultStaleThreshold),
		scanLimit:      intOrDefault(cfg.ScanLimit, defaultScanLimit),
		now:            nowOrDefault(cfg.Now),
		logger:         logger,
	}
}

func (s *Scanner) Run(ctx context.Context) error {
	items, err := s.repo.FindRecoverableTasks(ctx, s.now().UTC().Add(-s.staleThreshold), s.scanLimit)
	if err != nil {
		return err
	}

	for _, item := range items {
		if err := s.handleTask(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) handleTask(ctx context.Context, item store.RecoverableTask) error {
	if item.Task == nil {
		return nil
	}
	if item.Task.Status == domain.TaskStatusRunning && !s.isRunningTaskStale(item.Task) {
		return nil
	}
	if requiresPayload(item.Task.Status) && !item.HasPayload {
		return s.repo.FinishFailed(ctx, item.Task.ID, "recovery_payload_missing", recoveryPayloadMissingMessage)
	}
	if item.Task.Status == domain.TaskStatusRunning && item.Task.RequestDispatchedAt != nil {
		s.logger.Printf("event=recovery_marked_uncertain task_id=%s status=%s", item.Task.ID, item.Task.Status)
		return s.repo.MarkUncertain(ctx, item.Task.ID, "upstream_transport_uncertain", recoveryUncertainMessage)
	}
	if !item.HasPayload {
		return s.repo.FinishFailed(ctx, item.Task.ID, "recovery_payload_missing", recoveryPayloadMissingMessage)
	}
	if err := s.repo.MarkQueued(ctx, item.Task.ID); err != nil {
		return err
	}
	if !s.queue.TryEnqueue(queue.TaskItem{TaskID: item.Task.ID}) {
		return s.repo.FinishFailed(ctx, item.Task.ID, "queue_full", "local task queue is full")
	}
	s.logger.Printf("event=recovery_requeued task_id=%s status=%s", item.Task.ID, item.Task.Status)
	return nil
}

func (s *Scanner) isRunningTaskStale(task *domain.Task) bool {
	if task == nil || task.Status != domain.TaskStatusRunning {
		return false
	}

	cutoff := s.now().UTC().Add(-s.staleThreshold)
	if task.HeartbeatAt == nil {
		return true
	}
	return task.HeartbeatAt.Before(cutoff)
}

func requiresPayload(status domain.TaskStatus) bool {
	switch status {
	case domain.TaskStatusAccepted, domain.TaskStatusQueued, domain.TaskStatusRunning:
		return true
	default:
		return false
	}
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func intOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func nowOrDefault(fn func() time.Time) func() time.Time {
	if fn != nil {
		return fn
	}
	return time.Now
}
