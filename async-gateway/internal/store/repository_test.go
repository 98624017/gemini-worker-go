package store

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"banana-async-gateway/internal/domain"
)

func TestCreateAcceptedTask(t *testing.T) {
	t.Parallel()

	if strings.Contains(strings.ToLower(insertTaskSQL), "auth_ciphertext") {
		t.Fatalf("tasks insert must not contain auth_ciphertext")
	}
	if strings.Contains(strings.ToLower(insertTaskSQL), "request_body_json") {
		t.Fatalf("tasks insert must not contain request_body_json")
	}
	if !strings.Contains(insertTaskPayloadSQL, "request_body_json") {
		t.Fatalf("task payload insert must contain request_body_json")
	}

	repo, mock := newRepositoryForTest(t)
	task, payload := makeAcceptedFixture(t)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertTaskSQL)).
		WithArgs(task.ID, task.Status, task.Model, task.OwnerHash, task.RequestPath, task.RequestQuery, task.RequestProtocol).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(regexp.QuoteMeta(insertTaskPayloadSQL)).
		WithArgs(payload.TaskID, payload.RequestBodyJSON, payload.ForwardHeaders, payload.AuthorizationCrypt, payload.ExpiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	if err := repo.CreateAcceptedTask(context.Background(), task, payload); err != nil {
		t.Fatalf("CreateAcceptedTask() error = %v", err)
	}
}

func TestCreateAcceptedTaskIncludesRequestProtocol(t *testing.T) {
	t.Parallel()

	if !strings.Contains(strings.ToLower(insertTaskSQL), "request_protocol") {
		t.Fatalf("tasks insert must contain request_protocol")
	}

	repo, mock := newRepositoryForTest(t)
	task, payload := makeAcceptedFixture(t)
	task.RequestProtocol = domain.RequestProtocolOpenAIImageGeneration

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertTaskSQL)).
		WithArgs(
			task.ID,
			task.Status,
			task.Model,
			task.OwnerHash,
			task.RequestPath,
			task.RequestQuery,
			task.RequestProtocol,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(regexp.QuoteMeta(insertTaskPayloadSQL)).
		WithArgs(payload.TaskID, payload.RequestBodyJSON, payload.ForwardHeaders, payload.AuthorizationCrypt, payload.ExpiresAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	if err := repo.CreateAcceptedTask(context.Background(), task, payload); err != nil {
		t.Fatalf("CreateAcceptedTask() error = %v", err)
	}
}

func TestMarkQueued(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, markQueuedSQL, "updated_at = NOW()")

	repo, mock := newRepositoryForTest(t)
	mock.ExpectExec(regexp.QuoteMeta(markQueuedSQL)).
		WithArgs("task-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := repo.MarkQueued(context.Background(), "task-1"); err != nil {
		t.Fatalf("MarkQueued() error = %v", err)
	}
}

func TestMarkRunning(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, markRunningSQL, "updated_at = NOW()")

	repo, mock := newRepositoryForTest(t)
	heartbeatAt := time.Date(2026, 3, 19, 4, 5, 6, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(markRunningSQL)).
		WithArgs("task-1", "worker-1", heartbeatAt).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := repo.MarkRunning(context.Background(), "task-1", "worker-1", heartbeatAt); err != nil {
		t.Fatalf("MarkRunning() error = %v", err)
	}
}

func TestFinishSucceeded(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, finishSucceededSQL, "updated_at = NOW()")

	repo, mock := newRepositoryForTest(t)
	summary := &domain.ResultSummary{
		ImageURLs:    []string{"https://example.com/final.png"},
		FinishReason: "STOP",
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	mock.ExpectExec(regexp.QuoteMeta(finishSucceededSQL)).
		WithArgs("task-1", summaryJSON).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := repo.FinishSucceeded(context.Background(), "task-1", summary); err != nil {
		t.Fatalf("FinishSucceeded() error = %v", err)
	}
}

func TestFinishFailed(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, finishFailedSQL, "updated_at = NOW()")

	repo, mock := newRepositoryForTest(t)
	mock.ExpectExec(regexp.QuoteMeta(finishFailedSQL)).
		WithArgs("task-1", "upstream_error", "newapi failed").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := repo.FinishFailed(context.Background(), "task-1", "upstream_error", "newapi failed"); err != nil {
		t.Fatalf("FinishFailed() error = %v", err)
	}
}

func TestMarkUncertain(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, markUncertainSQL, "updated_at = NOW()")

	repo, mock := newRepositoryForTest(t)
	mock.ExpectExec(regexp.QuoteMeta(markUncertainSQL)).
		WithArgs("task-1", "upstream_transport_uncertain", "connection dropped after request dispatch").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := repo.MarkUncertain(context.Background(), "task-1", "upstream_transport_uncertain", "connection dropped after request dispatch"); err != nil {
		t.Fatalf("MarkUncertain() error = %v", err)
	}
}

