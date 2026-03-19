package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	taskcache "banana-async-gateway/internal/cache"
	"banana-async-gateway/internal/config"
	"banana-async-gateway/internal/domain"
	taskratelimit "banana-async-gateway/internal/ratelimit"
	"banana-async-gateway/internal/security"
	"banana-async-gateway/internal/store"
)

const (
	defaultListDays  = 3
	maxListDays      = 3
	defaultListLimit = 20
	maxListLimit     = 100
)

type queryRepository interface {
	GetTaskByID(ctx context.Context, taskID string) (*domain.Task, error)
	ListTasksByOwner(ctx context.Context, ownerHash string, since time.Time, limit int, beforeCreatedAt *time.Time, beforeID string) ([]domain.TaskSummary, error)
}

type QueryHandler struct {
	repo              queryRepository
	cache             *taskcache.TaskCache
	limiter           *taskratelimit.Limiter
	ownerHashSecret   string
	retryAfterSeconds int
	now               func() time.Time
}

func NewQueryHandler(cfg config.Config, repo queryRepository, cache *taskcache.TaskCache, limiter *taskratelimit.Limiter) *QueryHandler {
	if cache == nil {
		cache = taskcache.NewTaskCache(taskcache.Config{})
	}
	if limiter == nil {
		limiter = taskratelimit.NewLimiter(taskratelimit.Config{
			RefillInterval: time.Duration(cfg.TaskPollRetryAfterSec) * time.Second,
			Burst:          1,
		})
	}

	return &QueryHandler{
		repo:              repo,
		cache:             cache,
		limiter:           limiter,
		ownerHashSecret:   cfg.OwnerHashSecret,
		retryAfterSeconds: cfg.TaskPollRetryAfterSec,
		now:               time.Now,
	}
}

func (h *QueryHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	ownerHash, ok := h.authorize(w, r)
	if !ok {
		return
	}

	taskID := extractTaskIDFromStatusPath(r.URL.Path)
	if taskID == "" {
		http.NotFound(w, r)
		return
	}
	if !h.allow(w, h.rateLimitKey("task:"+taskID, ownerHash, clientIP(r))) {
		return
	}

	task, found, err := h.loadTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unknown_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}
	if task.OwnerHash != ownerHash {
		writeError(w, http.StatusForbidden, "forbidden", "task does not belong to current api key")
		return
	}
	if isPendingStatus(task.Status) {
		w.Header().Set("Retry-After", strconv.Itoa(h.retryAfterSeconds))
	}
	writeJSON(w, http.StatusOK, buildTaskResponse(task))
}

func (h *QueryHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	ownerHash, ok := h.authorize(w, r)
	if !ok {
		return
	}
	if !h.allow(w, h.rateLimitKey("list", ownerHash, clientIP(r))) {
		return
	}

	days := clampDays(parsePositiveInt(r.URL.Query().Get("days"), defaultListDays))
	limit := clampLimit(parsePositiveInt(r.URL.Query().Get("limit"), defaultListLimit))
	beforeCreatedAt, beforeID, err := parseBeforeCursor(r.URL.Query().Get("before_created_at"), r.URL.Query().Get("before_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}

	cacheKey := taskcache.ListKey(ownerHash, days, limit, beforeCreatedAt, beforeID)
	if items, ok := h.cache.GetTaskList(cacheKey); ok {
		writeJSON(w, http.StatusOK, buildTaskListResponse(days, items))
		return
	}

	since := h.now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	items, err := h.repo.ListTasksByOwner(r.Context(), ownerHash, since, limit, beforeCreatedAt, beforeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unknown_error", err.Error())
		return
	}

	h.cache.SetTaskList(cacheKey, items)
	writeJSON(w, http.StatusOK, buildTaskListResponse(days, items))
}

func (h *QueryHandler) TaskContent(w http.ResponseWriter, r *http.Request) {
	ownerHash, ok := h.authorize(w, r)
	if !ok {
		return
	}

	taskID := extractTaskIDFromContentPath(r.URL.Path)
	if taskID == "" {
		http.NotFound(w, r)
		return
	}
	if !h.allow(w, h.rateLimitKey("content:"+taskID, ownerHash, clientIP(r))) {
		return
	}

	task, found, err := h.loadTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unknown_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}
	if task.OwnerHash != ownerHash {
		writeError(w, http.StatusForbidden, "forbidden", "task does not belong to current api key")
		return
	}
	if task.Status != domain.TaskStatusSucceeded || task.ResultSummary == nil || len(task.ResultSummary.ImageURLs) == 0 {
		writeError(w, http.StatusConflict, "task_not_ready", "task is not ready for content redirect")
		return
	}

	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, task.ResultSummary.ImageURLs[0], http.StatusFound)
}

