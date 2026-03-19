package cleanup

import (
	"context"
	"time"
)

const (
	defaultBatchSize     = 100
	defaultRunInterval   = time.Minute
	defaultTaskRetention = 72 * time.Hour
)

type Config struct {
	BatchSize     int
	Interval      time.Duration
	TaskRetention time.Duration
	Now           func() time.Time
}

type repository interface {
	DeleteExpiredTasksBatch(ctx context.Context, finishedBefore time.Time, limit int) (int64, error)
	DeleteExpiredPayloadsBatch(ctx context.Context, expiresBefore time.Time, limit int) (int64, error)
}

type Cleaner struct {
	repo          repository
	batchSize     int
	interval      time.Duration
	taskRetention time.Duration
	now           func() time.Time
}

func NewCleaner(repo repository, cfg Config) *Cleaner {
	return &Cleaner{
		repo:          repo,
		batchSize:     intOrDefault(cfg.BatchSize, defaultBatchSize),
		interval:      durationOrDefault(cfg.Interval, defaultRunInterval),
		taskRetention: durationOrDefault(cfg.TaskRetention, defaultTaskRetention),
		now:           nowOrDefault(cfg.Now),
	}
}

func (c *Cleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.RunOnce(ctx)
		}
	}
}

func (c *Cleaner) RunOnce(ctx context.Context) error {
	now := c.now().UTC()
	if _, err := c.repo.DeleteExpiredTasksBatch(ctx, now.Add(-c.taskRetention), c.batchSize); err != nil {
		return err
	}
	if _, err := c.repo.DeleteExpiredPayloadsBatch(ctx, now, c.batchSize); err != nil {
		return err
	}
	return nil
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
