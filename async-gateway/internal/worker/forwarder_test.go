package worker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"banana-async-gateway/internal/domain"
	"banana-async-gateway/internal/security"
)

const forwarderTestKeyBase64 = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func TestForwarderPreservesPathQueryBodyAndAuthorization(t *testing.T) {
	t.Parallel()

	var (
		gotPath            string
		gotQuery           string
		gotAuth            string
		gotContentEncoding string
		gotBody            string
		dispatched         atomic.Bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotContentEncoding = r.Header.Get("Content-Encoding")
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"responseId":"resp-1","modelVersion":"gemini-3-pro-image-preview","usageMetadata":{"totalTokenCount":12},"candidates":[{"finishReason":"STOP","content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"https://example.com/final.png"}}]}}]}`))
	}))
	defer server.Close()

	forwarder := newForwarderForTest(t, server.URL)
	task, payload := makeForwardFixture(t)

	outcome, err := forwarder.Forward(context.Background(), task, payload, func(context.Context) error {
		dispatched.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if !dispatched.Load() {
		t.Fatalf("expected onDispatched callback")
	}
	if outcome.ErrorCode != "" || outcome.TransportUncertain {
		t.Fatalf("unexpected forward outcome = %#v", outcome)
	}
	if gotPath != task.RequestPath || gotQuery != task.RequestQuery {
		t.Fatalf("path/query mismatch got=%s?%s want=%s?%s", gotPath, gotQuery, task.RequestPath, task.RequestQuery)
	}
	if gotAuth != "Bearer sk-live" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer sk-live")
	}
	if gotContentEncoding != "" {
		t.Fatalf("Content-Encoding = %q, want empty", gotContentEncoding)
	}
	if gotBody != string(payload.RequestBodyJSON) {
		t.Fatalf("body mismatch got=%s want=%s", gotBody, string(payload.RequestBodyJSON))
	}
	if outcome.Summary == nil || len(outcome.Summary.ImageURLs) != 1 {
		t.Fatalf("expected success summary, got %#v", outcome.Summary)
	}
}

func TestForwarderPreservesTrustedForwardHeaders(t *testing.T) {
	t.Parallel()

	var (
		gotForwardedFor   string
		gotRealIP         string
		gotForwardedProto string
		gotForwarded      string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwardedFor = r.Header.Get("X-Forwarded-For")
		gotRealIP = r.Header.Get("X-Real-IP")
		gotForwardedProto = r.Header.Get("X-Forwarded-Proto")
		gotForwarded = r.Header.Get("Forwarded")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"inlineData":{"data":"https://example.com/final.png","mimeType":"image/png"}}]}}]}`))
	}))
	defer server.Close()

	forwarder := newForwarderForTest(t, server.URL)
	task, payload := makeForwardFixture(t)
	payload.ForwardHeaders["X-Forwarded-For"] = "203.0.113.10, 10.0.0.2"
	payload.ForwardHeaders["X-Real-IP"] = "203.0.113.10"
	payload.ForwardHeaders["X-Forwarded-Proto"] = "https"
	payload.ForwardHeaders["Forwarded"] = `for=203.0.113.10;proto=https;host=async.example.com`

	outcome, err := forwarder.Forward(context.Background(), task, payload, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if outcome.ErrorCode != "" || outcome.Summary == nil {
		t.Fatalf("unexpected outcome = %#v", outcome)
	}
	if gotForwardedFor != "203.0.113.10, 10.0.0.2" {
		t.Fatalf("X-Forwarded-For = %q", gotForwardedFor)
	}
	if gotRealIP != "203.0.113.10" {
		t.Fatalf("X-Real-IP = %q", gotRealIP)
	}
	if gotForwardedProto != "https" {
		t.Fatalf("X-Forwarded-Proto = %q", gotForwardedProto)
	}
	if gotForwarded != `for=203.0.113.10;proto=https;host=async.example.com` {
		t.Fatalf("Forwarded = %q", gotForwarded)
	}
}

func TestForwarderClassifiesHTTPStatusCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		status        int
		wantCode      string
		wantUncertain bool
	}{
		{name: "401", status: http.StatusUnauthorized, wantCode: "auth_failed"},
		{name: "403", status: http.StatusForbidden, wantCode: "auth_failed"},
		{name: "402", status: http.StatusPaymentRequired, wantCode: "insufficient_balance"},
		{name: "429", status: http.StatusTooManyRequests, wantCode: "upstream_rate_limited"},
		{name: "500", status: http.StatusInternalServerError, wantCode: "upstream_error"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "upstream failed", tc.status)
			}))
			defer server.Close()

			forwarder := newForwarderForTest(t, server.URL)
			task, payload := makeForwardFixture(t)

			outcome, err := forwarder.Forward(context.Background(), task, payload, func(context.Context) error { return nil })
			if err != nil {
				t.Fatalf("Forward() error = %v", err)
			}
			if outcome.ErrorCode != tc.wantCode || outcome.TransportUncertain != tc.wantUncertain {
				t.Fatalf("unexpected outcome = %#v", outcome)
			}
		})
	}
}

func TestForwarderMarksTransportUncertainWhenConnectionDropsAfterDispatch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("response writer is not hijackable")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"candidates\":[")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer server.Close()

	forwarder := newForwarderForTest(t, server.URL)
	task, payload := makeForwardFixture(t)

	outcome, err := forwarder.Forward(context.Background(), task, payload, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if !outcome.TransportUncertain || outcome.ErrorCode != "upstream_transport_uncertain" {
		t.Fatalf("unexpected outcome = %#v", outcome)
	}
}

func TestForwarderFailsSingle408WithoutRetry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "timeout", http.StatusRequestTimeout)
	}))
	defer server.Close()

	forwarder := newForwarderForTest(t, server.URL)
	task, payload := makeForwardFixture(t)

	outcome, err := forwarder.Forward(context.Background(), task, payload, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}
	if calls.Load() != 1 || outcome.ErrorCode != "upstream_timeout" {
		t.Fatalf("unexpected timeout outcome calls=%d outcome=%#v", calls.Load(), outcome)
	}
}

func newForwarderForTest(t *testing.T, baseURL string) *Forwarder {
	t.Helper()

	key, err := security.ParseEncryptionKey(forwarderTestKeyBase64)
	if err != nil {
		t.Fatalf("ParseEncryptionKey() error = %v", err)
	}

	return &Forwarder{
		baseURL:        baseURL,
		client:         &http.Client{Timeout: 5 * time.Second},
		encryptionKey:  key,
		requestTimeout: 5 * time.Second,
	}
}

func makeForwardFixture(t *testing.T) (*domain.Task, *domain.TaskPayload) {
	t.Helper()

	key, err := security.ParseEncryptionKey(forwarderTestKeyBase64)
	if err != nil {
		t.Fatalf("ParseEncryptionKey() error = %v", err)
	}
	authCiphertext, err := security.EncryptAuthorization("Bearer sk-live", key)
	if err != nil {
		t.Fatalf("EncryptAuthorization() error = %v", err)
	}

	return &domain.Task{
			ID:           "task-1",
			Model:        "gemini-3-pro-image-preview",
			RequestPath:  "/v1beta/models/gemini-3-pro-image-preview:generateContent",
			RequestQuery: "output=url",
		}, &domain.TaskPayload{
			TaskID:             "task-1",
			RequestBodyJSON:    []byte(`{"contents":[{"parts":[{"text":"draw cat"}]}]}`),
			ForwardHeaders:     map[string]string{"Content-Type": "application/json", "Accept": "application/json"},
			AuthorizationCrypt: authCiphertext,
		}
}
