package cleanup

import (
	"context"
	"testing"
	"time"
)

func TestCleanerRunOnceUsesExpectedCutoffs(t *testing.T) {
	t.Parallel()

	now := time.Unix(1773964800, 0).UTC()
	repo := &cleanupRepositoryStub{}
	cleaner := NewCleaner(repo, Config{
		BatchSize:     100,
		TaskRetention: 72 * time.Hour,
		Now: func() time.Time {
			return now
		},
	})

	if err := cleaner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if !repo.tasksCutoff.Equal(now.Add(-72 * time.Hour)) {
		t.Fatalf("tasks cutoff = %v, want %v", repo.tasksCutoff, now.Add(-72*time.Hour))
	}
	if !repo.payloadsCutoff.Equal(now) {
		t.Fatalf("payload cutoff = %v, want %v", repo.payloadsCutoff, now)
	}
	if repo.batchSize != 100 {
		t.Fatalf("batch size = %d, want %d", repo.batchSize, 100)
	}
}

type cleanupRepositoryStub struct {
	tasksCutoff    time.Time
	payloadsCutoff time.Time
	batchSize      int
}

func (s *cleanupRepositoryStub) DeleteExpiredTasksBatch(_ context.Context, cutoff time.Time, batchSize int) (int64, error) {
	s.tasksCutoff = cutoff
	s.batchSize = batchSize
	return 0, nil
}

func (s *cleanupRepositoryStub) DeleteExpiredPayloadsBatch(_ context.Context, cutoff time.Time, batchSize int) (int64, error) {
	s.payloadsCutoff = cutoff
	s.batchSize = batchSize
	return 0, nil
}
