package httpapi

import (
	"log"
	"net/http"
	"strings"
)

type Router struct {
	logger          *log.Logger
	submitHandler   http.Handler
	batchGetHandler http.Handler
	getTaskHandler  http.Handler
	listHandler     http.Handler
	contentHandler  http.Handler
}

type Handlers struct {
	SubmitTask    http.Handler
	BatchGetTasks http.Handler
	GetTask       http.Handler
	ListTasks     http.Handler
	TaskContent   http.Handler
}

func NewRouter(logger *log.Logger, handlers Handlers) http.Handler {
	return &Router{
		logger:          logger,
		submitHandler:   handlers.SubmitTask,
		batchGetHandler: handlers.BatchGetTasks,
		getTaskHandler:  handlers.GetTask,
		listHandler:     handlers.ListTasks,
		contentHandler:  handlers.TaskContent,
	}
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && isGenerateContentPath(r.URL.Path):
		rt.dispatchOrNotImplemented(rt.submitHandler, w, r)
	case r.Method == http.MethodPost && isTaskBatchGetPath(r.URL.Path):
		rt.dispatchOrNotImplemented(rt.batchGetHandler, w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks":
		rt.dispatchOrNotImplemented(rt.listHandler, w, r)
	case r.Method == http.MethodGet && isTaskStatusPath(r.URL.Path):
		rt.dispatchOrNotImplemented(rt.getTaskHandler, w, r)
	case r.Method == http.MethodGet && isTaskContentPath(r.URL.Path):
		rt.dispatchOrNotImplemented(rt.contentHandler, w, r)
	default:
		http.NotFound(w, r)
	}
}

func (rt *Router) dispatchOrNotImplemented(handler http.Handler, w http.ResponseWriter, r *http.Request) {
	if handler != nil {
		handler.ServeHTTP(w, r)
		return
	}
	rt.writeNotImplemented(w, r)
}

func (rt *Router) writeNotImplemented(w http.ResponseWriter, r *http.Request) {
	if rt.logger != nil {
		rt.logger.Printf("route scaffold hit: %s %s", r.Method, r.URL.Path)
	}
	writeError(w, http.StatusNotImplemented, "not_implemented", "endpoint not implemented in current task")
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

func isTaskBatchGetPath(path string) bool {
	return path == "/v1/tasks/batch-get"
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
