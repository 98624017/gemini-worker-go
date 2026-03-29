package main

import (
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRuntimeGCPercent                      = 80
	autoRuntimeMemoryLimitRatioNumerator         = 65
	autoRuntimeMemoryLimitRatioDenominator       = 100
	defaultInlineDataMemCacheMaxBytes            = 100 * 1024 * 1024
	minInlineDataMemCacheMaxBytes                = 16 * 1024 * 1024
	memoryReliefGCThresholdRatio                 = 85
	memoryReliefFreeOSMemoryThresholdRatio       = 94
	memoryReliefGCCooldown                       = 5 * time.Second
	memoryReliefFreeOSMemoryCooldown             = 30 * time.Second
	runtimeMemoryLimitUnlimitedThreshold   int64 = 1 << 60
)

type runtimeMemoryTuning struct {
	ContainerLimitBytes int64
	MemoryLimitBytes    int64
	GCPercent           int
	AutoMemoryLimit     bool
	AutoGCPercent       bool
}

type memoryReliefAction int

const (
	memoryReliefActionNone memoryReliefAction = iota
	memoryReliefActionGC
	memoryReliefActionFreeOSMemory
)

type memoryReliefController struct {
	mu sync.Mutex

	now                func() time.Time
	readMemStats       func(*runtime.MemStats)
	currentMemoryLimit func() int64
	gc                 func()
	freeOSMemory       func()

	gcThresholdRatio           int64
	freeOSMemoryThresholdRatio int64
	gcCooldown                 time.Duration
	freeOSMemoryCooldown       time.Duration

	lastGCAt           time.Time
	lastFreeOSMemoryAt time.Time
}

func newMemoryReliefController() *memoryReliefController {
	return &memoryReliefController{
		now:                time.Now,
		readMemStats:       runtime.ReadMemStats,
		currentMemoryLimit: func() int64 { return debug.SetMemoryLimit(-1) },
		gc:                 runtime.GC,
		freeOSMemory:       debug.FreeOSMemory,

		gcThresholdRatio:           memoryReliefGCThresholdRatio,
		freeOSMemoryThresholdRatio: memoryReliefFreeOSMemoryThresholdRatio,
		gcCooldown:                 memoryReliefGCCooldown,
		freeOSMemoryCooldown:       memoryReliefFreeOSMemoryCooldown,
	}
}

func (c *memoryReliefController) maybeRelieveMemory() memoryReliefAction {
	if c == nil || c.readMemStats == nil || c.currentMemoryLimit == nil || c.now == nil {
		return memoryReliefActionNone
	}

	limit := c.currentMemoryLimit()
	if limit <= 0 || limit >= runtimeMemoryLimitUnlimitedThreshold {
		return memoryReliefActionNone
	}

	var stats runtime.MemStats
	c.readMemStats(&stats)
	heapAlloc := int64(stats.HeapAlloc)
	if heapAlloc <= 0 {
		return memoryReliefActionNone
	}

	now := c.now()
	gcTrigger := limit * c.gcThresholdRatio / 100
	freeTrigger := limit * c.freeOSMemoryThresholdRatio / 100

	action := memoryReliefActionNone

	c.mu.Lock()
	switch {
	case heapAlloc >= freeTrigger && (c.lastFreeOSMemoryAt.IsZero() || now.Sub(c.lastFreeOSMemoryAt) >= c.freeOSMemoryCooldown):
		c.lastFreeOSMemoryAt = now
		c.lastGCAt = now
		action = memoryReliefActionFreeOSMemory
	case heapAlloc >= gcTrigger && (c.lastGCAt.IsZero() || now.Sub(c.lastGCAt) >= c.gcCooldown):
		c.lastGCAt = now
		action = memoryReliefActionGC
	}
	c.mu.Unlock()

	switch action {
	case memoryReliefActionFreeOSMemory:
		if c.freeOSMemory != nil {
			c.freeOSMemory()
		}
	case memoryReliefActionGC:
		if c.gc != nil {
			c.gc()
		}
	}

	return action
}

func (app *App) maybeRelieveMemory() {
	if app == nil || app.MemoryController == nil {
		return
	}
	app.MemoryController.maybeRelieveMemory()
}

func configureRuntimeMemory(
	getenv func(string) string,
	containerLimitBytes int64,
	setGCPercent func(int) int,
	setMemoryLimit func(int64) int64,
) runtimeMemoryTuning {
	if getenv == nil {
		getenv = os.Getenv
	}

	tuning := runtimeMemoryTuning{
		ContainerLimitBytes: containerLimitBytes,
	}

	hasRuntimeMemoryBound := containerLimitBytes > 0 || strings.TrimSpace(getenv("GOMEMLIMIT")) != ""

	if hasRuntimeMemoryBound && strings.TrimSpace(getenv("GOGC")) == "" && setGCPercent != nil {
		setGCPercent(defaultRuntimeGCPercent)
		tuning.AutoGCPercent = true
		tuning.GCPercent = defaultRuntimeGCPercent
	}

	if strings.TrimSpace(getenv("GOMEMLIMIT")) == "" && setMemoryLimit != nil {
		if limit := autoRuntimeMemoryLimitBytes(containerLimitBytes); limit > 0 {
			setMemoryLimit(limit)
			tuning.AutoMemoryLimit = true
			tuning.MemoryLimitBytes = limit
		}
	}

	return tuning
}

func autoRuntimeMemoryLimitBytes(containerLimitBytes int64) int64 {
	if containerLimitBytes <= 0 {
		return 0
	}
	return containerLimitBytes * autoRuntimeMemoryLimitRatioNumerator / autoRuntimeMemoryLimitRatioDenominator
}

func autoInlineDataMemCacheMaxBytes(containerLimitBytes int64) int64 {
	if containerLimitBytes <= 0 {
		return defaultInlineDataMemCacheMaxBytes
	}

	limit := containerLimitBytes / 40
	if limit < minInlineDataMemCacheMaxBytes {
		return minInlineDataMemCacheMaxBytes
	}
	if limit > defaultInlineDataMemCacheMaxBytes {
		return defaultInlineDataMemCacheMaxBytes
	}
	return limit
}

func detectContainerMemoryLimitBytes() int64 {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		if limit := readPositiveMemoryLimitFile(path); limit > 0 {
			return limit
		}
	}
	return 0
}

func readPositiveMemoryLimitFile(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	value := strings.TrimSpace(string(raw))
	if value == "" || value == "max" {
		return 0
	}

	limit, err := strconv.ParseInt(value, 10, 64)
	if err != nil || limit <= 0 || limit >= runtimeMemoryLimitUnlimitedThreshold {
		return 0
	}

	return limit
}
