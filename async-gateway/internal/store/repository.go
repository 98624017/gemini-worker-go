package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"banana-async-gateway/internal/domain"
)

var ErrTaskNotFound = errors.New("task not found")

type dbBeginner interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Repository struct {
	db dbBeginner
}

type RecoverableTask struct {
	Task       *domain.Task
	HasPayload bool
}

const (
	insertTaskSQL = `
INSERT INTO tasks (
	task_id,
	status,
	model,
	owner_hash,
	request_path,
	request_query
) VALUES ($1, $2, $3, $4, $5, $6)
`
	insertTaskPayloadSQL = `
INSERT INTO task_payloads (
	task_id,
	request_body_json,
	forward_headers_json,
	auth_ciphertext,
	payload_expires_at
) VALUES ($1, $2, $3, $4, $5)
`
	markQueuedSQL = `
UPDATE tasks
SET status = 'queued',
	updated_at = NOW()
WHERE task_id = $1
`
	markRunningSQL = `
UPDATE tasks
SET status = 'running',
	worker_id = $2,
	heartbeat_at = $3,
	updated_at = NOW()
WHERE task_id = $1
`
	markRequestDispatchedSQL = `
UPDATE tasks
SET request_dispatched_at = $2,
	updated_at = NOW()
WHERE task_id = $1
`
	updateHeartbeatSQL = `
UPDATE tasks
SET heartbeat_at = $2,
	updated_at = NOW()
WHERE task_id = $1
  AND status = 'running'
  AND finished_at IS NULL
`
	finishSucceededSQL = `
UPDATE tasks
SET status = 'succeeded',
	result_summary_json = $2,
	error_code = '',
	error_message = '',
	transport_uncertain = FALSE,
	finished_at = NOW(),
	updated_at = NOW()
WHERE task_id = $1
  AND finished_at IS NULL
`
	finishFailedSQL = `
UPDATE tasks
SET status = 'failed',
	error_code = $2,
	error_message = $3,
	transport_uncertain = FALSE,
	finished_at = NOW(),
	updated_at = NOW()
WHERE task_id = $1
  AND finished_at IS NULL
`
	markUncertainSQL = `
UPDATE tasks
SET status = 'uncertain',
	error_code = $2,
	error_message = $3,
	transport_uncertain = TRUE,
	finished_at = NOW(),
	updated_at = NOW()
WHERE task_id = $1
  AND finished_at IS NULL
`
	markDispatchedRunningUncertainSQL = `
UPDATE tasks
SET status = 'uncertain',
	error_code = $1,
	error_message = $2,
	transport_uncertain = TRUE,
	finished_at = NOW(),
	updated_at = NOW()
WHERE status = 'running'
  AND finished_at IS NULL
  AND request_dispatched_at IS NOT NULL
`
	getTaskByIDSQL = `
SELECT
	task_id,
	status,
	model,
	owner_hash,
	request_path,
	request_query,
	worker_id,
	heartbeat_at,
	request_dispatched_at,
	result_summary_json,
	error_code,
	error_message,
	transport_uncertain,
	created_at,
	updated_at,
	finished_at
FROM tasks
WHERE task_id = $1
`
	getTasksByIDsSQL = `
SELECT
	task_id,
	status,
	model,
	owner_hash,
	request_path,
	request_query,
	worker_id,
	heartbeat_at,
	request_dispatched_at,
	result_summary_json,
	error_code,
	error_message,
	transport_uncertain,
	created_at,
	updated_at,
	finished_at
FROM tasks
WHERE task_id = ANY($1)
`
	getTaskPayloadSQL = `
SELECT
	task_id,
	request_body_json,
	forward_headers_json,
	auth_ciphertext,
	payload_expires_at,
	created_at,
	updated_at
FROM task_payloads
WHERE task_id = $1
`
	listTasksByOwnerSQL = `
SELECT
	task_id,
	status,
	model,
	result_summary_json,
	error_code,
	error_message,
	created_at,
	finished_at
FROM tasks
WHERE owner_hash = $1
  AND created_at >= $2
ORDER BY created_at DESC, task_id DESC
LIMIT $3
`
	listTasksByOwnerBeforeSQL = `
SELECT
	task_id,
	status,
	model,
	result_summary_json,
	error_code,
	error_message,
	created_at,
	finished_at
FROM tasks
WHERE owner_hash = $1
  AND created_at >= $2
  AND (created_at < $3 OR (created_at = $3 AND task_id < $4))
ORDER BY created_at DESC, task_id DESC
LIMIT $5
`
	findRecoverableTasksSQL = `
SELECT
	t.task_id,
	t.status,
	t.model,
	t.owner_hash,
	t.request_path,
	t.request_query,
	t.worker_id,
	t.heartbeat_at,
	t.request_dispatched_at,
	t.result_summary_json,
	t.error_code,
	t.error_message,
	t.transport_uncertain,
	t.created_at,
	t.updated_at,
	t.finished_at,
	(tp.task_id IS NOT NULL) AS has_payload
FROM tasks t
LEFT JOIN task_payloads tp ON tp.task_id = t.task_id
WHERE
	t.status IN ('accepted', 'queued')
	OR (
		t.status = 'running'
		AND (
			t.heartbeat_at IS NULL
			OR t.heartbeat_at < $1
			OR t.request_dispatched_at IS NOT NULL
		)
	)
ORDER BY t.created_at ASC
LIMIT $2
`
	deleteExpiredTasksBatchSQL = `
WITH victims AS (
	SELECT task_id
	FROM tasks
	WHERE finished_at < $1
	  AND status IN ('succeeded', 'failed', 'uncertain')
	ORDER BY finished_at ASC
	LIMIT $2
	FOR UPDATE SKIP LOCKED
)
DELETE FROM tasks t
USING victims
WHERE t.task_id = victims.task_id
`
	deleteExpiredPayloadsBatchSQL = `
WITH victims AS (
	SELECT tp.task_id
	FROM task_payloads tp
	INNER JOIN tasks t ON t.task_id = tp.task_id
	WHERE tp.payload_expires_at < $1
	  AND (
		t.finished_at IS NOT NULL
		OR t.request_dispatched_at IS NOT NULL
	  )
	ORDER BY tp.payload_expires_at ASC
	LIMIT $2
	FOR UPDATE OF tp SKIP LOCKED
)
DELETE FROM task_payloads tp
USING victims
WHERE tp.task_id = victims.task_id
`
)

