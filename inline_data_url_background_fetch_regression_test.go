package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestFetchImageUrlAsInlineData_BackgroundBridge_DoesNotReuseCompletedResultWithoutDiskCache(t *testing.T) {
	rawURL := "https://example.com/slow-image.jpg"

	release := make(chan struct{})
	rt := &blockingTransport{
		contentType: "image/jpeg",
		body:        []byte("slow-image-bytes"),
		releaseCh:   release,
	}
	fetcher, err := newInlineDataBackgroundFetcher(500*time.Millisecond, 16)
	if err != nil {
		t.Fatalf("background fetcher init failed: %v", err)
	}

	app := &App{
		Config: Config{
			ImageFetchTimeout:                        40 * time.Millisecond,
			InlineDataURLBackgroundFetchWaitTimeout:  40 * time.Millisecond,
			InlineDataURLBackgroundFetchTotalTimeout: 500 * time.Millisecond,
		},
		ImageFetchClient:               &http.Client{Timeout: 40 * time.Millisecond, Transport: rt},
		ImageFetchBackgroundClient:     &http.Client{Timeout: 500 * time.Millisecond, Transport: rt},
		InlineDataURLBackgroundFetcher: fetcher,
	}

	_, _, _, err = app.fetchImageUrlAsInlineData(rawURL)
	if err == nil {
		t.Fatalf("expected first fetch to timeout waiting for background result, got nil")
	}
	var waitErr *inlineDataBackgroundWaitTimeoutError
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected background wait timeout error, got: %v", err)
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected exactly 1 upstream fetch started, got=%d", got)
	}

	close(release)
	time.Sleep(80 * time.Millisecond)

	_, _, fromCache, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("expected retry after background completion to succeed, got: %v", err)
	}
	if fromCache {
		t.Fatalf("expected retry without disk cache to refetch, got fromCache=true")
	}
	if got := rt.getCallCount(); got != 2 {
		t.Fatalf("expected completed background task to be removed and trigger a new fetch, got=%d", got)
	}
}

func TestInlineDataBackgroundFetcher_FetchReturnsBeforeOnSuccessCompletes(t *testing.T) {
	fetcher, err := newInlineDataBackgroundFetcher(500*time.Millisecond, 16)
	if err != nil {
		t.Fatalf("background fetcher init failed: %v", err)
	}

	onSuccessStarted := make(chan struct{})
	onSuccessRelease := make(chan struct{})
	type result struct {
		mime string
		data []byte
		err  error
	}
	done := make(chan result, 1)

	go func() {
		mime, data, err := fetcher.Fetch(
			"https://example.com/image.jpg",
			"https://example.com/image.jpg",
			40*time.Millisecond,
			func(ctx context.Context) (string, []byte, error) {
				return "image/jpeg", []byte("image-bytes"), nil
			},
			func(mime string, bytesData []byte) {
				close(onSuccessStarted)
				<-onSuccessRelease
			},
		)
		done <- result{mime: mime, data: data, err: err}
	}()

	select {
	case <-onSuccessStarted:
	case <-time.After(time.Second):
		t.Fatalf("onSuccess was not invoked in time")
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("expected fetch to succeed before onSuccess finished, got: %v", res.err)
		}
		if res.mime != "image/jpeg" || string(res.data) != "image-bytes" {
			t.Fatalf("unexpected fetch result: mime=%q data=%q", res.mime, string(res.data))
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("fetch did not return before onSuccess completed")
	}

	close(onSuccessRelease)
}