func TestMarkDispatchedRunningUncertain(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, markDispatchedRunningUncertainSQL, "request_dispatched_at IS NOT NULL")

	repo, mock := newRepositoryForTest(t)
	mock.ExpectExec(regexp.QuoteMeta(markDispatchedRunningUncertainSQL)).
		WithArgs("gateway_shutdown_uncertain", "gateway shutdown grace period elapsed before task completion").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	rows, err := repo.MarkDispatchedRunningUncertain(context.Background(), "gateway_shutdown_uncertain", "gateway shutdown grace period elapsed before task completion")
	if err != nil {
		t.Fatalf("MarkDispatchedRunningUncertain() error = %v", err)
	}
	if rows != 2 {
		t.Fatalf("MarkDispatchedRunningUncertain() rows = %d, want %d", rows, 2)
	}
}

func TestGetTaskByID(t *testing.T) {
	t.Parallel()

	repo, mock := newRepositoryForTest(t)
	createdAt := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(5 * time.Second)
	finishedAt := createdAt.Add(30 * time.Second)
	summary := []byte(`{"image_urls":["https://example.com/ok.png"],"finish_reason":"STOP"}`)

	rows := mock.NewRows([]string{
		"task_id", "status", "model", "owner_hash", "request_path", "request_query",
		"request_protocol", "worker_id", "heartbeat_at", "request_dispatched_at", "result_summary_json",
		"error_code", "error_message", "transport_uncertain", "created_at", "updated_at", "finished_at",
	}).AddRow(
		"task-1", "succeeded", "gemini-3-pro-image-preview", "owner", "/v1beta/models/gemini-3-pro-image-preview:generateContent", "output=url", "gemini_generate_content",
		"worker-1", nil, createdAt, summary, "", "", false, createdAt, updatedAt, finishedAt,
	)

	mock.ExpectQuery(regexp.QuoteMeta(getTaskByIDSQL)).
		WithArgs("task-1").
		WillReturnRows(rows)

	task, err := repo.GetTaskByID(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTaskByID() error = %v", err)
	}
	if task == nil || task.ID != "task-1" || task.Status != domain.TaskStatusSucceeded {
		t.Fatalf("unexpected task = %#v", task)
	}
	if task.ResultSummary == nil || len(task.ResultSummary.ImageURLs) != 1 {
		t.Fatalf("expected parsed result summary, got %#v", task.ResultSummary)
	}
}

func TestGetTaskByIDLoadsRequestProtocol(t *testing.T) {
	t.Parallel()

	repo, mock := newRepositoryForTest(t)
	createdAt := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(5 * time.Second)

	rows := mock.NewRows([]string{
		"task_id", "status", "model", "owner_hash", "request_path", "request_query",
		"request_protocol", "worker_id", "heartbeat_at", "request_dispatched_at", "result_summary_json",
		"error_code", "error_message", "transport_uncertain", "created_at", "updated_at", "finished_at",
	}).AddRow(
		"task-1", "accepted", "gpt-image-1", "owner", "/v1/images/generations", "", "openai_image_generation",
		"", nil, nil, []byte(`{}`), "", "", false, createdAt, updatedAt, nil,
	)

	mock.ExpectQuery(regexp.QuoteMeta(getTaskByIDSQL)).
		WithArgs("task-1").
		WillReturnRows(rows)

	task, err := repo.GetTaskByID(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTaskByID() error = %v", err)
	}
	if task.RequestProtocol != domain.RequestProtocolOpenAIImageGeneration {
		t.Fatalf("task.RequestProtocol = %q, want %q", task.RequestProtocol, domain.RequestProtocolOpenAIImageGeneration)
	}
}

func TestGetTaskByIDDefaultsEmptyRequestProtocol(t *testing.T) {
	t.Parallel()

	repo, mock := newRepositoryForTest(t)
	createdAt := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(5 * time.Second)

	rows := mock.NewRows([]string{
		"task_id", "status", "model", "owner_hash", "request_path", "request_query",
		"request_protocol", "worker_id", "heartbeat_at", "request_dispatched_at", "result_summary_json",
		"error_code", "error_message", "transport_uncertain", "created_at", "updated_at", "finished_at",
	}).AddRow(
		"task-1", "accepted", "gemini-3-pro-image-preview", "owner", "/v1beta/models/gemini-3-pro-image-preview:generateContent", "output=url", "",
		"", nil, nil, []byte(`{}`), "", "", false, createdAt, updatedAt, nil,
	)

	mock.ExpectQuery(regexp.QuoteMeta(getTaskByIDSQL)).
		WithArgs("task-1").
		WillReturnRows(rows)

	task, err := repo.GetTaskByID(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTaskByID() error = %v", err)
	}
	if task.RequestProtocol != domain.RequestProtocolGeminiGenerateContent {
		t.Fatalf("task.RequestProtocol = %q, want %q", task.RequestProtocol, domain.RequestProtocolGeminiGenerateContent)
	}
}