func NewRepository(db dbBeginner) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateAcceptedTask(ctx context.Context, task *domain.Task, payload *domain.TaskPayload) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create accepted task tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, insertTaskSQL,
		task.ID,
		task.Status,
		task.Model,
		task.OwnerHash,
		task.RequestPath,
		task.RequestQuery,
	); err != nil {
		return fmt.Errorf("insert tasks row: %w", err)
	}

	if _, err := tx.Exec(ctx, insertTaskPayloadSQL,
		payload.TaskID,
		payload.RequestBodyJSON,
		payload.ForwardHeaders,
		payload.AuthorizationCrypt,
		payload.ExpiresAt,
	); err != nil {
		return fmt.Errorf("insert task payload row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create accepted task tx: %w", err)
	}
	return nil
}

func (r *Repository) MarkQueued(ctx context.Context, taskID string) error {
	return execAffectingOne(ctx, r.db, markQueuedSQL, taskID)
}

func (r *Repository) MarkRunning(ctx context.Context, taskID, workerID string, heartbeatAt time.Time) error {
	return execAffectingOne(ctx, r.db, markRunningSQL, taskID, workerID, heartbeatAt)
}

func (r *Repository) MarkRequestDispatched(ctx context.Context, taskID string, dispatchedAt time.Time) error {
	return execAffectingOne(ctx, r.db, markRequestDispatchedSQL, taskID, dispatchedAt)
}

func (r *Repository) UpdateHeartbeat(ctx context.Context, taskID string, heartbeatAt time.Time) error {
	return execAffectingOne(ctx, r.db, updateHeartbeatSQL, taskID, heartbeatAt)
}

func (r *Repository) FinishSucceeded(ctx context.Context, taskID string, summary *domain.ResultSummary) error {
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal success summary: %w", err)
	}
	return execAffectingOne(ctx, r.db, finishSucceededSQL, taskID, summaryJSON)
}

