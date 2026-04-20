package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
)

func TestImageSubmitAccepted(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{}
	queue := &submitQueueStub{allowEnqueue: true}
	handler := newImageSubmitHandlerForTest(t, repo, queue)

	req := newImageSubmitRequest(t, map[string]any{
		"model": "gpt-image-1",
		"image": []any{
			"https://example.com/reference.png",
		},
	}, "Bearer sk-live")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	if repo.createdTask == nil {
		t.Fatalf("expected accepted task persisted")
	}
	if repo.createdTask.RequestProtocol != domain.RequestProtocolOpenAIImageGeneration {
		t.Fatalf("RequestProtocol = %q, want %q", repo.createdTask.RequestProtocol, domain.RequestProtocolOpenAIImageGeneration)
	}
	if repo.createdTask.RequestPath != "/v1/images/generations" {
		t.Fatalf("RequestPath = %q, want %q", repo.createdTask.RequestPath, "/v1/images/generations")
	}
}

func TestImageSubmitQueueFullReturns503(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{}
	queue := &submitQueueStub{allowEnqueue: false}
	handler := newImageSubmitHandlerForTest(t, repo, queue)

	req := newImageSubmitRequest(t, map[string]any{
		"model": "gpt-image-1",
		"image": []any{
			"https://example.com/reference.png",
		},
	}, "Bearer sk-live")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Fatalf("Retry-After = %q, want %q", got, "3")
	}
	if repo.finishFailedID != "img_testtask123" || repo.finishFailedCode != "queue_full" {
		t.Fatalf("expected queue_full mark failed, got id=%q code=%q", repo.finishFailedID, repo.finishFailedCode)
	}
}

func TestImageSubmitValidationFailureReturns4xx(t *testing.T) {
	t.Parallel()

	handler := newImageSubmitHandlerForTest(t, &submitRepositoryStub{}, &submitQueueStub{allowEnqueue: true})
	req := newImageSubmitRequest(t, map[string]any{
		"model":           "gpt-image-1",
		"response_format": "b64_json",
	}, "Bearer sk-live")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestImageSubmitCreateAcceptedTaskFailureReturns500(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{createErr: errors.New("db down")}
	handler := newImageSubmitHandlerForTest(t, repo, &submitQueueStub{allowEnqueue: true})
	req := newImageSubmitRequest(t, map[string]any{
		"model": "gpt-image-1",
		"image": []any{
			"https://example.com/reference.png",
		},
	}, "Bearer sk-live")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestImageSubmitMarkQueuedFailureReturns500(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{markQueuedErr: errors.New("mark queued failed")}
	handler := newImageSubmitHandlerForTest(t, repo, &submitQueueStub{allowEnqueue: true})
	req := newImageSubmitRequest(t, map[string]any{
		"model": "gpt-image-1",
		"image": []any{
			"https://example.com/reference.png",
		},
	}, "Bearer sk-live")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func newImageSubmitHandlerForTest(t *testing.T, repo submitRepository, queue submitQueue) *ImageSubmitHandler {
	t.Helper()

	cfg := config.Config{
		OwnerHashSecret:          "owner-secret",
		TaskPayloadEncryptionKey: submitTestKeyBase64,
		TaskPollRetryAfterSec:    3,
	}

	handler, err := NewImageSubmitHandler(cfg, repo, queue)
	if err != nil {
		t.Fatalf("NewImageSubmitHandler() error = %v", err)
	}
	handler.now = func() time.Time {
		return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	}
	handler.newTaskID = func(time.Time) string {
		return "img_testtask123"
	}
	handler.payloadTTL = 6 * time.Hour
	return handler
}

func newImageSubmitRequest(t *testing.T, body map[string]any, authHeader string) *http.Request {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req
}