func TestRepositoryGetTasksByIDs(t *testing.T) {
	t.Parallel()

	t.Run("empty ids returns empty map", func(t *testing.T) {
		t.Parallel()

		repo, _ := newRepositoryForTest(t)

		tasks, err := repo.GetTasksByIDs(context.Background(), nil)
		if err != nil {
			t.Fatalf("GetTasksByIDs() error = %v", err)
		}
		if len(tasks) != 0 {
			t.Fatalf("len(tasks) = %d, want 0", len(tasks))
		}
	})

	t.Run("returns existing tasks only", func(t *testing.T) {
		t.Parallel()

		repo, mock := newRepositoryForTest(t)
		createdAt := time.Date(2026, 3, 19, 10, 0, 0, 0, time.UTC)
		updatedAt := createdAt.Add(5 * time.Second)
		finishedAt := createdAt.Add(30 * time.Second)
		summary := []byte(`{"image_urls":["https://example.com/ok.png"],"finish_reason":"STOP"}`)

		rows := mock.NewRows([]string{
			"task_id", "status", "model", "owner_hash", "request_path", "request_query",
			"request_protocol", "worker_id", "heartbeat_at", "request_dispatched_at", "result_summary_json",
			"error_code", "error_message", "transport_uncertain", "created_at", "updated_at", "finished_at",
		}).AddRow(
			"task-1", "succeeded", "gemini-3-pro-image-preview", "owner", "/v1beta/models/gemini-3-pro-image-preview:generateContent", "output=url", "gemini_generate_content",
			"worker-1", nil, createdAt, summary, "", "", false, createdAt, updatedAt, finishedAt,
		)

		mock.ExpectQuery(regexp.QuoteMeta(getTasksByIDsSQL)).
			WithArgs([]string{"task-1", "task-2"}).
			WillReturnRows(rows)

		tasks, err := repo.GetTasksByIDs(context.Background(), []string{"task-1", "task-2"})
		if err != nil {
			t.Fatalf("GetTasksByIDs() error = %v", err)
		}
		if len(tasks) != 1 {
			t.Fatalf("len(tasks) = %d, want 1", len(tasks))
		}

		task, ok := tasks["task-1"]
		if !ok {
			t.Fatalf("task-1 not found in result map: %#v", tasks)
		}
		if task.Status != domain.TaskStatusSucceeded {
			t.Fatalf("task-1 status = %q, want %q", task.Status, domain.TaskStatusSucceeded)
		}
		if task.ResultSummary == nil || len(task.ResultSummary.ImageURLs) != 1 {
			t.Fatalf("expected parsed result summary, got %#v", task.ResultSummary)
		}
		if task.RequestProtocol != domain.RequestProtocolGeminiGenerateContent {
			t.Fatalf("task-1 request protocol = %q, want %q", task.RequestProtocol, domain.RequestProtocolGeminiGenerateContent)
		}
		if _, ok := tasks["task-2"]; ok {
			t.Fatalf("task-2 should be absent from result map: %#v", tasks)
		}
	})
}

func TestListTasksByOwner(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, listTasksByOwnerBeforeSQL, "created_at < $3")
	assertSQLContains(t, listTasksByOwnerBeforeSQL, "task_id < $4")

	repo, mock := newRepositoryForTest(t)
	since := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	beforeCreatedAt := time.Date(2026, 3, 19, 8, 0, 0, 0, time.UTC)
	rows := mock.NewRows([]string{
		"task_id", "status", "model", "result_summary_json", "error_code", "error_message", "created_at", "finished_at",
	}).AddRow(
		"task-1", "succeeded", "gemini-3-pro-image-preview", []byte(`{"image_urls":["https://example.com/1.png"]}`), "", "", beforeCreatedAt.Add(-1*time.Minute), beforeCreatedAt,
	)

	mock.ExpectQuery(regexp.QuoteMeta(listTasksByOwnerBeforeSQL)).
		WithArgs("owner", since, beforeCreatedAt, "task-9", 20).
		WillReturnRows(rows)

	items, err := repo.ListTasksByOwner(context.Background(), "owner", since, 20, &beforeCreatedAt, "task-9")
	if err != nil {
		t.Fatalf("ListTasksByOwner() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != "task-1" {
		t.Fatalf("unexpected items = %#v", items)
	}
}

