package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"sort"
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
	defaultListDays      = 3
	maxListDays          = 3
	defaultListLimit     = 20
	maxListLimit         = 100
	maxBatchGetTaskIDs   = 100
	maxBatchGetBodyBytes = 64 * 1024
)

var errBatchGetBodyTooLarge = errors.New("batch get body too large")

type queryRepository interface {
	GetTaskByID(ctx context.Context, taskID string) (*domain.Task, error)
	GetTasksByIDs(ctx context.Context, ids []string) (map[string]*domain.Task, error)
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

func (h *QueryHandler) BatchGetTasks(w http.ResponseWriter, r *http.Request) {
	ownerHash, ok := h.authorize(w, r)
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBatchGetBodyBytes)
	ids, err := parseBatchGetTaskIDs(r)
	if err != nil {
		if errors.Is(err, errBatchGetBodyTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !h.allow(w, h.rateLimitKey(batchRateLimitScope(ids), ownerHash, clientIP(r))) {
		return
	}

	tasksByID, err := h.loadTasksByIDs(r.Context(), ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unknown_error", err.Error())
		return
	}

	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		task, found := tasksByID[id]
		if !found || task.OwnerHash != ownerHash {
			items = append(items, buildBatchTaskNotFoundResponse(id))
			continue
		}
		items = append(items, buildTaskResponse(task))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object":             "batch.task.list",
		"items":              items,
		"next_poll_after_ms": h.retryAfterSeconds * 1000,
	})
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

func (h *QueryHandler) loadTasksByIDs(ctx context.Context, ids []string) (map[string]*domain.Task, error) {
	tasksByID := make(map[string]*domain.Task, len(ids))
	missingIDs := make([]string, 0, len(ids))

	for _, id := range ids {
		if task, ok := h.cache.GetTask(id); ok {
			tasksByID[id] = task
			continue
		}
		missingIDs = append(missingIDs, id)
	}

	if len(missingIDs) == 0 {
		return tasksByID, nil
	}

	loadedTasks, err := h.repo.GetTasksByIDs(ctx, missingIDs)
	if err != nil {
		return nil, err
	}
	for id, task := range loadedTasks {
		h.cache.SetTask(task)
		tasksByID[id] = task
	}
	return tasksByID, nil
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

func buildBatchTaskNotFoundResponse(taskID string) map[string]any {
	return map[string]any{
		"id":     taskID,
		"object": "image.task",
		"status": "not_found",
		"error": map[string]string{
			"code":    "not_found",
			"message": "task not found",
		},
	}
}

func batchRateLimitScope(ids []string) string {
	normalized := append([]string(nil), ids...)
	sort.Strings(normalized)

	hasher := fnv.New64a()
	for _, id := range normalized {
		_, _ = hasher.Write([]byte(id))
		_, _ = hasher.Write([]byte{0})
	}
	return fmt.Sprintf("batch:%x", hasher.Sum64())
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

func parseBatchGetTaskIDs(r *http.Request) ([]string, error) {
	var request struct {
		IDs []string `json:"ids"`
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return nil, errBatchGetBodyTooLarge
		}
		return nil, fmt.Errorf("request body must be a valid json object")
	}

	if len(request.IDs) == 0 {
		return nil, fmt.Errorf("ids must be a non-empty array")
	}
	if len(request.IDs) > maxBatchGetTaskIDs {
		return nil, fmt.Errorf("ids must not contain more than %d items", maxBatchGetTaskIDs)
	}

	uniqueIDs := make([]string, 0, len(request.IDs))
	seen := make(map[string]struct{}, len(request.IDs))
	for _, rawID := range request.IDs {
		taskID := strings.TrimSpace(rawID)
		if taskID == "" {
			return nil, fmt.Errorf("ids must not contain empty values")
		}
		if _, exists := seen[taskID]; exists {
			continue
		}
		seen[taskID] = struct{}{}
		uniqueIDs = append(uniqueIDs, taskID)
	}

	return uniqueIDs, nil
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
