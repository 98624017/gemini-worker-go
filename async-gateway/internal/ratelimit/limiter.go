package ratelimit

import (
	"math"
	"sync"
	"time"
)

const defaultRefillInterval = 3 * time.Second

type Config struct {
	RefillInterval time.Duration
	Burst          int
	Now            func() time.Time
}

type Limiter struct {
	mu             sync.Mutex
	refillInterval time.Duration
	burst          int
	now            func() time.Time
	buckets        map[string]*bucket
}

type bucket struct {
	tokens    float64
	updatedAt time.Time
}

func NewLimiter(cfg Config) *Limiter {
	burst := cfg.Burst
	if burst <= 0 {
		burst = 1
	}

	return &Limiter{
		refillInterval: durationOrDefault(cfg.RefillInterval, defaultRefillInterval),
		burst:          burst,
		now:            nowOrDefault(cfg.Now),
		buckets:        map[string]*bucket{},
	}
}

func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	item, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{
			tokens:    float64(l.burst - 1),
			updatedAt: now,
		}
		return true, 0
	}

	elapsed := now.Sub(item.updatedAt)
	if elapsed > 0 {
		item.tokens = math.Min(float64(l.burst), item.tokens+float64(elapsed)/float64(l.refillInterval))
		item.updatedAt = now
	}

	if item.tokens >= 1 {
		item.tokens--
		return true, 0
	}

	retryAfter := time.Duration(math.Ceil((1-item.tokens)*float64(l.refillInterval)/float64(time.Second))) * time.Second
	if retryAfter <= 0 {
		retryAfter = time.Second
	}
	return false, retryAfter
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
