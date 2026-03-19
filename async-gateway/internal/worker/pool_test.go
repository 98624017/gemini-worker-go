package worker

import (
	"context"
	"testing"
	"time"

	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/queue"
)

func TestPoolProcessOneMarksRunningAndFinishesSucceeded(t *testing.T) {
	t.Parallel()

	repo := &workerRepositoryStub{
		task: &domain.Task{
			ID:           "task-1",
			Model:        "gemini-3-pro-image-preview",
			RequestPath:  "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			RequestQuery: "output=url",
		},
		payload: &domain.TaskPayload{
			TaskID:          "task-1",
			RequestBodyJSON: []byte(`{"contents":[]}`),
		},
	}
	forwarder := &forwarderStub{
		outcome: ForwardOutcome{
			Summary: &domain.ResultSummary{ImageURLs: []string{"https://example.com/final.png"}},
		},
	}

	pool := &Pool{
		repo:              repo,
		forwarder:         forwarder,
		workerID:          "worker-1",
		heartbeatInterval: time.Hour,
	}

	if err := pool.processTask(context.Background(), queue.TaskItem{TaskID: "task-1"}); err != nil {
		t.Fatalf("processTask() error = %v", err)
	}
	if repo.markRunningID != "task-1" || repo.finishSucceededID != "task-1" {
		t.Fatalf("unexpected repository calls: %#v", repo)
	}
}

func TestPoolProcessOneMarksUncertainWhenForwarderReturnsTransportUncertain(t *testing.T) {
	t.Parallel()

	repo := &workerRepositoryStub{
		task: &domain.Task{
			ID:           "task-1",
			Model:        "gemini-3-pro-image-preview",
			RequestPath:  "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			RequestQuery: "output=url",
		},
		payload: &domain.TaskPayload{
			TaskID:          "task-1",
			RequestBodyJSON: []byte(`{"contents":[]}`),
		},
	}
	forwarder := &forwarderStub{
		outcome: ForwardOutcome{
			ErrorCode:          "upstream_transport_uncertain",
			ErrorMessage:       "connection dropped",
			TransportUncertain: true,
		},
	}

	pool := &Pool{
		repo:              repo,
		forwarder:         forwarder,
		workerID:          "worker-1",
		heartbeatInterval: time.Hour,
	}

	if err := pool.processTask(context.Background(), queue.TaskItem{TaskID: "task-1"}); err != nil {
		t.Fatalf("processTask() error = %v", err)
	}
	if repo.markUncertainID != "task-1" {
		t.Fatalf("expected uncertain mark, got %#v", repo)
	}
}

type workerRepositoryStub struct {
	task                    *domain.Task
	payload                 *domain.TaskPayload
	markRunningID           string
	finishSucceededID       string
	finishFailedID          string
	markUncertainID         string
	markRequestDispatchedID string
}

func (s *workerRepositoryStub) GetTaskByID(context.Context, string) (*domain.Task, error) {
	return s.task, nil
}

func (s *workerRepositoryStub) GetTaskPayload(context.Context, string) (*domain.TaskPayload, error) {
	return s.payload, nil
}

func (s *workerRepositoryStub) MarkRunning(_ context.Context, taskID, _ string, _ time.Time) error {
	s.markRunningID = taskID
	return nil
}

func (s *workerRepositoryStub) UpdateHeartbeat(context.Context, string, time.Time) error {
	return nil
}

func (s *workerRepositoryStub) MarkRequestDispatched(_ context.Context, taskID string, _ time.Time) error {
	s.markRequestDispatchedID = taskID
	return nil
}

func (s *workerRepositoryStub) FinishSucceeded(_ context.Context, taskID string, _ *domain.ResultSummary) error {
	s.finishSucceededID = taskID
	return nil
}

func (s *workerRepositoryStub) FinishFailed(_ context.Context, taskID, _, _ string) error {
	s.finishFailedID = taskID
	return nil
}

func (s *workerRepositoryStub) MarkUncertain(_ context.Context, taskID, _, _ string) error {
	s.markUncertainID = taskID
	return nil
}

type forwarderStub struct {
	outcome ForwardOutcome
	err     error
}

func (s *forwarderStub) Forward(ctx context.Context, task *domain.Task, payload *domain.TaskPayload, onDispatched func(context.Context) error) (ForwardOutcome, error) {
	if onDispatched != nil {
		if err := onDispatched(ctx); err != nil {
			return ForwardOutcome{}, err
		}
	}
	return s.outcome, s.err
}
