package smoketest

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRunSuccess(t *testing.T) {
	t.Parallel()

	var (
		submitCalls  atomic.Int32
		statusCalls  atomic.Int32
		listCalls    atomic.Int32
		contentCalls atomic.Int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1beta/models/"):
			submitCalls.Add(1)
			if r.URL.RawQuery != "output=url" {
				t.Fatalf("submit query = %q, want output=url", r.URL.RawQuery)
			}
			if r.Header.Get("Content-Encoding") != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", r.Header.Get("Content-Encoding"))
			}
			gr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Fatalf("gzip.NewReader() error = %v", err)
			}
			defer gr.Close()
			bodyBytes, err := io.ReadAll(gr)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if !strings.Contains(string(bodyBytes), "banana") {
				t.Fatalf("unexpected submit body = %s", string(bodyBytes))
			}
			writeJSONForTest(w, http.StatusAccepted, map[string]any{
				"id":     "task-1",
				"status": "accepted",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1":
			call := statusCalls.Add(1)
			if call == 1 {
				w.Header().Set("Retry-After", "1")
				writeJSONForTest(w, http.StatusOK, map[string]any{
					"id":     "task-1",
					"status": "running",
				})
				return
			}
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"id":     "task-1",
				"status": "succeeded",
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{
								map[string]any{"text": "ok"},
								map[string]any{
									"inlineData": map[string]any{
										"mimeType": "image/png",
										"data":     "https://example.com/final.png",
									},
								},
							},
						},
						"finishReason": "STOP",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks":
			listCalls.Add(1)
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"object": "list",
				"days":   3,
				"items": []any{
					map[string]any{
						"id":          "task-1",
						"status":      "succeeded",
						"content_url": "/v1/tasks/task-1/content",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1/content":
			contentCalls.Add(1)
			w.Header().Set("Location", "https://example.com/final.png")
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		GatewayBaseURL: server.URL,
		APIKey:         "sk-test",
		Model:          "gemini-3-pro-image-preview",
		Prompt:         "draw banana",
		PollInterval:   10 * time.Millisecond,
		Timeout:        5 * time.Second,
		HTTPClient:     server.Client(),
	})

	result, err := client.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.TaskID != "task-1" || result.ImageURL != "https://example.com/final.png" {
		t.Fatalf("unexpected result = %#v", result)
	}
	if submitCalls.Load() != 1 || statusCalls.Load() < 2 || listCalls.Load() != 1 || contentCalls.Load() != 1 {
		t.Fatalf("unexpected call counts submit=%d status=%d list=%d content=%d", submitCalls.Load(), statusCalls.Load(), listCalls.Load(), contentCalls.Load())
	}
}

func TestClientRunUsesCustomRequestBody(t *testing.T) {
	t.Parallel()

	const customBody = `{"contents":[{"role":"user","parts":[{"text":"叉烧包"}]}],"generationConfig":{"imageConfig":{"output":"url"}}}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1beta/models/"):
			if r.URL.RawQuery != "output=url" {
				t.Fatalf("submit query = %q, want output=url", r.URL.RawQuery)
			}
			gr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Fatalf("gzip.NewReader() error = %v", err)
			}
			defer gr.Close()
			bodyBytes, err := io.ReadAll(gr)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(bodyBytes) != customBody {
				t.Fatalf("submit body = %s, want %s", string(bodyBytes), customBody)
			}
			writeJSONForTest(w, http.StatusAccepted, map[string]any{
				"id":     "task-2",
				"status": "accepted",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-2":
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"id":     "task-2",
				"status": "succeeded",
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{
								map[string]any{
									"inlineData": map[string]any{
										"data": "https://example.com/custom.png",
									},
								},
							},
						},
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks":
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"items": []any{
					map[string]any{
						"id":          "task-2",
						"content_url": "/v1/tasks/task-2/content",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-2/content":
			w.Header().Set("Location", "https://example.com/custom.png")
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		GatewayBaseURL: server.URL,
		APIKey:         "sk-test",
		Model:          "gemini-3-pro-image-preview",
		Prompt:         "draw banana",
		RequestBody:    []byte(customBody),
		PollInterval:   10 * time.Millisecond,
		Timeout:        5 * time.Second,
		HTTPClient:     server.Client(),
	})

	result, err := client.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ImageURL != "https://example.com/custom.png" {
		t.Fatalf("unexpected result = %#v", result)
	}
}

func TestClientRunFailsOnUncertain(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1beta/models/"):
			writeJSONForTest(w, http.StatusAccepted, map[string]any{
				"id":     "task-1",
				"status": "accepted",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-1":
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"id":                  "task-1",
				"status":              "uncertain",
				"transport_uncertain": true,
				"error": map[string]any{
					"code":    "upstream_transport_uncertain",
					"message": "connection to newapi broke after request dispatch; task result may be uncertain",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		GatewayBaseURL: server.URL,
		APIKey:         "sk-test",
		Model:          "gemini-3-pro-image-preview",
		Prompt:         "draw banana",
		PollInterval:   10 * time.Millisecond,
		Timeout:        5 * time.Second,
		HTTPClient:     server.Client(),
	})

	_, err := client.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "upstream_transport_uncertain") {
		t.Fatalf("expected uncertain error, got %v", err)
	}
}

func writeJSONForTest(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
