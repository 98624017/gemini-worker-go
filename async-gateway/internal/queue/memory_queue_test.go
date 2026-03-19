package queue

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryQueueTryEnqueueAndDequeue(t *testing.T) {
	t.Parallel()

	q := NewMemoryQueue(1)

	if !q.TryEnqueue(TaskItem{TaskID: "task-1"}) {
		t.Fatalf("expected enqueue success")
	}
	if q.TryEnqueue(TaskItem{TaskID: "task-2"}) {
		t.Fatalf("expected second enqueue to fail on full queue")
	}

	item, err := q.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("Dequeue() error = %v", err)
	}
	if item.TaskID != "task-1" {
		t.Fatalf("TaskID = %q, want %q", item.TaskID, "task-1")
	}
}

func TestMemoryQueueCloseStopsDequeue(t *testing.T) {
	t.Parallel()

	q := NewMemoryQueue(1)
	q.Close()

	_, err := q.Dequeue(context.Background())
	if !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("expected ErrQueueClosed, got %v", err)
	}
}
