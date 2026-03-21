package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/security"
)

const submitTestKeyBase64 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestSubmitRejectsMissingAuthorization(t *testing.T) {
	t.Parallel()

	handler := newSubmitHandlerForTest(t, &submitRepositoryStub{}, &submitQueueStub{})
	req := newSubmitRequest(t, makeSubmitBody("draw cat"), "", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSubmitRejectsOutputNotURL(t *testing.T) {
	t.Parallel()

	body := makeSubmitBody("draw cat")
	body["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)["output"] = "base64"

	handler := newSubmitHandlerForTest(t, &submitRepositoryStub{}, &submitQueueStub{})
	req := newSubmitRequest(t, body, "Bearer sk-test", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSubmitRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	handler := newSubmitHandlerForTest(t, &submitRepositoryStub{}, &submitQueueStub{})
	req := newSubmitRequest(t, makeSubmitBody(strings.Repeat("a", 2*1024*1024+1)), "Bearer sk-test", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestSubmitAccepted(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{}
	queue := &submitQueueStub{allowEnqueue: true}
	handler := newSubmitHandlerForTest(t, repo, queue)

	req := newSubmitRequest(t, makeSubmitBody("draw cat"), "Bearer sk-live", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if payload["status"] != "accepted" {
		t.Fatalf("status payload = %#v", payload["status"])
	}
	if payload["id"] != "img_testtask123" {
		t.Fatalf("id = %#v", payload["id"])
	}
	if payload["polling_url"] != "/v1/tasks/img_testtask123" {
		t.Fatalf("polling_url = %#v", payload["polling_url"])
	}
	if payload["content_url"] != "/v1/tasks/img_testtask123/content" {
		t.Fatalf("content_url = %#v", payload["content_url"])
	}

	if repo.createdTask == nil || repo.createdTask.Status != domain.TaskStatusAccepted {
		t.Fatalf("expected accepted task persisted, got %#v", repo.createdTask)
	}
	if repo.createdPayload == nil || len(repo.createdPayload.AuthorizationCrypt) == 0 {
		t.Fatalf("expected encrypted auth payload, got %#v", repo.createdPayload)
	}
	if repo.markQueuedID != "img_testtask123" {
		t.Fatalf("MarkQueued id = %q", repo.markQueuedID)
	}
	if queue.enqueued.TaskID != "img_testtask123" {
		t.Fatalf("enqueued task id = %q", queue.enqueued.TaskID)
	}
}

func TestSubmitAcceptedPreservesTrustedForwardHeaders(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{}
	queue := &submitQueueStub{allowEnqueue: true}
	handler := newSubmitHandlerForTest(t, repo, queue)

	req := newSubmitRequest(t, makeSubmitBody("draw cat"), "Bearer sk-live", "")
	req.Header.Set("X-Forwarded-For", "203.0.113.10, 10.0.0.2")
	req.Header.Set("X-Real-IP", "203.0.113.10")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", `for=203.0.113.10;proto=https;host=async.example.com`)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if repo.createdPayload == nil {
		t.Fatalf("expected created payload")
	}

	got := repo.createdPayload.ForwardHeaders
	if got["X-Forwarded-For"] != "203.0.113.10, 10.0.0.2" {
		t.Fatalf("X-Forwarded-For = %q", got["X-Forwarded-For"])
	}
	if got["X-Real-IP"] != "203.0.113.10" {
		t.Fatalf("X-Real-IP = %q", got["X-Real-IP"])
	}
	if got["X-Forwarded-Proto"] != "https" {
		t.Fatalf("X-Forwarded-Proto = %q", got["X-Forwarded-Proto"])
	}
	if got["Forwarded"] != `for=203.0.113.10;proto=https;host=async.example.com` {
		t.Fatalf("Forwarded = %q", got["Forwarded"])
	}
}

func TestSubmitAcceptedPreservesRepeatedTrustedForwardHeaders(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{}
	queue := &submitQueueStub{allowEnqueue: true}
	handler := newSubmitHandlerForTest(t, repo, queue)

	req := newSubmitRequest(t, makeSubmitBody("draw cat"), "Bearer sk-live", "")
	req.Header.Add("X-Forwarded-For", "203.0.113.10")
	req.Header.Add("X-Forwarded-For", "10.0.0.2")
	req.Header.Add("Forwarded", `for=203.0.113.10;proto=https`)
	req.Header.Add("Forwarded", `for=10.0.0.2;by=proxy.internal`)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if repo.createdPayload == nil {
		t.Fatalf("expected created payload")
	}

	got := repo.createdPayload.ForwardHeaders
	if got["X-Forwarded-For"] != "203.0.113.10, 10.0.0.2" {
		t.Fatalf("X-Forwarded-For = %q", got["X-Forwarded-For"])
	}
	if got["Forwarded"] != `for=203.0.113.10;proto=https, for=10.0.0.2;by=proxy.internal` {
		t.Fatalf("Forwarded = %q", got["Forwarded"])
	}
}

func TestSubmitQueueFullReturns503AndMarksFailed(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{}
	queue := &submitQueueStub{allowEnqueue: false}
	handler := newSubmitHandlerForTest(t, repo, queue)

	req := newSubmitRequest(t, makeSubmitBody("draw cat"), "Bearer sk-live", "")
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

func TestSubmitCreateAcceptedTaskFailureReturns500(t *testing.T) {
	t.Parallel()

	repo := &submitRepositoryStub{createErr: errors.New("db down")}
	handler := newSubmitHandlerForTest(t, repo, &submitQueueStub{allowEnqueue: true})

	req := newSubmitRequest(t, makeSubmitBody("draw cat"), "Bearer sk-live", "")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func newSubmitHandlerForTest(t *testing.T, repo submitRepository, queue submitQueue) *SubmitHandler {
	t.Helper()

	cfg := config.Config{
		OwnerHashSecret:          "owner-secret",
		TaskPayloadEncryptionKey: submitTestKeyBase64,
		TaskPollRetryAfterSec:    3,
	}

	handler, err := NewSubmitHandler(cfg, repo, queue)
	if err != nil {
		t.Fatalf("NewSubmitHandler() error = %v", err)
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

func newSubmitRequest(t *testing.T, body map[string]any, authHeader, rawQuery string) *http.Request {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	target := "/v1beta/models/gemini-3-pro-image-preview:generateContent"
	if rawQuery != "" {
		target += "?" + rawQuery
	}

	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req
}

func makeSubmitBody(prompt string) map[string]any {
	return map[string]any{
		"contents": []any{
			map[string]any{
				"role": "user",
				"parts": []any{
					map[string]any{"text": prompt},
					map[string]any{
						"inlineData": map[string]any{
							"mimeType": "image/png",
							"data":     "https://example.com/source.png",
						},
					},
				},
			},
		},
		"generationConfig": map[string]any{
			"imageConfig": map[string]any{
				"output": "url",
			},
		},
	}
}

type submitRepositoryStub struct {
	createErr        error
	markQueuedErr    error
	createdTask      *domain.Task
	createdPayload   *domain.TaskPayload
	markQueuedID     string
	finishFailedID   string
	finishFailedCode string
}

func (s *submitRepositoryStub) CreateAcceptedTask(_ context.Context, task *domain.Task, payload *domain.TaskPayload) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.createdTask = task
	s.createdPayload = payload
	return nil
}

func (s *submitRepositoryStub) MarkQueued(_ context.Context, taskID string) error {
	if s.markQueuedErr != nil {
		return s.markQueuedErr
	}
	s.markQueuedID = taskID
	return nil
}

func (s *submitRepositoryStub) FinishFailed(_ context.Context, taskID, errorCode, _ string) error {
	s.finishFailedID = taskID
	s.finishFailedCode = errorCode
	return nil
}

type submitQueueStub struct {
	allowEnqueue bool
	enqueued     submitQueueItem
}

func (s *submitQueueStub) TryEnqueue(item submitQueueItem) bool {
	if !s.allowEnqueue {
		return false
	}
	s.enqueued = item
	return true
}

func TestSubmitFixtureKeyIsValid(t *testing.T) {
	if _, err := security.ParseEncryptionKey(submitTestKeyBase64); err != nil {
		t.Fatalf("submit test key must stay valid: %v", err)
	}
}
