package queue

import (
	"context"
	"errors"
	"sync"
)

var ErrQueueClosed = errors.New("memory queue is closed")

type TaskItem struct {
	TaskID string
}

type MemoryQueue struct {
	items     chan TaskItem
	closed    chan struct{}
	closeOnce sync.Once
}

func NewMemoryQueue(capacity int) *MemoryQueue {
	if capacity <= 0 {
		capacity = 1
	}
	return &MemoryQueue{
		items:  make(chan TaskItem, capacity),
		closed: make(chan struct{}),
	}
}

func (q *MemoryQueue) TryEnqueue(item TaskItem) bool {
	select {
	case <-q.closed:
		return false
	default:
	}

	select {
	case q.items <- item:
		return true
	default:
		return false
	}
}

func (q *MemoryQueue) Dequeue(ctx context.Context) (TaskItem, error) {
	select {
	case <-ctx.Done():
		return TaskItem{}, ctx.Err()
	case item, ok := <-q.items:
		if !ok {
			return TaskItem{}, ErrQueueClosed
		}
		return item, nil
	case <-q.closed:
		select {
		case item, ok := <-q.items:
			if !ok {
				return TaskItem{}, ErrQueueClosed
			}
			return item, nil
		default:
			return TaskItem{}, ErrQueueClosed
		}
	}
}

func (q *MemoryQueue) Len() int {
	return len(q.items)
}

func (q *MemoryQueue) Close() {
	q.closeOnce.Do(func() {
		close(q.closed)
		close(q.items)
	})
}