func (r *Repository) FinishFailed(ctx context.Context, taskID, errorCode, errorMessage string) error {
	return execAffectingOne(ctx, r.db, finishFailedSQL, taskID, errorCode, errorMessage)
}

func (r *Repository) MarkUncertain(ctx context.Context, taskID, errorCode, errorMessage string) error {
	return execAffectingOne(ctx, r.db, markUncertainSQL, taskID, errorCode, errorMessage)
}

func (r *Repository) MarkDispatchedRunningUncertain(ctx context.Context, errorCode, errorMessage string) (int64, error) {
	tag, err := r.db.Exec(ctx, markDispatchedRunningUncertainSQL, errorCode, errorMessage)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) GetTaskByID(ctx context.Context, taskID string) (*domain.Task, error) {
	row := r.db.QueryRow(ctx, getTaskByIDSQL, taskID)
	task, err := scanTask(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("get task by id: %w", err)
	}
	return task, nil
}

func (r *Repository) GetTasksByIDs(ctx context.Context, ids []string) (map[string]*domain.Task, error) {
	if len(ids) == 0 {
		return map[string]*domain.Task{}, nil
	}

	rows, err := r.db.Query(ctx, getTasksByIDsSQL, ids)
	if err != nil {
		return nil, fmt.Errorf("get tasks by ids: %w", err)
	}
	defer rows.Close()

	tasks := make(map[string]*domain.Task, len(ids))
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan batch task: %w", err)
		}
		tasks[task.ID] = task
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("iterate batch tasks: %w", rows.Err())
	}

	return tasks, nil
}

func (r *Repository) GetTaskPayload(ctx context.Context, taskID string) (*domain.TaskPayload, error) {
	row := r.db.QueryRow(ctx, getTaskPayloadSQL, taskID)
	payload := &domain.TaskPayload{}
	var headersJSON []byte
	if err := row.Scan(
		&payload.TaskID,
		&payload.RequestBodyJSON,
		&headersJSON,
		&payload.AuthorizationCrypt,
		&payload.ExpiresAt,
		&payload.CreatedAt,
		&payload.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("get task payload: %w", err)
	}
	if len(headersJSON) > 0 {
		if err := json.Unmarshal(headersJSON, &payload.ForwardHeaders); err != nil {
			return nil, fmt.Errorf("unmarshal payload headers: %w", err)
		}
	}
	if payload.ForwardHeaders == nil {
		payload.ForwardHeaders = map[string]string{}
	}
	return payload, nil
}

func (r *Repository) ListTasksByOwner(ctx context.Context, ownerHash string, since time.Time, limit int, beforeCreatedAt *time.Time, beforeID string) ([]domain.TaskSummary, error) {
	query := listTasksByOwnerSQL
	args := []any{ownerHash, since, limit}
	if beforeCreatedAt != nil && beforeID != "" {
		query = listTasksByOwnerBeforeSQL
		args = []any{ownerHash, since, *beforeCreatedAt, beforeID, limit}
	}

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks by owner: %w", err)
	}
	defer rows.Close()

	var items []domain.TaskSummary
	for rows.Next() {
		var item domain.TaskSummary
		var summaryJSON []byte
		var finishedAt sql.NullTime
		if err := rows.Scan(
			&item.ID,
			&item.Status,
			&item.Model,
			&summaryJSON,
			&item.ErrorCode,
			&item.ErrorMessage,
			&item.CreatedAt,
			&finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan task summary: %w", err)
		}
		summary, err := parseResultSummary(summaryJSON)
		if err != nil {
			return nil, err
		}
		if finishedAt.Valid {
			item.FinishedAt = &finishedAt.Time
		}
		item.ResultSummary = summary
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("iterate task summaries: %w", rows.Err())
	}

	return items, nil
}

