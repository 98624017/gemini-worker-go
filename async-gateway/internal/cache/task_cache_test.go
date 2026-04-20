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

func TestTaskCacheClonesOpenAIImageUsageDeeply(t *testing.T) {
	t.Parallel()

	original := &domain.ResultSummary{
		OpenAIImageResult: &domain.OpenAIImageResult{
			Created: 1710000000,
			Usage: map[string]any{
				"nested": map[string]any{
					"count": 1,
				},
				"items": []any{
					"a",
					map[string]any{"value": "keep"},
				},
			},
		},
	}

	cloned := cloneResultSummary(original)
	if cloned == nil || cloned.OpenAIImageResult == nil {
		t.Fatalf("expected cloned OpenAIImageResult: %#v", cloned)
	}

	clonedNested, ok := cloned.OpenAIImageResult.Usage["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map: %#v", cloned.OpenAIImageResult.Usage)
	}
	clonedNested["count"] = 2

	clonedItems, ok := cloned.OpenAIImageResult.Usage["items"].([]any)
	if !ok || len(clonedItems) != 2 {
		t.Fatalf("expected items slice: %#v", cloned.OpenAIImageResult.Usage)
	}
	clonedItems[0] = "changed"
	clonedItemMap, ok := clonedItems[1].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map inside items: %#v", clonedItems)
	}
	clonedItemMap["value"] = "mutated"

	originalNested := original.OpenAIImageResult.Usage["nested"].(map[string]any)
	if originalNested["count"] != 1 {
		t.Fatalf("original nested map was mutated: %#v", originalNested)
	}

	originalItems := original.OpenAIImageResult.Usage["items"].([]any)
	if originalItems[0] != "a" {
		t.Fatalf("original items[0] was mutated: %#v", originalItems)
	}
	originalItemMap := originalItems[1].(map[string]any)
	if originalItemMap["value"] != "keep" {
		t.Fatalf("original nested map in items was mutated: %#v", originalItemMap)
	}
}
