package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterDispatchesImageSubmitPath(t *testing.T) {
	t.Parallel()

	imageSubmitCalled := false
	submitCalled := false
	router := NewRouter(nil, Handlers{
		SubmitTask: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			submitCalled = true
			w.WriteHeader(http.StatusAccepted)
		}),
		ImageSubmit: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			imageSubmitCalled = true
			w.WriteHeader(http.StatusAccepted)
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !imageSubmitCalled {
		t.Fatalf("expected ImageSubmit handler to be called")
	}
	if submitCalled {
		t.Fatalf("did not expect SubmitTask handler to be called")
	}
}

func TestRouterDispatchesGenerateContentPathToSubmitTask(t *testing.T) {
	t.Parallel()

	imageSubmitCalled := false
	submitCalled := false
	router := NewRouter(nil, Handlers{
		SubmitTask: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			submitCalled = true
			w.WriteHeader(http.StatusAccepted)
		}),
		ImageSubmit: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			imageSubmitCalled = true
			w.WriteHeader(http.StatusAccepted)
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/x:generateContent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !submitCalled {
		t.Fatalf("expected SubmitTask handler to be called")
	}
	if imageSubmitCalled {
		t.Fatalf("did not expect ImageSubmit handler to be called")
	}
}
