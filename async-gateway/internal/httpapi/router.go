package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

type Router struct {
	logger *log.Logger
}

func NewRouter(logger *log.Logger) http.Handler {
	return &Router{logger: logger}
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && isGenerateContentPath(r.URL.Path):
		rt.writeNotImplemented(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks":
		rt.writeNotImplemented(w, r)
	case r.Method == http.MethodGet && isTaskStatusPath(r.URL.Path):
		rt.writeNotImplemented(w, r)
	case r.Method == http.MethodGet && isTaskContentPath(r.URL.Path):
		rt.writeNotImplemented(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (rt *Router) writeNotImplemented(w http.ResponseWriter, r *http.Request) {
	if rt.logger != nil {
		rt.logger.Printf("route scaffold hit: %s %s", r.Method, r.URL.Path)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    "not_implemented",
			"message": "endpoint not implemented in task 1 scaffold",
		},
	})
}

func isGenerateContentPath(path string) bool {
	const prefix = "/v1beta/models/"
	const suffix = ":generateContent"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}

	model := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return model != "" && !strings.Contains(model, "/")
}

func isTaskStatusPath(path string) bool {
	const prefix = "/v1/tasks/"

	if !strings.HasPrefix(path, prefix) {
		return false
	}

	taskID := strings.TrimPrefix(path, prefix)
	return taskID != "" && !strings.Contains(taskID, "/")
}

func isTaskContentPath(path string) bool {
	const prefix = "/v1/tasks/"
	const suffix = "/content"

	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}

	taskID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return taskID != "" && !strings.Contains(taskID, "/")
}
