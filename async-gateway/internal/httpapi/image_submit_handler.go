package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/security"
	"banana-async-gateway/internal/validation"
)

type ImageSubmitHandler struct {
	repo              submitRepository
	queue             submitQueue
	ownerHashSecret   string
	encryptionKey     []byte
	retryAfterSeconds int
	payloadTTL        time.Duration
	now               func() time.Time
	newTaskID         func(time.Time) string
}

func NewImageSubmitHandler(cfg config.Config, repo submitRepository, taskQueue submitQueue) (*ImageSubmitHandler, error) {
	encryptionKey, err := security.ParseEncryptionKey(cfg.TaskPayloadEncryptionKey)
	if err != nil {
		return nil, err
	}

	return &ImageSubmitHandler{
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

func (h *ImageSubmitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	validated, err := validation.ValidateImageGenerationRequest(r)
	if err != nil {
		writeSubmitValidationError(w, err)
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
		ID:              taskID,
		Status:          domain.TaskStatusAccepted,
		Model:           validated.Model,
		RequestProtocol: domain.RequestProtocolOpenAIImageGeneration,
		OwnerHash:       ownerHash,
		RequestPath:     r.URL.Path,
		RequestQuery:    r.URL.RawQuery,
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

func writeSubmitValidationError(w http.ResponseWriter, err error) {
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
