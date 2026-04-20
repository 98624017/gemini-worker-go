package recovery

import (
	"context"
	"testing"
	"time"

	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/queue"
	"banana-async-gateway/internal/store"
)

func TestScannerRequeuesRecoverableTasks(t *testing.T) {
	t.Parallel()

	now := time.Unix(1773964800, 0).UTC()
	cases := []struct {
		name string
		task *domain.Task
	}{
		{
			name: "accepted",
			task: &domain.Task{ID: "task-1", Status: domain.TaskStatusAccepted},
		},
		{
			name: "queued",
			task: &domain.Task{ID: "task-2", Status: domain.TaskStatusQueued},
		},
		{
			name: "running without dispatch",
			task: &domain.Task{ID: "task-3", Status: domain.TaskStatusRunning},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := &recoveryRepositoryStub{
				items: []store.RecoverableTask{{Task: tc.task, HasPayload: true}},
			}
			queueStub := &recoveryQueueStub{}
			scanner := NewScanner(repo, queueStub, Config{
				Now: func() time.Time {
					return now
				},
			})

			if err := scanner.Run(context.Background()); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if len(queueStub.items) != 1 || queueStub.items[0].TaskID != tc.task.ID {
				t.Fatalf("unexpected queue items = %#v", queueStub.items)
			}
			if len(repo.markQueuedIDs) != 1 || repo.markQueuedIDs[0] != tc.task.ID {
				t.Fatalf("unexpected queued ids = %#v", repo.markQueuedIDs)
			}
		})
	}
}

func TestScannerMarksDispatchedRunningTaskUncertain(t *testing.T) {
	t.Parallel()

	dispatchedAt := time.Unix(1773964810, 0).UTC()
	repo := &recoveryRepositoryStub{
		items: []store.RecoverableTask{{
			Task: &domain.Task{
				ID:                  "task-1",
				Status:              domain.TaskStatusRunning,
				RequestDispatchedAt: &dispatchedAt,
			},
			HasPayload: true,
		}},
	}
	scanner := NewScanner(repo, &recoveryQueueStub{}, Config{})

	if err := scanner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if repo.markUncertainID != "task-1" {
		t.Fatalf("expected uncertain mark, got %#v", repo)
	}
}

func TestScannerSkipsFreshDispatchedRunningTask(t *testing.T) {
	t.Parallel()

	now := time.Unix(1773964800, 0).UTC()
	freshHeartbeat := now.Add(-30 * time.Second)
	dispatchedAt := now.Add(-15 * time.Second)
	repo := &recoveryRepositoryStub{
		items: []store.RecoverableTask{{
			Task: &domain.Task{
				ID:                  "task-1",
				Status:              domain.TaskStatusRunning,
				HeartbeatAt:         &freshHeartbeat,
				RequestDispatchedAt: &dispatchedAt,
			},
			HasPayload: true,
		}},
	}
	queueStub := &recoveryQueueStub{}
	scanner := NewScanner(repo, queueStub, Config{
		StaleThreshold: 5 * time.Minute,
		Now: func() time.Time {
			return now
		},
	})

	if err := scanner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if repo.markUncertainID != "" {
		t.Fatalf("fresh running task should not be marked uncertain, got %#v", repo)
	}
	if len(repo.markQueuedIDs) != 0 {
		t.Fatalf("fresh running task should not be requeued, got %#v", repo.markQueuedIDs)
	}
	if repo.finishFailedID != "" {
		t.Fatalf("fresh running task should not be failed, got %#v", repo)
	}
	if len(queueStub.items) != 0 {
		t.Fatalf("fresh running task should not be enqueued, got %#v", queueStub.items)
	}
}

func TestScannerMarksMissingPayloadFailed(t *testing.T) {
	t.Parallel()

	repo := &recoveryRepositoryStub{
		items: []store.RecoverableTask{{
			Task:       &domain.Task{ID: "task-1", Status: domain.TaskStatusAccepted},
			HasPayload: false,
		}},
	}
	scanner := NewScanner(repo, &recoveryQueueStub{}, Config{})

	if err := scanner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if repo.finishFailedID != "task-1" || repo.finishFailedCode != "recovery_payload_missing" {
		t.Fatalf("expected payload missing failure, got %#v", repo)
	}
}

type recoveryRepositoryStub struct {
	items            []store.RecoverableTask
	markQueuedIDs    []string
	finishFailedID   string
	finishFailedCode string
	markUncertainID  string
}

func (s *recoveryRepositoryStub) FindRecoverableTasks(context.Context, time.Time, int) ([]store.RecoverableTask, error) {
	return s.items, nil
}

func (s *recoveryRepositoryStub) MarkQueued(_ context.Context, taskID string) error {
	s.markQueuedIDs = append(s.markQueuedIDs, taskID)
	return nil
}

func (s *recoveryRepositoryStub) FinishFailed(_ context.Context, taskID, errorCode, _ string) error {
	s.finishFailedID = taskID
	s.finishFailedCode = errorCode
	return nil
}

func (s *recoveryRepositoryStub) MarkUncertain(_ context.Context, taskID, _, _ string) error {
	s.markUncertainID = taskID
	return nil
}

type recoveryQueueStub struct {
	items []queue.TaskItem
}

func (s *recoveryQueueStub) TryEnqueue(item queue.TaskItem) bool {
	s.items = append(s.items, item)
	return true
}
