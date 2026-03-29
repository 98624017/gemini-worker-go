package main

import (
	"runtime"
	"testing"
	"time"
)

func TestLoadConfigWithEnv_ScalesDefaultInlineDataMemCacheForSmallContainer(t *testing.T) {
	cfg := loadConfigWithEnv(func(string) string { return "" }, int64(5)*512*1024*1024)

	const want = 64 * 1024 * 1024
	if cfg.InlineDataURLMemCacheMaxBytes != want {
		t.Fatalf("expected auto-scaled memory cache default=%d, got=%d", want, cfg.InlineDataURLMemCacheMaxBytes)
	}
}

func TestConfigureRuntimeMemory_AutoSetsRuntimeLimitAndGCPercent(t *testing.T) {
	var gotGCPercent int
	var gotMemoryLimit int64

	tuning := configureRuntimeMemory(
		func(string) string { return "" },
		int64(5)*512*1024*1024,
		func(percent int) int {
			gotGCPercent = percent
			return 100
		},
		func(limit int64) int64 {
			gotMemoryLimit = limit
			return 0
		},
	)

	if !tuning.AutoGCPercent {
		t.Fatal("expected auto GC percent to be enabled")
	}
	if gotGCPercent != defaultRuntimeGCPercent {
		t.Fatalf("expected GC percent=%d, got=%d", defaultRuntimeGCPercent, gotGCPercent)
	}

	wantLimit := autoRuntimeMemoryLimitBytes(int64(5) * 512 * 1024 * 1024)
	if !tuning.AutoMemoryLimit {
		t.Fatal("expected auto memory limit to be enabled")
	}
	if gotMemoryLimit != wantLimit {
		t.Fatalf("expected memory limit=%d, got=%d", wantLimit, gotMemoryLimit)
	}
}

func TestConfigureRuntimeMemory_RespectsExistingGOMEMLIMIT(t *testing.T) {
	called := false
	gotGCPercent := 0

	tuning := configureRuntimeMemory(
		func(key string) string {
			if key == "GOMEMLIMIT" {
				return "1700MiB"
			}
			return ""
		},
		int64(5)*512*1024*1024,
		func(percent int) int {
			gotGCPercent = percent
			return percent
		},
		func(limit int64) int64 {
			called = true
			return 0
		},
	)

	if tuning.AutoMemoryLimit {
		t.Fatal("expected auto memory limit to stay disabled when GOMEMLIMIT is already set")
	}
	if !tuning.AutoGCPercent {
		t.Fatal("expected auto GC percent to stay enabled when GOMEMLIMIT is already set")
	}
	if gotGCPercent != defaultRuntimeGCPercent {
		t.Fatalf("expected GC percent=%d with explicit GOMEMLIMIT, got=%d", defaultRuntimeGCPercent, gotGCPercent)
	}
	if called {
		t.Fatal("expected runtime memory limit setter not to be called when GOMEMLIMIT is already set")
	}
}

func TestConfigureRuntimeMemory_DoesNotLowerGCPercentWithoutMemoryBound(t *testing.T) {
	gotGCPercent := 0

	tuning := configureRuntimeMemory(
		func(string) string { return "" },
		0,
		func(percent int) int {
			gotGCPercent = percent
			return percent
		},
		func(limit int64) int64 { return limit },
	)

	if tuning.AutoGCPercent {
		t.Fatal("expected auto GC percent to stay disabled without memory bound")
	}
	if gotGCPercent != 0 {
		t.Fatalf("expected GC percent setter not to be called, got=%d", gotGCPercent)
	}
}

func TestMemoryReliefController_MaybeRelieveMemoryHonorsCooldown(t *testing.T) {
	now := time.Unix(100, 0)
	gcCalls := 0
	stats := runtime.MemStats{HeapAlloc: 90}

	controller := &memoryReliefController{
		now:                        func() time.Time { return now },
		readMemStats:               func(out *runtime.MemStats) { *out = stats },
		currentMemoryLimit:         func() int64 { return 100 },
		gc:                         func() { gcCalls++ },
		freeOSMemory:               func() { t.Fatal("unexpected FreeOSMemory call") },
		gcThresholdRatio:           85,
		freeOSMemoryThresholdRatio: 95,
		gcCooldown:                 5 * time.Second,
		freeOSMemoryCooldown:       30 * time.Second,
	}

	if action := controller.maybeRelieveMemory(); action != memoryReliefActionGC {
		t.Fatalf("expected first action=%v, got=%v", memoryReliefActionGC, action)
	}
	if gcCalls != 1 {
		t.Fatalf("expected 1 GC call, got=%d", gcCalls)
	}

	if action := controller.maybeRelieveMemory(); action != memoryReliefActionNone {
		t.Fatalf("expected cooldown to suppress GC, got action=%v", action)
	}
	if gcCalls != 1 {
		t.Fatalf("expected GC call count to stay at 1 during cooldown, got=%d", gcCalls)
	}

	now = now.Add(6 * time.Second)
	if action := controller.maybeRelieveMemory(); action != memoryReliefActionGC {
		t.Fatalf("expected GC after cooldown, got=%v", action)
	}
	if gcCalls != 2 {
		t.Fatalf("expected 2 GC calls after cooldown, got=%d", gcCalls)
	}
}
