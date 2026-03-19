package smoketest

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientRunFailsWhenInlineDataIsNotHTTPURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1beta/models/"):
			gr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Fatalf("gzip.NewReader() error = %v", err)
			}
			defer gr.Close()
			if _, err := io.ReadAll(gr); err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			writeJSONForTest(w, http.StatusAccepted, map[string]any{
				"id":     "task-bad-inline-data",
				"status": "accepted",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-bad-inline-data":
			writeJSONForTest(w, http.StatusOK, map[string]any{
				"id":     "task-bad-inline-data",
				"status": "succeeded",
				"candidates": []any{
					map[string]any{
						"content": map[string]any{
							"parts": []any{
								map[string]any{
									"inlineData": map[string]any{
										"data": "iVBORw0KGgoAAAANSUhEUgAAAAUA",
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
						"id":          "task-bad-inline-data",
						"content_url": "/v1/tasks/task-bad-inline-data/content",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-bad-inline-data/content":
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
		Timeout:        2 * time.Second,
		HTTPClient:     server.Client(),
	})

	_, err := client.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid image url") {
		t.Fatalf("expected invalid image url error, got %v", err)
	}
}
