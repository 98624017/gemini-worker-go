package metrics

import (
	"expvar"
	"sync"
)

type Registry struct {
	SubmitSucceeded       *expvar.Int
	SubmitFailed          *expvar.Int
	QueryRequests         *expvar.Int
	QueryRateLimited      *expvar.Int
	QueueDepth            *expvar.Int
	WorkerBusy            *expvar.Int
	TaskSucceeded         *expvar.Int
	TaskFailed            *expvar.Int
	TaskUncertain         *expvar.Int
	RecoveryRequeued      *expvar.Int
	RecoveryMarkedUnknown *expvar.Int
}

var (
	defaultRegistry *Registry
	defaultOnce     sync.Once
)

func Default() *Registry {
	defaultOnce.Do(func() {
		registry := &Registry{
			SubmitSucceeded:       newInt("banana_async_submit_succeeded"),
			SubmitFailed:          newInt("banana_async_submit_failed"),
			QueryRequests:         newInt("banana_async_query_requests"),
			QueryRateLimited:      newInt("banana_async_query_ratelimited"),
			QueueDepth:            newInt("banana_async_queue_depth"),
			WorkerBusy:            newInt("banana_async_worker_busy"),
			TaskSucceeded:         newInt("banana_async_task_succeeded"),
			TaskFailed:            newInt("banana_async_task_failed"),
			TaskUncertain:         newInt("banana_async_task_uncertain"),
			RecoveryRequeued:      newInt("banana_async_recovery_requeued"),
			RecoveryMarkedUnknown: newInt("banana_async_recovery_marked_uncertain"),
		}
		defaultRegistry = registry
	})
	return defaultRegistry
}

func newInt(name string) *expvar.Int {
	value := &expvar.Int{}
	expvar.Publish(name, value)
	return value
}