func TestFindRecoverableTasks(t *testing.T) {
	t.Parallel()

	repo, mock := newRepositoryForTest(t)
	staleBefore := time.Date(2026, 3, 19, 9, 0, 0, 0, time.UTC)
	rows := mock.NewRows([]string{
		"task_id", "status", "model", "owner_hash", "request_path", "request_query",
		"request_protocol", "worker_id", "heartbeat_at", "request_dispatched_at", "result_summary_json",
		"error_code", "error_message", "transport_uncertain", "created_at", "updated_at", "finished_at", "has_payload",
	}).AddRow(
		"task-1", "accepted", "gemini-3-pro-image-preview", "owner", "/v1beta/models/gemini-3-pro-image-preview:generateContent", "output=url", "gemini_generate_content",
		"", nil, nil, []byte(`{}`), "", "", false, staleBefore.Add(-1*time.Hour), staleBefore.Add(-30*time.Minute), nil, true,
	)

	mock.ExpectQuery(regexp.QuoteMeta(findRecoverableTasksSQL)).
		WithArgs(staleBefore, 50).
		WillReturnRows(rows)

	items, err := repo.FindRecoverableTasks(context.Background(), staleBefore, 50)
	if err != nil {
		t.Fatalf("FindRecoverableTasks() error = %v", err)
	}
	if len(items) != 1 || !items[0].HasPayload {
		t.Fatalf("unexpected recoverable items = %#v", items)
	}
	if items[0].Task == nil {
		t.Fatalf("recoverable task is nil: %#v", items[0])
	}
	if items[0].Task.RequestProtocol != domain.RequestProtocolGeminiGenerateContent {
		t.Fatalf("recoverable task request protocol = %q, want %q", items[0].Task.RequestProtocol, domain.RequestProtocolGeminiGenerateContent)
	}
}

func TestDeleteExpiredTasksBatch(t *testing.T) {
	t.Parallel()

	repo, mock := newRepositoryForTest(t)
	cutoff := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(deleteExpiredTasksBatchSQL)).
		WithArgs(cutoff, 100).
		WillReturnResult(pgxmock.NewResult("DELETE", 2))

	deleted, err := repo.DeleteExpiredTasksBatch(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("DeleteExpiredTasksBatch() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("DeleteExpiredTasksBatch() = %d, want 2", deleted)
	}
}

func TestDeleteExpiredPayloadsBatch(t *testing.T) {
	t.Parallel()

	assertSQLContains(t, deleteExpiredPayloadsBatchSQL, "t.finished_at IS NOT NULL")
	assertSQLContains(t, deleteExpiredPayloadsBatchSQL, "t.request_dispatched_at IS NOT NULL")

	repo, mock := newRepositoryForTest(t)
	cutoff := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(deleteExpiredPayloadsBatchSQL)).
		WithArgs(cutoff, 100).
		WillReturnResult(pgxmock.NewResult("DELETE", 3))

	deleted, err := repo.DeleteExpiredPayloadsBatch(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("DeleteExpiredPayloadsBatch() error = %v", err)
	}
	if deleted != 3 {
		t.Fatalf("DeleteExpiredPayloadsBatch() = %d, want 3", deleted)
	}
}

func newRepositoryForTest(t *testing.T) (*Repository, pgxmock.PgxPoolIface) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool() error = %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet pgx expectations: %v", err)
		}
	})

	return NewRepository(mock), mock
}

func makeAcceptedFixture(t *testing.T) (*domain.Task, *domain.TaskPayload) {
	t.Helper()

	expiresAt := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	return &domain.Task{
			ID:              "task-1",
			Status:          domain.TaskStatusAccepted,
			Model:           "gemini-3-pro-image-preview",
			RequestProtocol: domain.RequestProtocolGeminiGenerateContent,
			OwnerHash:       "owner",
			RequestPath:     "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			RequestQuery:    "output=url",
		}, &domain.TaskPayload{
			TaskID:             "task-1",
			RequestBodyJSON:    []byte(`{"contents":[{"parts":[{"text":"draw cat"}]}]}`),
			ForwardHeaders:     map[string]string{"Content-Type": "application/json"},
			AuthorizationCrypt: []byte("ciphertext"),
			ExpiresAt:          expiresAt,
		}
}

func assertSQLContains(t *testing.T, sql, needle string) {
	t.Helper()
	if !strings.Contains(sql, needle) {
		t.Fatalf("SQL missing %q:\n%s", needle, sql)
	}
}