func (h *QueryHandler) authorize(w http.ResponseWriter, r *http.Request) (string, bool) {
	ownerHash, err := security.DeriveOwnerHash(h.ownerHashSecret, r.Header.Get("Authorization"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing_api_key", err.Error())
		return "", false
	}
	return ownerHash, true
}

func (h *QueryHandler) allow(w http.ResponseWriter, key string) bool {
	allowed, retryAfter := h.limiter.Allow(key)
	if allowed {
		return true
	}

	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "polling too fast")
	return false
}

func (h *QueryHandler) loadTask(ctx context.Context, taskID string) (*domain.Task, bool, error) {
	if task, ok := h.cache.GetTask(taskID); ok {
		return task, true, nil
	}

	task, err := h.repo.GetTaskByID(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}

	h.cache.SetTask(task)
	return task, true, nil
}

func (h *QueryHandler) rateLimitKey(scope, ownerHash, ip string) string {
	return fmt.Sprintf("%s:%s:%s", scope, ownerHash, ip)
}

func buildTaskResponse(task *domain.Task) map[string]any {
	response := map[string]any{
		"id":         task.ID,
		"object":     "image.task",
		"model":      task.Model,
		"created_at": task.CreatedAt.Unix(),
		"status":     string(task.Status),
	}
	if task.FinishedAt != nil {
		response["finished_at"] = task.FinishedAt.Unix()
	}

	switch task.Status {
	case domain.TaskStatusSucceeded:
		if task.ResultSummary != nil {
			if task.ResultSummary.ResponseID != "" {
				response["response_id"] = task.ResultSummary.ResponseID
			}
			if task.ResultSummary.ModelVersion != "" {
				response["model_version"] = task.ResultSummary.ModelVersion
			}
			if len(task.ResultSummary.UsageMetadata) > 0 {
				response["usage_metadata"] = task.ResultSummary.UsageMetadata
			}
			response["candidates"] = []any{buildSuccessCandidate(task.ResultSummary)}
		}
	case domain.TaskStatusFailed, domain.TaskStatusUncertain:
		response["error"] = map[string]string{
			"code":    task.ErrorCode,
			"message": task.ErrorMessage,
		}
		if task.TransportUncertain {
			response["transport_uncertain"] = true
		}
	}

	return response
}

func buildSuccessCandidate(summary *domain.ResultSummary) map[string]any {
	parts := make([]map[string]any, 0, len(summary.ImageURLs)+1)
	if summary.TextSummary != "" {
		parts = append(parts, map[string]any{
			"text": summary.TextSummary,
		})
	}
	for _, imageURL := range summary.ImageURLs {
		parts = append(parts, map[string]any{
			"inlineData": map[string]string{
				"mimeType": "image/png",
				"data":     imageURL,
			},
		})
	}

	return map[string]any{
		"content": map[string]any{
			"parts": parts,
		},
		"finishReason": summary.FinishReason,
	}
}

func buildTaskListResponse(days int, items []domain.TaskSummary) map[string]any {
	responseItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{
			"id":         item.ID,
			"model":      item.Model,
			"status":     string(item.Status),
			"created_at": item.CreatedAt.Unix(),
		}
		if item.FinishedAt != nil {
			entry["finished_at"] = item.FinishedAt.Unix()
		}
		if item.Status == domain.TaskStatusSucceeded && item.ResultSummary != nil && len(item.ResultSummary.ImageURLs) > 0 {
			entry["content_url"] = "/v1/tasks/" + item.ID + "/content"
		}
		responseItems = append(responseItems, entry)
	}

	return map[string]any{
		"object": "list",
		"days":   days,
		"items":  responseItems,
	}
}

func parsePositiveInt(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func clampDays(value int) int {
	if value <= 0 {
		return defaultListDays
	}
	if value > maxListDays {
		return maxListDays
	}
	return value
}

func clampLimit(value int) int {
	if value <= 0 {
		return defaultListLimit
	}
	if value > maxListLimit {
		return maxListLimit
	}
	return value
}

func parseBeforeCursor(rawUnix, beforeID string) (*time.Time, string, error) {
	rawUnix = strings.TrimSpace(rawUnix)
	beforeID = strings.TrimSpace(beforeID)
	if rawUnix == "" && beforeID == "" {
		return nil, "", nil
	}
	if rawUnix == "" || beforeID == "" {
		return nil, "", fmt.Errorf("before_created_at and before_id must be provided together")
	}

	parsed, err := strconv.ParseInt(rawUnix, 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("before_created_at must be unix seconds")
	}
	value := time.Unix(parsed, 0).UTC()
	return &value, beforeID, nil
}

func isPendingStatus(status domain.TaskStatus) bool {
	return status == domain.TaskStatusAccepted || status == domain.TaskStatusQueued || status == domain.TaskStatusRunning
}

func extractTaskIDFromStatusPath(path string) string {
	if !isTaskStatusPath(path) {
		return ""
	}
	return strings.TrimPrefix(path, "/v1/tasks/")
}

func extractTaskIDFromContentPath(path string) string {
	if !isTaskContentPath(path) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(path, "/v1/tasks/"), "/content")
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