func (r *Repository) FindRecoverableTasks(ctx context.Context, staleBefore time.Time, limit int) ([]RecoverableTask, error) {
	rows, err := r.db.Query(ctx, findRecoverableTasksSQL, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("find recoverable tasks: %w", err)
	}
	defer rows.Close()

	var items []RecoverableTask
	for rows.Next() {
		task, hasPayload, err := scanRecoverableTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, RecoverableTask{Task: task, HasPayload: hasPayload})
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("iterate recoverable tasks: %w", rows.Err())
	}
	return items, nil
}

func (r *Repository) DeleteExpiredTasksBatch(ctx context.Context, finishedBefore time.Time, limit int) (int64, error) {
	tag, err := r.db.Exec(ctx, deleteExpiredTasksBatchSQL, finishedBefore, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired tasks: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) DeleteExpiredPayloadsBatch(ctx context.Context, expiresBefore time.Time, limit int) (int64, error) {
	tag, err := r.db.Exec(ctx, deleteExpiredPayloadsBatchSQL, expiresBefore, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired payloads: %w", err)
	}
	return tag.RowsAffected(), nil
}

func execAffectingOne(ctx context.Context, db dbBeginner, sql string, args ...any) error {
	tag, err := db.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaskNotFound
	}
	return nil
}

func scanTask(row interface {
	Scan(dest ...any) error
}) (*domain.Task, error) {
	task := &domain.Task{}
	var summaryJSON []byte
	var heartbeatAt sql.NullTime
	var requestDispatchedAt sql.NullTime
	var finishedAt sql.NullTime
	if err := row.Scan(
		&task.ID,
		&task.Status,
		&task.Model,
		&task.OwnerHash,
		&task.RequestPath,
		&task.RequestQuery,
		&task.WorkerID,
		&heartbeatAt,
		&requestDispatchedAt,
		&summaryJSON,
		&task.ErrorCode,
		&task.ErrorMessage,
		&task.TransportUncertain,
		&task.CreatedAt,
		&task.UpdatedAt,
		&finishedAt,
	); err != nil {
		return nil, err
	}
	summary, err := parseResultSummary(summaryJSON)
	if err != nil {
		return nil, err
	}
	if heartbeatAt.Valid {
		task.HeartbeatAt = &heartbeatAt.Time
	}
	if requestDispatchedAt.Valid {
		task.RequestDispatchedAt = &requestDispatchedAt.Time
	}
	if finishedAt.Valid {
		task.FinishedAt = &finishedAt.Time
	}
	task.ResultSummary = summary
	return task, nil
}

func scanRecoverableTask(row interface {
	Scan(dest ...any) error
}) (*domain.Task, bool, error) {
	task := &domain.Task{}
	var summaryJSON []byte
	var hasPayload bool
	var heartbeatAt sql.NullTime
	var requestDispatchedAt sql.NullTime
	var finishedAt sql.NullTime
	if err := row.Scan(
		&task.ID,
		&task.Status,
		&task.Model,
		&task.OwnerHash,
		&task.RequestPath,
		&task.RequestQuery,
		&task.WorkerID,
		&heartbeatAt,
		&requestDispatchedAt,
		&summaryJSON,
		&task.ErrorCode,
		&task.ErrorMessage,
		&task.TransportUncertain,
		&task.CreatedAt,
		&task.UpdatedAt,
		&finishedAt,
		&hasPayload,
	); err != nil {
		return nil, false, fmt.Errorf("scan recoverable task: %w", err)
	}
	summary, err := parseResultSummary(summaryJSON)
	if err != nil {
		return nil, false, err
	}
	if heartbeatAt.Valid {
		task.HeartbeatAt = &heartbeatAt.Time
	}
	if requestDispatchedAt.Valid {
		task.RequestDispatchedAt = &requestDispatchedAt.Time
	}
	if finishedAt.Valid {
		task.FinishedAt = &finishedAt.Time
	}
	task.ResultSummary = summary
	return task, hasPayload, nil
}

func parseResultSummary(summaryJSON []byte) (*domain.ResultSummary, error) {
	trimmed := strings.TrimSpace(string(summaryJSON))
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return nil, nil
	}

	var summary domain.ResultSummary
	if err := json.Unmarshal(summaryJSON, &summary); err != nil {
		return nil, fmt.Errorf("unmarshal result summary: %w", err)
	}
	return &summary, nil
}
