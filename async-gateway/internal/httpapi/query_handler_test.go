package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	taskcache "banana-async-gateway/internal/cache"
	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
	taskratelimit "banana-async-gateway/internal/ratelimit"
	"banana-async-gateway/internal/security"
	"banana-async-gateway/internal/store"
)

const queryHandlerTestAuth = "Bearer sk-query"

func TestGetTaskReturnsNotFound(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{getTaskErr: store.ErrTaskNotFound}
	handler, _ := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-404", nil)
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.GetTask(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestGetTaskReturnsForbiddenForOtherOwner(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		task: &domain.Task{
			ID:        "task-1",
			Status:    domain.TaskStatusSucceeded,
			Model:     "gemini-3-pro-image-preview",
			OwnerHash: "someone-else",
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1", nil)
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.GetTask(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestGetTaskRunningStatusesIncludeRetryAfter(t *testing.T) {
	t.Parallel()

	cases := []domain.TaskStatus{
		domain.TaskStatusAccepted,
		domain.TaskStatusQueued,
		domain.TaskStatusRunning,
	}

	for _, status := range cases {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			repo := &queryRepositoryStub{
				task: &domain.Task{
					ID:        "task-1",
					Status:    status,
					Model:     "gemini-3-pro-image-preview",
					OwnerHash: mustOwnerHash(t),
					CreatedAt: time.Unix(1773964800, 0).UTC(),
				},
			}
			handler, _ := newQueryHandlerForTest(t, repo)

			req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1", nil)
			req.Header.Set("Authorization", queryHandlerTestAuth)
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()

			handler.GetTask(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if rec.Header().Get("Retry-After") != "3" {
				t.Fatalf("Retry-After = %q, want %q", rec.Header().Get("Retry-After"), "3")
			}
		})
	}
}

func TestGetTaskSucceededReturnsCandidateSummary(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		task: &domain.Task{
			ID:         "task-1",
			Status:     domain.TaskStatusSucceeded,
			Model:      "gemini-3-pro-image-preview",
			OwnerHash:  mustOwnerHash(t),
			CreatedAt:  time.Unix(1773964800, 0).UTC(),
			FinishedAt: timePtr(time.Unix(1773964898, 0).UTC()),
			ResultSummary: &domain.ResultSummary{
				ImageURLs:    []string{"https://example.com/final.png"},
				FinishReason: "STOP",
				TextSummary:  "已根据提示生成图片",
			},
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1", nil)
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.GetTask(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	candidates, ok := body["candidates"].([]any)
	if !ok || len(candidates) != 1 {
		t.Fatalf("expected one candidate in response: %#v", body)
	}
}

func TestGetTaskFailedAndUncertainReturnErrorPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		task              *domain.Task
		wantTransportFlag bool
	}{
		{
			name: "failed",
			task: &domain.Task{
				ID:           "task-1",
				Status:       domain.TaskStatusFailed,
				Model:        "gemini-3-pro-image-preview",
				OwnerHash:    mustOwnerHash(t),
				ErrorCode:    "upstream_timeout",
				ErrorMessage: "newapi upstream request timed out",
			},
		},
		{
			name: "uncertain",
			task: &domain.Task{
				ID:                 "task-2",
				Status:             domain.TaskStatusUncertain,
				Model:              "gemini-3-pro-image-preview",
				OwnerHash:          mustOwnerHash(t),
				ErrorCode:          "upstream_transport_uncertain",
				ErrorMessage:       "connection to newapi broke after request dispatch; task result may be uncertain",
				TransportUncertain: true,
			},
			wantTransportFlag: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := &queryRepositoryStub{task: tc.task}
			handler, _ := newQueryHandlerForTest(t, repo)

			req := httptest.NewRequest(http.MethodGet, "/v1/tasks/"+tc.task.ID, nil)
			req.Header.Set("Authorization", queryHandlerTestAuth)
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()

			handler.GetTask(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if _, ok := body["error"].(map[string]any); !ok {
				t.Fatalf("expected error payload: %#v", body)
			}
			if got, _ := body["transport_uncertain"].(bool); got != tc.wantTransportFlag {
				t.Fatalf("transport_uncertain = %v, want %v", got, tc.wantTransportFlag)
			}
		})
	}
}

func TestListTasksDefaultsToThreeDaysAndSupportsKeysetPagination(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		list: []domain.TaskSummary{
			{
				ID:         "task-1",
				Status:     domain.TaskStatusSucceeded,
				Model:      "gemini-3-pro-image-preview",
				CreatedAt:  time.Unix(1773964800, 0).UTC(),
				FinishedAt: timePtr(time.Unix(1773964898, 0).UTC()),
				ResultSummary: &domain.ResultSummary{
					ImageURLs: []string{"https://example.com/final.png"},
				},
			},
		},
	}
	handler, currentTime := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks?limit=20&before_created_at=1773964700&before_id=task-0", nil)
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.ListTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if repo.listOwnerHash != mustOwnerHash(t) {
		t.Fatalf("owner hash mismatch: got=%q want=%q", repo.listOwnerHash, mustOwnerHash(t))
	}
	wantSince := currentTime.Add(-72 * time.Hour)
	if !repo.listSince.Equal(wantSince) {
		t.Fatalf("since = %v, want %v", repo.listSince, wantSince)
	}
	if repo.listBeforeID != "task-0" || repo.listBeforeCreatedAt == nil || repo.listBeforeCreatedAt.Unix() != 1773964700 {
		t.Fatalf("unexpected keyset args: before=%v before_id=%q", repo.listBeforeCreatedAt, repo.listBeforeID)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if int(body["days"].(float64)) != 3 {
		t.Fatalf("days = %v, want %d", body["days"], 3)
	}
}

func TestListTasksClampsDaysToThree(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{}
	handler, currentTime := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks?days=7", nil)
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.ListTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	wantSince := currentTime.Add(-72 * time.Hour)
	if !repo.listSince.Equal(wantSince) {
		t.Fatalf("since = %v, want %v", repo.listSince, wantSince)
	}
}

func TestTaskContentRedirectsToFirstImageURL(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		task: &domain.Task{
			ID:        "task-1",
			Status:    domain.TaskStatusSucceeded,
			Model:     "gemini-3-pro-image-preview",
			OwnerHash: mustOwnerHash(t),
			ResultSummary: &domain.ResultSummary{
				ImageURLs: []string{"https://example.com/final.png", "https://example.com/other.png"},
			},
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/content", nil)
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.TaskContent(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if rec.Header().Get("Location") != "https://example.com/final.png" {
		t.Fatalf("Location = %q, want %q", rec.Header().Get("Location"), "https://example.com/final.png")
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want %q", rec.Header().Get("Referrer-Policy"), "no-referrer")
	}
}

func TestTaskContentReturnsConflictForNonTerminalTask(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		task: &domain.Task{
			ID:        "task-1",
			Status:    domain.TaskStatusRunning,
			Model:     "gemini-3-pro-image-preview",
			OwnerHash: mustOwnerHash(t),
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1/content", nil)
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.TaskContent(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}

func TestGetTaskRateLimitedWhenPollingTooFast(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		task: &domain.Task{
			ID:        "task-1",
			Status:    domain.TaskStatusRunning,
			Model:     "gemini-3-pro-image-preview",
			OwnerHash: mustOwnerHash(t),
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req1 := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1", nil)
	req1.Header.Set("Authorization", queryHandlerTestAuth)
	req1.RemoteAddr = "127.0.0.1:1234"
	rec1 := httptest.NewRecorder()
	handler.GetTask(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1", nil)
	req2.Header.Set("Authorization", queryHandlerTestAuth)
	req2.RemoteAddr = "127.0.0.1:1234"
	rec2 := httptest.NewRecorder()
	handler.GetTask(rec2, req2)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
}

type queryRepositoryStub struct {
	task                *domain.Task
	getTaskErr          error
	list                []domain.TaskSummary
	listOwnerHash       string
	listSince           time.Time
	listLimit           int
	listBeforeCreatedAt *time.Time
	listBeforeID        string
}

func (s *queryRepositoryStub) GetTaskByID(context.Context, string) (*domain.Task, error) {
	if s.getTaskErr != nil {
		return nil, s.getTaskErr
	}
	return s.task, nil
}

func (s *queryRepositoryStub) ListTasksByOwner(_ context.Context, ownerHash string, since time.Time, limit int, beforeCreatedAt *time.Time, beforeID string) ([]domain.TaskSummary, error) {
	s.listOwnerHash = ownerHash
	s.listSince = since
	s.listLimit = limit
	s.listBeforeCreatedAt = beforeCreatedAt
	s.listBeforeID = beforeID
	return s.list, nil
}

func newQueryHandlerForTest(t *testing.T, repo *queryRepositoryStub) (*QueryHandler, time.Time) {
	t.Helper()

	now := time.Unix(1773964800, 0).UTC()
	handler := NewQueryHandler(
		config.Config{
			OwnerHashSecret:       "owner-secret",
			TaskPollRetryAfterSec: 3,
		},
		repo,
		taskcache.NewTaskCache(taskcache.Config{
			Now: func() time.Time {
				return now
			},
		}),
		taskratelimit.NewLimiter(taskratelimit.Config{
			RefillInterval: 3 * time.Second,
			Burst:          1,
			Now: func() time.Time {
				return now
			},
		}),
	)
	handler.now = func() time.Time {
		return now
	}
	return handler, now
}

func mustOwnerHash(t *testing.T) string {
	t.Helper()

	ownerHash, err := security.DeriveOwnerHash("owner-secret", queryHandlerTestAuth)
	if err != nil {
		t.Fatalf("DeriveOwnerHash() error = %v", err)
	}
	return ownerHash
}

func timePtr(value time.Time) *time.Time {
	return &value
}
