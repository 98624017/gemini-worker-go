package cache

import (
	"fmt"
	"sync"
	"time"

	"banana-async-gateway/internal/domain"
)

const (
	defaultRunningTTL  = 5 * time.Second
	defaultTerminalTTL = 45 * time.Second
	defaultListTTL     = 5 * time.Second
)

type Config struct {
	RunningTTL  time.Duration
	TerminalTTL time.Duration
	ListTTL     time.Duration
	Now         func() time.Time
}

type TaskCache struct {
	mu          sync.Mutex
	tasks       map[string]taskEntry
	lists       map[string]listEntry
	runningTTL  time.Duration
	terminalTTL time.Duration
	listTTL     time.Duration
	now         func() time.Time
}

type taskEntry struct {
	value     *domain.Task
	expiresAt time.Time
}

type listEntry struct {
	value     []domain.TaskSummary
	expiresAt time.Time
}

func NewTaskCache(cfg Config) *TaskCache {
	return &TaskCache{
		tasks:       map[string]taskEntry{},
		lists:       map[string]listEntry{},
		runningTTL:  durationOrDefault(cfg.RunningTTL, defaultRunningTTL),
		terminalTTL: durationOrDefault(cfg.TerminalTTL, defaultTerminalTTL),
		listTTL:     durationOrDefault(cfg.ListTTL, defaultListTTL),
		now:         nowOrDefault(cfg.Now),
	}
}

func (c *TaskCache) GetTask(taskID string) (*domain.Task, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.tasks[taskID]
	if !ok {
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		delete(c.tasks, taskID)
		return nil, false
	}
	return cloneTask(entry.value), true
}

func (c *TaskCache) SetTask(task *domain.Task) {
	if task == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.tasks[task.ID] = taskEntry{
		value:     cloneTask(task),
		expiresAt: c.now().Add(c.taskTTL(task.Status)),
	}
}

func (c *TaskCache) GetTaskList(key string) ([]domain.TaskSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.lists[key]
	if !ok {
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		delete(c.lists, key)
		return nil, false
	}
	return cloneTaskSummaries(entry.value), true
}

func (c *TaskCache) SetTaskList(key string, items []domain.TaskSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lists[key] = listEntry{
		value:     cloneTaskSummaries(items),
		expiresAt: c.now().Add(c.listTTL),
	}
}

func ListKey(ownerHash string, days, limit int, beforeCreatedAt *time.Time, beforeID string) string {
	beforeUnix := int64(0)
	if beforeCreatedAt != nil {
		beforeUnix = beforeCreatedAt.Unix()
	}
	return fmt.Sprintf("%s:%d:%d:%d:%s", ownerHash, days, limit, beforeUnix, beforeID)
}

func (c *TaskCache) taskTTL(status domain.TaskStatus) time.Duration {
	switch status {
	case domain.TaskStatusSucceeded, domain.TaskStatusFailed, domain.TaskStatusUncertain:
		return c.terminalTTL
	default:
		return c.runningTTL
	}
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
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

func cloneTask(task *domain.Task) *domain.Task {
	if task == nil {
		return nil
	}
	cloned := *task
	if task.HeartbeatAt != nil {
		value := *task.HeartbeatAt
		cloned.HeartbeatAt = &value
	}
	if task.RequestDispatchedAt != nil {
		value := *task.RequestDispatchedAt
		cloned.RequestDispatchedAt = &value
	}
	if task.FinishedAt != nil {
		value := *task.FinishedAt
		cloned.FinishedAt = &value
	}
	cloned.ResultSummary = cloneResultSummary(task.ResultSummary)
	return &cloned
}

func cloneTaskSummaries(items []domain.TaskSummary) []domain.TaskSummary {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]domain.TaskSummary, 0, len(items))
	for _, item := range items {
		copyItem := item
		if item.FinishedAt != nil {
			value := *item.FinishedAt
			copyItem.FinishedAt = &value
		}
		copyItem.ResultSummary = cloneResultSummary(item.ResultSummary)
		cloned = append(cloned, copyItem)
	}
	return cloned
}

func cloneResultSummary(summary *domain.ResultSummary) *domain.ResultSummary {
	if summary == nil {
		return nil
	}
	cloned := *summary
	if len(summary.ImageURLs) > 0 {
		cloned.ImageURLs = append([]string(nil), summary.ImageURLs...)
	}
	if len(summary.UsageMetadata) > 0 {
		cloned.UsageMetadata = make(map[string]any, len(summary.UsageMetadata))
		for key, value := range summary.UsageMetadata {
			cloned.UsageMetadata[key] = value
		}
	}
	if summary.OpenAIImageResult != nil {
		cloned.OpenAIImageResult = &domain.OpenAIImageResult{
			Created: summary.OpenAIImageResult.Created,
		}
		if len(summary.OpenAIImageResult.Data) > 0 {
			cloned.OpenAIImageResult.Data = append([]domain.OpenAIImageData(nil), summary.OpenAIImageResult.Data...)
		}
		if len(summary.OpenAIImageResult.Usage) > 0 {
			cloned.OpenAIImageResult.Usage = make(map[string]any, len(summary.OpenAIImageResult.Usage))
			for key, value := range summary.OpenAIImageResult.Usage {
				cloned.OpenAIImageResult.Usage[key] = value
			}
		}
	}
	return &cloned
}
