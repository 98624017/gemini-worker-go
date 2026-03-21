package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/queue"
	"banana-async-gateway/internal/security"
	"banana-async-gateway/internal/validation"
)

const defaultPayloadTTL = 6 * time.Hour

type submitRepository interface {
	CreateAcceptedTask(ctx context.Context, task *domain.Task, payload *domain.TaskPayload) error
	MarkQueued(ctx context.Context, taskID string) error
	FinishFailed(ctx context.Context, taskID, errorCode, errorMessage string) error
}

type submitQueueItem = queue.TaskItem

type submitQueue interface {
	TryEnqueue(item submitQueueItem) bool
}

type SubmitHandler struct {
	repo              submitRepository
	queue             submitQueue
	ownerHashSecret   string
	encryptionKey     []byte
	retryAfterSeconds int
	payloadTTL        time.Duration
	now               func() time.Time
	newTaskID         func(time.Time) string
}

func NewSubmitHandler(cfg config.Config, repo submitRepository, taskQueue submitQueue) (*SubmitHandler, error) {
	encryptionKey, err := security.ParseEncryptionKey(cfg.TaskPayloadEncryptionKey)
	if err != nil {
		return nil, err
	}

	return &SubmitHandler{
		repo:              repo,
		queue:             taskQueue,
		ownerHashSecret:   cfg.OwnerHashSecret,
		encryptionKey:     encryptionKey,
		retryAfterSeconds: cfg.TaskPollRetryAfterSec,
		payloadTTL:        defaultPayloadTTL,
		now:               time.Now,
		newTaskID:         newTaskID,
	}, nil
}

func (h *SubmitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	model := extractModelFromGenerateContentPath(r.URL.Path)
	validated, err := validation.ValidateGenerateContentRequest(r, model)
	if err != nil {
		h.writeValidationError(w, err)
		return
	}

	ownerHash, err := security.DeriveOwnerHash(h.ownerHashSecret, r.Header.Get("Authorization"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing_api_key", err.Error())
		return
	}

	authCiphertext, err := security.EncryptAuthorization(validated.AuthorizationHeader, h.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unknown_error", fmt.Sprintf("encrypt authorization: %v", err))
		return
	}

	now := h.now().UTC()
	taskID := h.newTaskID(now)
	task := &domain.Task{
		ID:           taskID,
		Status:       domain.TaskStatusAccepted,
		Model:        validated.Model,
		OwnerHash:    ownerHash,
		RequestPath:  r.URL.Path,
		RequestQuery: r.URL.RawQuery,
	}
	payload := &domain.TaskPayload{
		TaskID:             taskID,
		RequestBodyJSON:    validated.RequestBodyJSON,
		ForwardHeaders:     extractForwardHeaders(r.Header),
		AuthorizationCrypt: authCiphertext,
		ExpiresAt:          now.Add(h.payloadTTL),
	}

	if err := h.repo.CreateAcceptedTask(r.Context(), task, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed", fmt.Sprintf("persist accepted task: %v", err))
		return
	}

	if err := h.repo.MarkQueued(r.Context(), taskID); err != nil {
		writeError(w, http.StatusInternalServerError, "persist_failed", fmt.Sprintf("mark queued: %v", err))
		return
	}

	if !h.queue.TryEnqueue(submitQueueItem{TaskID: taskID}) {
		_ = h.repo.FinishFailed(r.Context(), taskID, "queue_full", "local task queue is full")
		w.Header().Set("Retry-After", fmt.Sprintf("%d", h.retryAfterSeconds))
		writeError(w, http.StatusServiceUnavailable, "queue_full", "local task queue is full")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":          taskID,
		"object":      "image.task",
		"model":       validated.Model,
		"created_at":  now.Unix(),
		"status":      string(domain.TaskStatusAccepted),
		"polling_url": "/v1/tasks/" + taskID,
		"content_url": "/v1/tasks/" + taskID + "/content",
	})
}

func (h *SubmitHandler) writeValidationError(w http.ResponseWriter, err error) {
	var requestErr *validation.RequestError
	if !errors.As(err, &requestErr) {
		writeError(w, http.StatusInternalServerError, "unknown_error", err.Error())
		return
	}

	code := requestErr.Code
	if requestErr.StatusCode == http.StatusUnauthorized {
		code = "missing_api_key"
	}
	if requestErr.StatusCode == http.StatusRequestEntityTooLarge {
		code = "request_too_large"
	}
	writeError(w, requestErr.StatusCode, code, requestErr.Message)
}

func extractForwardHeaders(header http.Header) map[string]string {
	result := map[string]string{}
	for _, key := range []string{
		"Content-Type",
		"Accept",
		"X-Request-ID",
		"X-Forwarded-For",
		"X-Real-IP",
		"X-Forwarded-Proto",
		"Forwarded",
	} {
		if value := strings.TrimSpace(strings.Join(header.Values(key), ", ")); value != "" {
			result[key] = value
		}
	}
	return result
}

func newTaskID(now time.Time) string {
	var buf [10]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("img_%d", now.UnixNano())
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:])
	return "img_" + strings.ToLower(encoded)
}
