package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestBatchGetTasksRejectsMissingIDs(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{}
	handler, _ := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{}`))
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.BatchGetTasks(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	errorBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error payload: %#v", body)
	}
	if errorBody["code"] != "invalid_request" {
		t.Fatalf("error.code = %v, want %q", errorBody["code"], "invalid_request")
	}
}

func TestBatchGetTasksRejectsTooManyIDs(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{}
	handler, _ := newQueryHandlerForTest(t, repo)

	ids := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		ids = append(ids, "task-over-limit")
	}
	bodyBytes, err := json.Marshal(map[string]any{"ids": ids})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.BatchGetTasks(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestBatchGetTasksRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{}
	handler, _ := newQueryHandlerForTest(t, repo)

	body := `{"ids":["` + strings.Repeat("x", 70*1024) + `"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(body))
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.BatchGetTasks(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestBatchGetTasksSuccessPreservesOrderAndHidesForeignTasks(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		batchTasks: map[string]*domain.Task{
			"task-b": {
				ID:         "task-b",
				Status:     domain.TaskStatusSucceeded,
				Model:      "gemini-3-pro-image-preview",
				OwnerHash:  mustOwnerHash(t),
				CreatedAt:  time.Unix(1773964800, 0).UTC(),
				FinishedAt: timePtr(time.Unix(1773964898, 0).UTC()),
				ResultSummary: &domain.ResultSummary{
					ImageURLs: []string{"https://example.com/final.png"},
				},
			},
			"task-a": {
				ID:        "task-a",
				Status:    domain.TaskStatusRunning,
				Model:     "gemini-3-pro-image-preview",
				OwnerHash: mustOwnerHash(t),
				CreatedAt: time.Unix(1773964801, 0).UTC(),
			},
			"task-x": {
				ID:        "task-x",
				Status:    domain.TaskStatusSucceeded,
				Model:     "gemini-3-pro-image-preview",
				OwnerHash: "someone-else",
				CreatedAt: time.Unix(1773964802, 0).UTC(),
				ResultSummary: &domain.ResultSummary{
					ImageURLs: []string{"https://example.com/hidden.png"},
				},
			},
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-b","task-a","task-b","task-x","task-missing"]}`))
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()

	handler.BatchGetTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(repo.batchIDs) != 5 {
		t.Fatalf("batch ids len = %d, want %d", len(repo.batchIDs), 5)
	}
	if repo.batchIDs[0] != "task-b" || repo.batchIDs[1] != "task-a" || repo.batchIDs[2] != "task-b" || repo.batchIDs[3] != "task-x" || repo.batchIDs[4] != "task-missing" {
		t.Fatalf("unexpected batch ids = %#v", repo.batchIDs)
	}

	var body struct {
		Object          string `json:"object"`
		NextPollAfterMS int    `json:"next_poll_after_ms"`
		Items           []struct {
			ID        string         `json:"id"`
			Object    string         `json:"object"`
			Model     string         `json:"model"`
			CreatedAt int64          `json:"created_at"`
			Status    string         `json:"status"`
			Error     map[string]any `json:"error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.Object != "batch.task.list" {
		t.Fatalf("object = %q, want %q", body.Object, "batch.task.list")
	}
	if body.NextPollAfterMS != 3000 {
		t.Fatalf("next_poll_after_ms = %d, want %d", body.NextPollAfterMS, 3000)
	}
	if len(body.Items) != 5 {
		t.Fatalf("items len = %d, want %d", len(body.Items), 5)
	}
	if body.Items[0].ID != "task-b" || body.Items[0].Object != "image.task" || body.Items[0].Model != "gemini-3-pro-image-preview" || body.Items[0].CreatedAt != 1773964800 || body.Items[0].Status != "succeeded" {
		t.Fatalf("unexpected first item = %#v", body.Items[0])
	}
	if body.Items[1].ID != "task-a" || body.Items[1].Object != "image.task" || body.Items[1].Model != "gemini-3-pro-image-preview" || body.Items[1].CreatedAt != 1773964801 || body.Items[1].Status != "running" {
		t.Fatalf("unexpected second item = %#v", body.Items[1])
	}
	if body.Items[2].ID != "task-b" || body.Items[2].Object != "image.task" || body.Items[2].Model != "gemini-3-pro-image-preview" || body.Items[2].CreatedAt != 1773964800 || body.Items[2].Status != "succeeded" {
		t.Fatalf("unexpected third item = %#v", body.Items[2])
	}
	if body.Items[3].ID != "task-x" || body.Items[3].Object != "image.task" || body.Items[3].Status != "not_found" {
		t.Fatalf("unexpected fourth item = %#v", body.Items[3])
	}
	if body.Items[3].Error["code"] != "not_found" {
		t.Fatalf("unexpected fourth item error = %#v", body.Items[3].Error)
	}
	if body.Items[4].ID != "task-missing" || body.Items[4].Object != "image.task" || body.Items[4].Status != "not_found" {
		t.Fatalf("unexpected fifth item = %#v", body.Items[4])
	}
	if body.Items[4].Error["code"] != "not_found" {
		t.Fatalf("unexpected fifth item error = %#v", body.Items[4].Error)
	}
}

func TestBatchGetTasksUsesCacheBeforeRepository(t *testing.T) {
	t.Parallel()

	now := time.Unix(1773964800, 0).UTC()
	cache := taskcache.NewTaskCache(taskcache.Config{
		Now: func() time.Time {
			return now
		},
	})
	cache.SetTask(&domain.Task{
		ID:        "task-cached",
		Status:    domain.TaskStatusSucceeded,
		Model:     "gemini-3-pro-image-preview",
		OwnerHash: mustOwnerHash(t),
		ResultSummary: &domain.ResultSummary{
			ImageURLs: []string{"https://example.com/cached.png"},
		},
	})
	repo := &queryRepositoryStub{
		batchTasks: map[string]*domain.Task{
			"task-db": {
				ID:        "task-db",
				Status:    domain.TaskStatusRunning,
				Model:     "gemini-3-pro-image-preview",
				OwnerHash: mustOwnerHash(t),
				CreatedAt: now,
			},
		},
	}
	handler := NewQueryHandler(
		config.Config{
			OwnerHashSecret:       "owner-secret",
			TaskPollRetryAfterSec: 3,
		},
		repo,
		cache,
		taskratelimit.NewLimiter(taskratelimit.Config{
			RefillInterval: 3 * time.Second,
			Burst:          1,
			Now: func() time.Time {
				return now
			},
		}),
	)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-cached","task-db"]}`))
	req1.Header.Set("Authorization", queryHandlerTestAuth)
	req1.Header.Set("Content-Type", "application/json")
	req1.RemoteAddr = "127.0.0.1:1234"
	rec1 := httptest.NewRecorder()

	handler.BatchGetTasks(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusOK)
	}
	if repo.batchCalls != 1 {
		t.Fatalf("batch calls = %d, want %d", repo.batchCalls, 1)
	}
	if len(repo.batchIDs) != 1 || repo.batchIDs[0] != "task-db" {
		t.Fatalf("unexpected batch ids = %#v", repo.batchIDs)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-cached","task-db"]}`))
	req2.Header.Set("Authorization", queryHandlerTestAuth)
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "127.0.0.2:1234"
	rec2 := httptest.NewRecorder()

	handler.BatchGetTasks(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusOK)
	}
	if repo.batchCalls != 1 {
		t.Fatalf("batch calls after cache fill = %d, want %d", repo.batchCalls, 1)
	}
}

func TestBatchGetTasksRateLimitedWhenPollingTooFast(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		batchTasks: map[string]*domain.Task{
			"task-1": {
				ID:        "task-1",
				Status:    domain.TaskStatusRunning,
				Model:     "gemini-3-pro-image-preview",
				OwnerHash: mustOwnerHash(t),
			},
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-1"]}`))
	req1.Header.Set("Authorization", queryHandlerTestAuth)
	req1.Header.Set("Content-Type", "application/json")
	req1.RemoteAddr = "127.0.0.1:1234"
	rec1 := httptest.NewRecorder()
	handler.BatchGetTasks(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-1"]}`))
	req2.Header.Set("Authorization", queryHandlerTestAuth)
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "127.0.0.1:1234"
	rec2 := httptest.NewRecorder()
	handler.BatchGetTasks(rec2, req2)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
	if rec2.Header().Get("Retry-After") != "3" {
		t.Fatalf("Retry-After = %q, want %q", rec2.Header().Get("Retry-After"), "3")
	}
}

func TestBatchGetTasksAllowsConfiguredBurstBeforeRateLimiting(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		batchTasks: map[string]*domain.Task{
			"task-1": {
				ID:        "task-1",
				Status:    domain.TaskStatusRunning,
				Model:     "gemini-3-pro-image-preview",
				OwnerHash: mustOwnerHash(t),
			},
		},
	}
	now := time.Unix(1773964800, 0).UTC()
	handler := NewQueryHandler(
		config.Config{
			OwnerHashSecret:       "owner-secret",
			TaskPollRetryAfterSec: 3,
			TaskPollBurst:         3,
		},
		repo,
		taskcache.NewTaskCache(taskcache.Config{
			Now: func() time.Time {
				return now
			},
		}),
		nil,
	)

	for attempt := 1; attempt <= 3; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-1"]}`))
		req.Header.Set("Authorization", queryHandlerTestAuth)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "127.0.0.1:1234"
		rec := httptest.NewRecorder()

		handler.BatchGetTasks(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status = %d, want %d", attempt, rec.Code, http.StatusOK)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-1"]}`))
	req.Header.Set("Authorization", queryHandlerTestAuth)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.BatchGetTasks(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	if rec.Header().Get("Retry-After") != "3" {
		t.Fatalf("Retry-After = %q, want %q", rec.Header().Get("Retry-After"), "3")
	}
}

func TestBatchGetTasksAllowsMultipleDistinctBatchesInSamePollWindow(t *testing.T) {
	t.Parallel()

	repo := &queryRepositoryStub{
		batchTasks: map[string]*domain.Task{
			"task-1": {
				ID:        "task-1",
				Status:    domain.TaskStatusRunning,
				Model:     "gemini-3-pro-image-preview",
				OwnerHash: mustOwnerHash(t),
			},
			"task-2": {
				ID:        "task-2",
				Status:    domain.TaskStatusRunning,
				Model:     "gemini-3-pro-image-preview",
				OwnerHash: mustOwnerHash(t),
			},
		},
	}
	handler, _ := newQueryHandlerForTest(t, repo)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-1"]}`))
	req1.Header.Set("Authorization", queryHandlerTestAuth)
	req1.Header.Set("Content-Type", "application/json")
	req1.RemoteAddr = "127.0.0.1:1234"
	rec1 := httptest.NewRecorder()
	handler.BatchGetTasks(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", rec1.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/tasks/batch-get", strings.NewReader(`{"ids":["task-2"]}`))
	req2.Header.Set("Authorization", queryHandlerTestAuth)
	req2.Header.Set("Content-Type", "application/json")
	req2.RemoteAddr = "127.0.0.1:1234"
	rec2 := httptest.NewRecorder()
	handler.BatchGetTasks(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", rec2.Code, http.StatusOK)
	}
}

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
	batchTasks          map[string]*domain.Task
	batchErr            error
	batchIDs            []string
	batchCalls          int
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

func (s *queryRepositoryStub) GetTasksByIDs(_ context.Context, ids []string) (map[string]*domain.Task, error) {
	s.batchCalls++
	s.batchIDs = append([]string(nil), ids...)
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	result := make(map[string]*domain.Task, len(ids))
	for _, id := range ids {
		if task, ok := s.batchTasks[id]; ok {
			result[id] = task
		}
	}
	return result, nil
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
