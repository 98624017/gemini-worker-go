package app

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDrainingSubmitHandlerRejectsNewSubmissions(t *testing.T) {
	t.Parallel()

	handler := NewDrainingSubmitHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}), 3)
	handler.StartDraining()

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image-preview:generateContent", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("Retry-After = %q, want %q", rec.Header().Get("Retry-After"), "3")
	}
}

func TestRunShutdownForcesUncertainWhenGracePeriodExpires(t *testing.T) {
	t.Parallel()

	handler := NewDrainingSubmitHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}), 3)
	workers := &lifecycleWorkersStub{}
	repo := &lifecycleRepositoryStub{}
	server := &lifecycleServerStub{}
	logger := log.New(io.Discard, "", 0)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	if err := runShutdown(ctx, logger, handler, workers, repo, server.Shutdown); err != nil {
		t.Fatalf("runShutdown() error = %v", err)
	}
	if !handler.draining.Load() {
		t.Fatalf("expected handler to enter draining mode")
	}
	if !workers.closeQueueCalled {
		t.Fatalf("expected queue to be closed")
	}
	if repo.forceCode != "gateway_shutdown_uncertain" {
		t.Fatalf("force code = %q, want %q", repo.forceCode, "gateway_shutdown_uncertain")
	}
	if !server.shutdownCalled {
		t.Fatalf("expected server shutdown")
	}
}

type lifecycleWorkersStub struct {
	closeQueueCalled bool
}

func (s *lifecycleWorkersStub) CloseQueue() {
	s.closeQueueCalled = true
}

func (s *lifecycleWorkersStub) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

type lifecycleRepositoryStub struct {
	forceCode string
}

func (s *lifecycleRepositoryStub) MarkDispatchedRunningUncertain(context.Context, string, string) (int64, error) {
	s.forceCode = "gateway_shutdown_uncertain"
	return 1, nil
}

type lifecycleServerStub struct {
	shutdownCalled bool
}

func (s *lifecycleServerStub) Shutdown(context.Context) error {
	s.shutdownCalled = true
	return nil
}
