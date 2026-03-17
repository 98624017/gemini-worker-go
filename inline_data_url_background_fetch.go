package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type inlineDataBackgroundFetchError struct {
	message string
}

func (e *inlineDataBackgroundFetchError) Error() string {
	if e == nil {
		return "background fetch error"
	}
	return e.message
}

type inlineDataBackgroundWaitTimeoutError struct {
	safeURL      string
	waitTimeout  time.Duration
	totalTimeout time.Duration
}

func (e *inlineDataBackgroundWaitTimeoutError) Error() string {
	if e == nil {
		return "background fetch wait timeout"
	}
	return fmt.Sprintf("inlineData 图片抓取超时（已转后台继续下载）(%s): wait=%v total=%v", e.safeURL, e.waitTimeout, e.totalTimeout)
}

type inlineDataBackgroundTask struct {
	startedAt time.Time
	done      chan struct{}
	doneOnce  sync.Once
	mime      string
	bytesData []byte
	err       error
}

type inlineDataBackgroundFetcher struct {
	totalTimeout time.Duration
	maxInflight  int

	mu    sync.Mutex
	tasks map[string]*inlineDataBackgroundTask
}

func newInlineDataBackgroundFetcher(totalTimeout time.Duration, maxInflight int) (*inlineDataBackgroundFetcher, error) {
	if totalTimeout <= 0 {
		return nil, errors.New("background fetch total timeout must be > 0")
	}
	if maxInflight <= 0 {
		return nil, errors.New("background fetch max inflight must be > 0")
	}
	return &inlineDataBackgroundFetcher{
		totalTimeout: totalTimeout,
		maxInflight:  maxInflight,
		tasks:        make(map[string]*inlineDataBackgroundTask),
	}, nil
}

func (f *inlineDataBackgroundFetcher) Fetch(
	url string,
	safeURL string,
	waitTimeout time.Duration,
	fetch func(ctx context.Context) (string, []byte, error),
	onSuccess func(mime string, bytesData []byte),
) (string, []byte, error) {
	if f == nil {
		return "", nil, &inlineDataBackgroundFetchError{message: "background fetcher is nil"}
	}
	if fetch == nil {
		return "", nil, &inlineDataBackgroundFetchError{message: "background fetch function is nil"}
	}

	task, err := f.getOrStartTask(url, fetch, onSuccess)
	if err != nil {
		return "", nil, err
	}

	if waitTimeout <= 0 {
		waitTimeout = f.totalTimeout
	}
	if waitTimeout <= 0 {
		<-task.done
		return task.mime, task.bytesData, task.err
	}

	timer := time.NewTimer(waitTimeout)
	defer timer.Stop()

	select {
	case <-task.done:
		return task.mime, task.bytesData, task.err
	case <-timer.C:
		return "", nil, &inlineDataBackgroundWaitTimeoutError{
			safeURL:      safeURL,
			waitTimeout:  waitTimeout,
			totalTimeout: f.totalTimeout,
		}
	}
}

func (f *inlineDataBackgroundFetcher) getOrStartTask(
	url string,
	fetch func(ctx context.Context) (string, []byte, error),
	onSuccess func(mime string, bytesData []byte),
) (*inlineDataBackgroundTask, error) {
	f.mu.Lock()
	now := time.Now()
	f.pruneExpiredCompletedTasksLocked(now)
	if task, ok := f.tasks[url]; ok {
		f.mu.Unlock()
		return task, nil
	}
	if f.inflightCountLocked() >= f.maxInflight {
		f.mu.Unlock()
		return nil, &inlineDataBackgroundFetchError{
			message: fmt.Sprintf("background fetch inflight limit reached: %d", f.maxInflight),
		}
	}

	task := &inlineDataBackgroundTask{
		startedAt: now,
		done:      make(chan struct{}),
	}
	f.tasks[url] = task
	f.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				task.err = fmt.Errorf("background fetch panic: %v", r)
			}
			task.markDone()
			f.scheduleTaskCleanup(url, task)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), f.totalTimeout)
		defer cancel()

		mime, bytesData, err := fetch(ctx)
		task.mime = mime
		task.bytesData = bytesData
		task.err = err

		if err == nil && onSuccess != nil {
			// 先释放等待者，再做 best-effort 落盘，避免慢磁盘把成功误判成超时。
			task.markDone()
			f.scheduleTaskCleanup(url, task)
			func() {
				defer func() {
					_ = recover()
				}()
				onSuccess(mime, bytesData)
			}()
		}
	}()

	return task, nil
}

func (t *inlineDataBackgroundTask) markDone() {
	if t == nil {
		return
	}
	t.doneOnce.Do(func() {
		close(t.done)
	})
}

func (t *inlineDataBackgroundTask) isDone() bool {
	if t == nil {
		return false
	}
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (f *inlineDataBackgroundFetcher) inflightCountLocked() int {
	count := 0
	for _, task := range f.tasks {
		if task == nil || task.isDone() {
			continue
		}
		count++
	}
	return count
}

func (f *inlineDataBackgroundFetcher) pruneExpiredCompletedTasksLocked(now time.Time) {
	if f == nil {
		return
	}
	for url, task := range f.tasks {
		if task == nil || !task.isDone() {
			continue
		}
		if now.Sub(task.startedAt) < f.totalTimeout {
			continue
		}
		delete(f.tasks, url)
	}
}

func (f *inlineDataBackgroundFetcher) scheduleTaskCleanup(url string, task *inlineDataBackgroundTask) {
	if f == nil || task == nil {
		return
	}

	delay := time.Until(task.startedAt.Add(f.totalTimeout))
	if delay < 0 {
		delay = 0
	}

	time.AfterFunc(delay, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if currentTask, ok := f.tasks[url]; ok && currentTask == task && task.isDone() {
			delete(f.tasks, url)
		}
	})
}
