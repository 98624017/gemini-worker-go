package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterRejectsBurstUntilTokenRefills(t *testing.T) {
	t.Parallel()

	now := time.Unix(1773964800, 0).UTC()
	limiter := NewLimiter(Config{
		RefillInterval: 3 * time.Second,
		Burst:          1,
		Now: func() time.Time {
			return now
		},
	})

	if allowed, retryAfter := limiter.Allow("task-1"); !allowed || retryAfter != 0 {
		t.Fatalf("first request should pass: allowed=%v retry_after=%v", allowed, retryAfter)
	}
	if allowed, retryAfter := limiter.Allow("task-1"); allowed || retryAfter <= 0 {
		t.Fatalf("second request should be limited: allowed=%v retry_after=%v", allowed, retryAfter)
	}

	now = now.Add(3 * time.Second)
	if allowed, retryAfter := limiter.Allow("task-1"); !allowed || retryAfter != 0 {
		t.Fatalf("request should pass after refill: allowed=%v retry_after=%v", allowed, retryAfter)
	}
}
