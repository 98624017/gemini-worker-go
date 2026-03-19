package cache

import (
	"testing"
	"time"

	"banana-async-gateway/internal/domain"
)

func TestTaskCacheExpiresRunningTaskQuickly(t *testing.T) {
	t.Parallel()

	now := time.Unix(1773964800, 0).UTC()
	cache := NewTaskCache(Config{
		RunningTTL:  2 * time.Second,
		TerminalTTL: 30 * time.Second,
		ListTTL:     5 * time.Second,
		Now: func() time.Time {
			return now
		},
	})

	cache.SetTask(&domain.Task{ID: "task-1", Status: domain.TaskStatusRunning})
	if _, ok := cache.GetTask("task-1"); !ok {
		t.Fatalf("expected running task cache hit")
	}

	now = now.Add(3 * time.Second)
	if _, ok := cache.GetTask("task-1"); ok {
		t.Fatalf("expected running task cache miss after ttl")
	}
}

func TestTaskCacheKeepsTerminalTaskAndListWithinTTL(t *testing.T) {
	t.Parallel()

	now := time.Unix(1773964800, 0).UTC()
	cache := NewTaskCache(Config{
		RunningTTL:  2 * time.Second,
		TerminalTTL: 30 * time.Second,
		ListTTL:     5 * time.Second,
		Now: func() time.Time {
			return now
		},
	})

	cache.SetTask(&domain.Task{ID: "task-2", Status: domain.TaskStatusSucceeded})
	cache.SetTaskList("owner:list", []domain.TaskSummary{{ID: "task-2", Status: domain.TaskStatusSucceeded}})

	now = now.Add(4 * time.Second)
	if _, ok := cache.GetTask("task-2"); !ok {
		t.Fatalf("expected terminal task cache hit within ttl")
	}
	if _, ok := cache.GetTaskList("owner:list"); !ok {
		t.Fatalf("expected list cache hit within ttl")
	}

	now = now.Add(2 * time.Second)
	if _, ok := cache.GetTaskList("owner:list"); ok {
		t.Fatalf("expected list cache miss after ttl")
	}
}
